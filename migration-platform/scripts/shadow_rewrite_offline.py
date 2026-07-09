#!/usr/bin/env python3
"""OFFLINE / FIXTURE-ONLY harness for the SSH-DEST shadow-rewrite spike (DESIGN FIX v4).

This module is **offline only** and **fixture only**: it operates on temporary
fixture files, never opens an SSH connection, never reads or writes a real
``~/etc/<dom>/shadow``, and never calls cPanel/UAPI. It exists to implement and
test the guarded primitive that the (future) live path would run on the
destination.

The primitive REFUSES to run against anything outside an explicit
``fixture_root`` (realpath containment) and against ``/etc``/the system shadow,
so it cannot be pointed at a real file by accident and a future SSH runner
cannot silently reuse it to bypass the live-gates (F9). A real destination
rewrite MUST go through a separate live runner that owns those gates.

Core primitive::

    guarded_rewrite(shadow_path, username, expected_current, new_value, ...)

used identically for forward (H_pre->H_new), rollback (H_new->H_pre) and the
two discriminating-test writes (H_pre->H_tw, H_tw->H_new).

Safety contract mirrored from the design review (FV1-FV6 / R1-R5):
* the field-2 byte-splice is performed by the static ``shadow_splice.pl`` worker
  (perl is REQUIRED; perl absent => hard-abort). No ``awk {print}``;
* ``expected_current`` / ``new_value`` reach the worker ONLY via structured
  stdin, never argv/env;
* a line-scoped premig snapshot (target line only, owner-only, O_EXCL) is
  mandatory and makes ALERT_MANUAL recoverable;
* a SHA-256 content-fingerprint is captured before the rewrite and re-checked
  right before the atomic rename (CAS against same-size/same-second writes);
* the committed temp is verified byte-for-byte against an independently
  reconstructed ``expected_temp`` (diff-scope byte-level; line-count is only a
  cheap early-out);
* the file's final-newline state and every byte outside field 2 are preserved;
* audit records are redacted (run-id/operator-id/correlation-id + state only) —
  no hash, password, shadow line or secret path ever appears in output/logs.
"""

from __future__ import annotations

import glob
import hashlib
import json
import os
import re
import shutil
import subprocess
import time
from dataclasses import dataclass, field
from enum import Enum
from pathlib import Path
from typing import Any, Callable, Iterable

_HERE = Path(__file__).resolve().parent
_SPLICE_PL = _HERE / "shadow_splice.pl"
_PREMIG_PREFIX = ".shadow.premig."
_MIG_PREFIX = ".shadow.mig."           # worker temp files (may carry the NEW hash)
_MIN_FIELDS_DEFAULT = 2


# --------------------------------------------------------------------------- #
# Terminal state machine
# --------------------------------------------------------------------------- #
class State(str, Enum):
    SAFE_ABORT = "SAFE_ABORT"                    # pre-mutation failure; live intact
    NOOP_ALREADY_MATCHING = "NOOP_ALREADY_MATCHING"
    UPDATED = "UPDATED"                          # primitive success: bytes written + read-back OK
    ALERT_MANUAL = "ALERT_MANUAL"                # post-mutation failure; manual recovery via premig


@dataclass(frozen=True)
class Ids:
    run_id: str
    operator_id: str
    correlation_id: str


@dataclass
class Tools:
    """Resolved external tooling. ``context_available`` models whether SELinux/
    ACL/xattr preservation tools exist (CageFS simulation): absent -> durability
    warning, not a hard fail. ``perl``/``splice_pl`` absent -> hard-abort."""

    perl: str | None
    splice_pl: str
    context_available: bool = True

    @classmethod
    def resolve(cls, *, context_available: bool = True) -> "Tools":
        return cls(perl=shutil.which("perl"), splice_pl=str(_SPLICE_PL),
                   context_available=context_available)


@dataclass
class Hooks:
    """Test seams (offline only). Never used in production."""

    before_fingerprint2: Callable[[str], None] | None = None  # mutate live file -> fp mismatch
    abort_before_mv: bool = False                              # simulate a failed write, file untouched
    after_mv: Callable[[str], None] | None = None             # post-rename fault: corrupt -> readback_mismatch; raise -> exception_after_mv


@dataclass
class RewriteResult:
    state: State
    reason: str
    audit: dict[str, Any]
    premig_path: str | None = None
    warnings: list[str] = field(default_factory=list)


# --------------------------------------------------------------------------- #
# Redaction
# --------------------------------------------------------------------------- #
def _assert_no_secrets(text: str, secrets: Iterable[str]) -> None:
    for s in secrets:
        if s and s in text:
            raise RuntimeError("secret leaked into harness output")
    # crypt-hash shapes must never appear in emitted text
    if re.search(r"\$(?:1|5|6|2[aby]?|y|argon2)\$", text):
        raise RuntimeError("hash-like value leaked into harness output")


# --------------------------------------------------------------------------- #
# Byte-level field-2 location (Python side: pre-checks + diff-scope rebuild)
# --------------------------------------------------------------------------- #
def _field2_matches(data: bytes, username: str) -> list[re.Match[bytes]]:
    rx = re.compile(rb"(?m)^" + re.escape(username.encode()) + rb":([^:\n]*)")
    return list(rx.finditer(data))


def _line_bounds(data: bytes, start: int) -> tuple[int, int]:
    end = data.find(b"\n", start)
    return (start, len(data) if end < 0 else end)


def _sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


# --------------------------------------------------------------------------- #
# Premig (line-scoped, owner-only) + orphan sweep  (R1)
# --------------------------------------------------------------------------- #
def sweep_orphan_premigs(directory: str, keep: str | None = None) -> list[str]:
    """Remove crash-orphaned artefacts left by a killed run: line-scoped premig
    snapshots (``.shadow.premig.*``) AND worker temp files (``.shadow.mig.*``).

    The worker temp is swept too because a process killed between the temp's
    creation and the atomic rename would otherwise leave a ``.shadow.mig.*``
    file on disk that CONTAINS THE NEW HASH (R1 / F2). Backward compatible: the
    behaviour on premig files is unchanged; ``keep`` still protects one path."""
    removed: list[str] = []
    keep_abs = os.path.abspath(keep) if keep is not None else None
    for prefix in (_PREMIG_PREFIX, _MIG_PREFIX):
        for p in glob.glob(os.path.join(directory, prefix + "*")):
            if keep_abs is not None and os.path.abspath(p) == keep_abs:
                continue
            try:
                os.unlink(p)
                removed.append(p)
            except OSError:
                pass
    return removed


# --------------------------------------------------------------------------- #
# Offline-only guard (F9): make an accidental LIVE use impossible without an
# explicit, noisy opt-in. This module is FIXTURE-ONLY; a real destination
# rewrite must go through a separate live runner with its own live-gates
# (--i-will-rewrite-destination-shadow, typed target, maintenance window).
# --------------------------------------------------------------------------- #
def _contained(child_real: str, root_real: str) -> bool:
    """True iff ``child_real`` is at/under ``root_real`` (component-aware, so a
    sibling like ``/a/bc`` is NOT considered under ``/a/b``). Inputs must be
    realpath-resolved by the caller so symlink/``..`` escapes cannot slip past."""
    try:
        return os.path.commonpath([child_real, root_real]) == root_real
    except ValueError:  # different drive / mixed absolute-relative
        return False


def offline_guard_ok(shadow_path: str, fixture_root: str) -> bool:
    """Fixture confinement check. Returns True only when ``shadow_path`` resolves
    INSIDE ``fixture_root`` and does NOT resolve under ``/etc`` or to the system
    shadow. Both sides are ``os.path.realpath``-resolved so symlinks and ``..``
    cannot be used to escape the fixture sandbox. The real destination shadow
    lives under ``~/etc/<dom>/shadow`` (i.e. under ``/home*``), so we do NOT
    blanket-reject ``/home``; instead we require positive containment under an
    explicit fixture root, which a live path could only satisfy by loudly
    pointing this OFFLINE primitive at production — the opt-in F9 forbids."""
    real = os.path.realpath(shadow_path)
    root = os.path.realpath(fixture_root)
    if not _contained(real, root):
        return False
    etc = os.path.realpath("/etc")
    if real == os.path.join(etc, "shadow") or _contained(real, etc):
        return False
    return True


def _write_premig(directory: str, run_id: str, target_line: bytes) -> str:
    """Line-scoped premig: only the target line, owner-only, O_EXCL. Raises on
    a non-writable directory (=> SAFE_ABORT upstream)."""
    path = os.path.join(directory, _PREMIG_PREFIX + run_id)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "wb") as f:
        f.write(target_line)
    return path


# --------------------------------------------------------------------------- #
# Perl worker invocation (hashes via stdin only)
# --------------------------------------------------------------------------- #
_PERL_ERR_REASON = {
    "args": "worker_args", "stdin": "worker_stdin", "reject_new": "reject_value",
    "open": "shadow_unreadable", "empty_file": "shadow_empty", "encoding": "encoding",
    "seen": "seen", "nf": "nf", "cas": "cas", "empty_needs_confirm": "empty_needs_confirm",
    "write": "worker_write",
}


def _run_worker(tools: Tools, shadow_path: str, username: str, expected_current: str,
                new_value: str, min_fields: int, allow_empty: bool) -> tuple[bool, str]:
    proc = subprocess.run(
        [tools.perl, tools.splice_pl, shadow_path, username, str(min_fields),
         "1" if allow_empty else "0"],
        input=(expected_current + "\n" + new_value + "\n").encode(),
        capture_output=True, timeout=30,
    )
    out = proc.stdout.decode(errors="replace").strip()
    if proc.returncode == 0 and out.startswith("OK "):
        return True, out[3:].strip()  # temp basename
    token = out.split()[1] if out.startswith("ERR ") and len(out.split()) > 1 else "worker"
    return False, _PERL_ERR_REASON.get(token, "worker")


# --------------------------------------------------------------------------- #
# The guarded primitive
# --------------------------------------------------------------------------- #
def _mutation_committed(state: State) -> bool:
    """States that imply bytes already landed on the target (premig must then
    survive for recovery): UPDATED (success) and ALERT_MANUAL (post-mv fault)."""
    return state in (State.UPDATED, State.ALERT_MANUAL)


def guarded_rewrite(
    shadow_path: str,
    username: str,
    expected_current: str,
    new_value: str,
    *,
    ids: Ids,
    tools: Tools,
    fixture_root: str,
    min_fields: int = _MIN_FIELDS_DEFAULT,
    allow_empty: bool = False,
    action: str = "shadow_rewrite",
    audit_sink: Callable[[dict[str, Any]], None] | None = None,
    hooks: Hooks | None = None,
    clock: Callable[[], float] = time.time,
) -> RewriteResult:
    """Single guarded, at-most-once field-2 rewrite — OFFLINE / FIXTURE-ONLY.

    No SSH, no network, no cPanel/UAPI. ``fixture_root`` is MANDATORY and
    confines every operation: ``shadow_path`` must resolve inside it (realpath
    containment) and must not resolve under ``/etc`` or to the system shadow,
    else the call fails closed with ``SAFE_ABORT``/``offline_guard`` before any
    read or write. This makes an accidental live rewrite impossible without a
    loud, explicit opt-in; a real destination rewrite must use a separate live
    runner with its own live-gates (F9)."""
    hooks = hooks or Hooks()
    secrets = [v for v in (expected_current, new_value) if v]
    directory = os.path.dirname(os.path.abspath(shadow_path))
    audit: dict[str, Any] = {
        "run_id": ids.run_id, "operator_id": ids.operator_id,
        "correlation_id": ids.correlation_id, "action": action,
        "target_localpart": username, "ts": round(clock(), 3),
        "durability_warning": not tools.context_available,
    }
    warnings: list[str] = []
    premig_path: str | None = None

    def finish(state: State, reason: str, *, keep_premig: bool = False) -> RewriteResult:
        """Terminal: stamp+redact the audit, emit it, reclaim the premig.

        Emits the audit BEFORE reclaiming the premig so a committed mutation
        stays recoverable if the sink dies. Audit-sink failures are handled
        here (F4): they never propagate and never leak an orphan. On a
        committed state an audit failure escalates to ALERT_MANUAL with the
        premig retained (T37/T38); on a pre-mutation state the live file is
        intact, so we keep the safe state but surface the audit failure as a
        non-silent warning and still reclaim the premig."""
        nonlocal premig_path
        audit["state"] = state.value
        audit["reason"] = reason
        audit["premig"] = os.path.basename(premig_path) if premig_path else None
        blob = json.dumps(audit)
        _assert_no_secrets(blob, secrets)

        audit_failed = False
        if audit_sink is not None:
            try:
                audit_sink(dict(audit))
            except Exception:
                audit_failed = True

        if audit_failed:
            if _mutation_committed(state):
                # bytes landed but audit could not be persisted -> not silent:
                # ALERT_MANUAL, premig retained for manual recovery.
                audit["state"] = State.ALERT_MANUAL.value
                audit["reason"] = "audit_write_failed"
                return RewriteResult(State.ALERT_MANUAL, "audit_write_failed",
                                     audit, premig_path, warnings)
            # pre-mutation: live intact; keep the safe state, flag the failure.
            if "audit_write_failed" not in warnings:
                warnings.append("audit_write_failed")

        if premig_path and not keep_premig:
            _safe_unlink(premig_path)
            premig_path = None
            audit["premig"] = None
        return RewriteResult(state, reason, audit, premig_path, warnings)

    # -- offline confinement guard (F9): fail closed before any I/O --------- #
    if not offline_guard_ok(shadow_path, fixture_root):
        return finish(State.SAFE_ABORT, "offline_guard")

    # -- perl is mandatory: absent => hard-abort ---------------------------- #
    if not tools.perl or not os.access(tools.perl, os.X_OK) or not os.path.exists(tools.splice_pl):
        return finish(State.SAFE_ABORT, "perl_missing")

    # -- value hygiene ------------------------------------------------------ #
    for v in (expected_current, new_value):
        if ":" in v or "\n" in v or "\x00" in v or "\r" in v:
            return finish(State.SAFE_ABORT, "reject_value")

    # -- read live bytes ---------------------------------------------------- #
    try:
        data = Path(shadow_path).read_bytes()
    except OSError:
        return finish(State.SAFE_ABORT, "shadow_unreadable")
    if not data:
        return finish(State.SAFE_ABORT, "shadow_empty")
    if b"\r" in data or b"\x00" in data:
        return finish(State.SAFE_ABORT, "encoding")

    # -- no-op -------------------------------------------------------------- #
    if expected_current == new_value:
        return finish(State.NOOP_ALREADY_MATCHING, "noop")

    # -- locate target (Python mirror of the worker's structural checks) ---- #
    matches = _field2_matches(data, username)
    if len(matches) != 1:
        return finish(State.SAFE_ABORT, "seen")
    m = matches[0]
    f2s, f2e = m.start(1), m.end(1)
    ls, le = _line_bounds(data, m.start())
    target_line = data[ls:le]
    if len(target_line.split(b":")) < min_fields:
        return finish(State.SAFE_ABORT, "nf")
    cur = data[f2s:f2e].decode("latin-1")
    if cur != expected_current:
        return finish(State.SAFE_ABORT, "cas")
    if cur == "" and not allow_empty:
        warnings.append("empty_field2_no_auth_to_auth")
        return finish(State.SAFE_ABORT, "empty_needs_confirm")

    # -- fingerprint + mandatory line-scoped premig ------------------------- #
    fp0 = _sha256(data)
    audit["fp0"] = fp0[:12]
    try:
        premig_path = _write_premig(directory, ids.run_id, target_line)
    except OSError:
        return finish(State.SAFE_ABORT, "premig_failed")

    # -- mutative section: try/finally guarantees the worker temp is reclaimed
    #    on EVERY exit (success, any SAFE_ABORT, or an unhandled exception like
    #    a worker timeout), and an unexpected exception maps to a safe state
    #    (SAFE_ABORT pre-rename, ALERT_MANUAL post-rename). (F1/F2/F3) -------- #
    worker_temp: str | None = None
    renamed = False
    try:
        ok, payload = _run_worker(tools, shadow_path, username, expected_current,
                                  new_value, min_fields, allow_empty)
        if not ok:
            return finish(State.SAFE_ABORT, payload)
        worker_temp = os.path.join(directory, payload)
        try:
            temp_bytes = Path(worker_temp).read_bytes()
        except OSError:
            return finish(State.SAFE_ABORT, "temp_unreadable")

        # -- diff-scope byte-level (line-count is only a cheap early-out) ---- #
        expected_temp = data[:f2s] + new_value.encode("latin-1") + data[f2e:]
        line_count_equal = data.count(b"\n") == temp_bytes.count(b"\n")
        if not line_count_equal or temp_bytes != expected_temp:
            return finish(State.SAFE_ABORT, "diff_scope")

        # -- CAS via content-fingerprint just before the rename ------------- #
        if hooks.before_fingerprint2 is not None:
            hooks.before_fingerprint2(shadow_path)
        try:
            fp1 = _sha256(Path(shadow_path).read_bytes())
        except OSError:
            return finish(State.SAFE_ABORT, "shadow_unreadable")
        if fp1 != fp0:
            return finish(State.SAFE_ABORT, "fingerprint_mismatch")

        if hooks.abort_before_mv:
            return finish(State.SAFE_ABORT, "injected_abort")

        if not tools.context_available:
            warnings.append("context_tooling_absent_durability_warning")

        # -- atomic rename (at-most-once; no retry) ------------------------- #
        os.replace(worker_temp, shadow_path)
        renamed = True
        worker_temp = None  # consumed by the rename; nothing to reclaim

        if hooks.after_mv is not None:
            hooks.after_mv(shadow_path)

        # -- read-back byte-equality ---------------------------------------- #
        rb = _field2_matches(Path(shadow_path).read_bytes(), username)
        if len(rb) != 1 or rb[0].group(1).decode("latin-1") != new_value:
            return finish(State.ALERT_MANUAL, "readback_mismatch", keep_premig=True)

        return finish(State.UPDATED, "ok")
    except Exception:
        # Unhandled fault (e.g. worker timeout). Map to a safe terminal state;
        # the finally clause below still reclaims the worker temp.
        if renamed:
            return finish(State.ALERT_MANUAL, "exception_after_mv", keep_premig=True)
        return finish(State.SAFE_ABORT, "exception_pre_mv")
    finally:
        if worker_temp is not None:
            _safe_unlink(worker_temp)


def _safe_unlink(path: str) -> None:
    try:
        os.unlink(path)
    except OSError:
        pass


# --------------------------------------------------------------------------- #
# Schema gate (R5 / FV7): source-scheme supported by destination
# --------------------------------------------------------------------------- #
_PORTABLE = {"SHA-512", "SHA-256", "MD5"}


def classify_scheme(h: str) -> str:
    if h == "":
        return "EMPTY"
    if h[0] in "!*":
        return "LOCKED"
    if h.startswith("$6$"):
        return "SHA-512"
    if h.startswith("$5$"):
        return "SHA-256"
    if h.startswith("$1$"):
        return "MD5"
    if h.startswith("$2"):
        return "bcrypt"
    if h.startswith("$y$"):
        return "yescrypt"
    if h.startswith("$argon2"):
        return "Argon2"
    return "UNKNOWN"


def scheme_gate(h: str, *, login_available: bool) -> tuple[bool, str, str]:
    """Return (allowed, scheme, note). Portable schemes pass; non-portable pass
    only with functional login evidence; EMPTY/LOCKED/UNKNOWN fail closed."""
    s = classify_scheme(h)
    if s in ("EMPTY", "LOCKED", "UNKNOWN"):
        return (False, s, "fail_closed")
    if s in _PORTABLE:
        note = "portable_md5_warn" if s == "MD5" else "portable"
        return (True, s, note)
    if login_available:
        return (True, s, "nonportable_login_evidence")
    return (False, s, "nonportable_needs_login")


# --------------------------------------------------------------------------- #
# Simulated auth (offline): passdb over the shadow + optional masking + cache
# --------------------------------------------------------------------------- #
def fake_crypt(plaintext: str, scheme: str = "$6$") -> str:
    return scheme + "salt$" + hashlib.sha256(plaintext.encode()).hexdigest()


class AuthOracle:
    """Deterministic offline stand-in for Dovecot auth. Reads the current
    field-2 from the shadow fixture; an optional ``masking`` set models a
    preceding passdb (e.g. SQL) that would hide the shadow's authority; an
    auth cache with TTL models stale positives that a naive negative control
    would miss (must be cache-defeated by advancing ``now``)."""

    def __init__(self, shadow_path: str, username: str, *, masking: Iterable[str] | None = None,
                 cache_ttl: int = 3600) -> None:
        self.shadow_path = shadow_path
        self.username = username
        self.masking = set(masking or [])
        self.cache_ttl = cache_ttl
        self._cache: dict[str, tuple[bool, float]] = {}

    def _current_hash(self) -> str | None:
        data = Path(self.shadow_path).read_bytes()
        m = re.search(rb"(?m)^" + re.escape(self.username.encode()) + rb":([^:\n]*)", data)
        return m.group(1).decode("latin-1") if m else None

    def login(self, plaintext: str, *, now: float, defeat_cache: bool = False) -> bool:
        if not defeat_cache and plaintext in self._cache:
            res, t = self._cache[plaintext]
            if now - t < self.cache_ttl:
                return res
        if plaintext in self.masking:
            res = True
        else:
            res = fake_crypt(plaintext) == self._current_hash()
        self._cache[plaintext] = (res, now)
        return res


# --------------------------------------------------------------------------- #
# SPIKE_VALIDATION: passdb discriminating test + LOGIN_CONFIRMED (R2 / FV3)
# --------------------------------------------------------------------------- #
def spike_discriminating_and_confirm(
    shadow_path: str,
    username: str,
    *,
    p_pre: str, h_pre: str,
    p_tw: str, h_tw: str,
    p_src: str, h_new: str,
    oracle: AuthOracle,
    ids: Ids,
    tools: Tools,
    fixture_root: str,
    clock_holder: dict[str, float],
    cache_ttl: int,
    second_write_hooks: Hooks | None = None,
) -> dict[str, Any]:
    """Prove the shadow drives auth (not a masking passdb), then write the
    preserved hash and confirm login. The negative control is cache-defeated
    by advancing the clock past the TTL. Returns a result dict (no secrets).
    OFFLINE / FIXTURE-ONLY: ``fixture_root`` confines every guarded rewrite."""

    def now() -> float:
        return clock_holder["now"]

    # 1) H_pre -> H_tw
    r1 = guarded_rewrite(shadow_path, username, h_pre, h_tw, ids=ids, tools=tools,
                         fixture_root=fixture_root, clock=now)
    if r1.state is not State.UPDATED:
        return {"result": "ABORT", "stage": "write_tw", "state": r1.state.value}

    # 2) throwaway login must succeed
    if not oracle.login(p_tw, now=now()):
        return {"result": "ABORT", "stage": "login_tw"}

    # 3) cache-defeating negative control: old password must now fail
    clock_holder["now"] += cache_ttl + 1
    if oracle.login(p_pre, now=now()):
        return {"result": "ABORT_AUTHORITY", "stage": "negative_control"}

    # 4) H_tw -> H_new (failure here => stuck-at-H_tw sub-state machine)
    r2 = guarded_rewrite(shadow_path, username, h_tw, h_new, ids=ids, tools=tools,
                         fixture_root=fixture_root, hooks=second_write_hooks, clock=now)
    if r2.state is not State.UPDATED:
        # recover to H_pre; if that also fails -> ALERT_MANUAL (P_tw is known, no hard lockout)
        rec = guarded_rewrite(shadow_path, username, h_tw, h_pre, ids=ids, tools=tools,
                              fixture_root=fixture_root, clock=now)
        if rec.state is State.UPDATED:
            return {"result": "STUCK_RECOVERED_TO_PRE", "stage": "write_new"}
        return {"result": "ALERT_MANUAL", "stage": "write_new"}

    # 5) LOGIN_CONFIRMED with the preserved password (cache-defeated)
    clock_holder["now"] += cache_ttl + 1
    if oracle.login(p_src, now=now()):
        return {"result": "LOGIN_CONFIRMED"}
    return {"result": "LOGIN_FAILED_AFTER_TTL"}
