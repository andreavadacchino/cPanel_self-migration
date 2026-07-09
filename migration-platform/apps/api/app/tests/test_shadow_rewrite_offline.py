"""Offline tests for the SSH-DEST shadow-rewrite harness (DESIGN FIX v4).

Fully offline: every test operates on a temporary fixture shadow file. There is
NO SSH, NO real shadow, NO cPanel/UAPI call, NO server mutation. The only
"mutations" are byte edits to throwaway fixtures under pytest's tmp_path.

Every ``guarded_rewrite`` / ``spike_discriminating_and_confirm`` call passes
``fixture_root=str(tmp_path)`` — the OFFLINE confinement guard (F9) requires it.
"""

from __future__ import annotations

import importlib.util
import json
import os
import subprocess
import sys
from pathlib import Path

import pytest


def _load():
    root = Path(__file__).resolve().parents[4]
    path = root / "scripts" / "shadow_rewrite_offline.py"
    spec = importlib.util.spec_from_file_location("shadow_rewrite_offline", path)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)
    return mod


mod = _load()
Ids = mod.Ids
Tools = mod.Tools
Hooks = mod.Hooks
State = mod.State
guarded_rewrite = mod.guarded_rewrite
fake_crypt = mod.fake_crypt

IDS = Ids("run-abc", "op-42", "corr-99")

# Real worker, invoked directly in the F5 barrier-live tests.
PERL = Tools.resolve().perl
SPLICE = str(mod._SPLICE_PL)
requires_perl = pytest.mark.skipif(PERL is None, reason="perl worker required")


def _tools(**kw):
    return Tools.resolve(**kw)


def _shadow(tmp_path, lines, trailing_newline=True):
    body = "\n".join(lines)
    if trailing_newline:
        body += "\n"
    p = tmp_path / "shadow"
    p.write_bytes(body.encode("latin-1"))
    return str(p)


def _field2(path, user="demobox"):
    data = Path(path).read_bytes()
    import re

    m = re.search(rb"(?m)^" + re.escape(user.encode()) + rb":([^:\n]*)", data)
    return m.group(1).decode("latin-1") if m else None


H_PRE = fake_crypt("old-password")
H_NEW = fake_crypt("source-password")
H_TW = fake_crypt("throwaway-xyz")


def _std_lines(h_demobox=H_PRE):
    # demobox (target) + two siblings that must never be touched
    return [
        f"demobox:{h_demobox}:19000:0:99999:7:::",
        f"info:{fake_crypt('info-pw')}:19000:0:99999:7:::",
        f"sales:{fake_crypt('sales-pw')}:19000:0:99999:7:::",
    ]


# --------------------------------------------------------------------------- #
# Happy path (baseline)
# --------------------------------------------------------------------------- #
def test_forward_rewrite_updates_only_field2(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    before = Path(p).read_bytes()
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.UPDATED
    assert _field2(p) == H_NEW
    # siblings byte-preserved
    after = Path(p).read_bytes()
    assert before.replace(H_PRE.encode(), H_NEW.encode()) == after
    # premig cleaned up on success
    assert r.premig_path is None


def test_rollback_uses_same_primitive(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    assert guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                           fixture_root=str(tmp_path)).state is State.UPDATED
    r = guarded_rewrite(p, "demobox", H_NEW, H_PRE, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.UPDATED
    assert _field2(p) == H_PRE


def test_noop_when_expected_equals_new(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    r = guarded_rewrite(p, "demobox", H_PRE, H_PRE, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.NOOP_ALREADY_MATCHING
    assert _field2(p) == H_PRE


# --------------------------------------------------------------------------- #
# T35 directory non-writable / mktemp-failure -> SAFE_ABORT
# --------------------------------------------------------------------------- #
def test_T35_dir_non_writable_safe_abort(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    d = os.path.dirname(p)
    os.chmod(d, 0o500)  # remove owner write -> premig O_EXCL create fails
    try:
        r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                            fixture_root=str(tmp_path))
    finally:
        os.chmod(d, 0o700)
    assert r.state is State.SAFE_ABORT
    assert r.reason == "premig_failed"
    assert _field2(p) == H_PRE  # untouched


# --------------------------------------------------------------------------- #
# T36 content-fingerprint mismatch -> SAFE_ABORT pre-mv
# --------------------------------------------------------------------------- #
def test_T36_fingerprint_mismatch_safe_abort(tmp_path):
    p = _shadow(tmp_path, _std_lines())

    def mutate(path):  # concurrent writer between fp0 and pre-mv fp1
        with open(path, "ab") as f:
            f.write(b"newuser:x:1::::::\n")

    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path),
                        hooks=Hooks(before_fingerprint2=mutate))
    assert r.state is State.SAFE_ABORT
    assert r.reason == "fingerprint_mismatch"
    assert _field2(p) == H_PRE  # our rewrite never committed


# --------------------------------------------------------------------------- #
# T37 premig line-scoped + ALERT_MANUAL recuperabile + cleanup/orphan-sweep
# --------------------------------------------------------------------------- #
def test_T37_premig_line_scoped_and_alert_recoverable(tmp_path):
    p = _shadow(tmp_path, _std_lines())

    def boom(_audit):
        raise OSError("audit sink down")

    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path), audit_sink=boom)
    # mutation landed but audit failed -> ALERT_MANUAL, premig retained
    assert r.state is State.ALERT_MANUAL
    assert r.premig_path is not None and os.path.exists(r.premig_path)
    premig_bytes = Path(r.premig_path).read_bytes()
    # line-scoped: exactly the ONE target line (contains H_pre), no sibling data
    assert premig_bytes == f"demobox:{H_PRE}:19000:0:99999:7:::".encode("latin-1")
    assert b"info:" not in premig_bytes and b"sales:" not in premig_bytes
    os.unlink(r.premig_path)


def test_T37_premig_cleaned_on_success(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.UPDATED
    leftovers = [f for f in os.listdir(os.path.dirname(p)) if f.startswith(".shadow.premig.")]
    assert leftovers == []


# --------------------------------------------------------------------------- #
# T38 audit-write-failure -> non prosegue silenziosamente
# --------------------------------------------------------------------------- #
def test_T38_audit_write_failure_not_silent(tmp_path):
    p = _shadow(tmp_path, _std_lines())

    def boom(_audit):
        raise RuntimeError("cannot persist audit")

    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path), audit_sink=boom)
    assert r.state is not State.UPDATED
    assert r.state is State.ALERT_MANUAL
    assert r.reason == "audit_write_failed"
    assert _field2(p) == H_NEW  # the mutation happened; it is just not silently "ok"


# --------------------------------------------------------------------------- #
# T39 file con e senza final newline -> preservazione byte-esatta
# --------------------------------------------------------------------------- #
@pytest.mark.parametrize("trailing", [True, False])
def test_T39_final_newline_preserved(tmp_path, trailing):
    p = _shadow(tmp_path, _std_lines(), trailing_newline=trailing)
    original = Path(p).read_bytes()
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.UPDATED
    after = Path(p).read_bytes()
    assert after == original.replace(H_PRE.encode(), H_NEW.encode())
    assert after.endswith(b"\n") is trailing  # final-newline state preserved exactly


# --------------------------------------------------------------------------- #
# T40 diff-scope byte-level cattura alterazioni fuori $2
# --------------------------------------------------------------------------- #
def test_T40_diff_scope_catches_out_of_field2(tmp_path, monkeypatch):
    p = _shadow(tmp_path, _std_lines())

    def tampering_worker(tools, path, user, expected, new, minf, allow_empty):
        # correct $2 splice BUT also corrupts a sibling byte (out of scope)
        data = bytearray(Path(path).read_bytes())
        data = bytearray(bytes(data).replace(expected.encode(), new.encode()))
        idx = bytes(data).find(b"info:")
        data[idx] = ord("X")  # flip a byte outside the target field
        tmp = os.path.join(os.path.dirname(path), ".shadow.mig.TAMPER")
        Path(tmp).write_bytes(bytes(data))
        return True, os.path.basename(tmp)

    monkeypatch.setattr(mod, "_run_worker", tampering_worker)
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.SAFE_ABORT
    assert r.reason == "diff_scope"
    assert _field2(p) == H_PRE  # never committed


# --------------------------------------------------------------------------- #
# T41 passdb discriminating test simulato (happy path -> LOGIN_CONFIRMED)
# --------------------------------------------------------------------------- #
def test_T41_discriminating_login_confirmed(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    oracle = mod.AuthOracle(p, "demobox", cache_ttl=100)
    holder = {"now": 0.0}
    res = mod.spike_discriminating_and_confirm(
        p, "demobox",
        p_pre="old-password", h_pre=H_PRE,
        p_tw="throwaway-xyz", h_tw=H_TW,
        p_src="source-password", h_new=H_NEW,
        oracle=oracle, ids=IDS, tools=_tools(), fixture_root=str(tmp_path),
        clock_holder=holder, cache_ttl=100,
    )
    assert res["result"] == "LOGIN_CONFIRMED"
    assert _field2(p) == H_NEW  # ends at the preserved hash


# --------------------------------------------------------------------------- #
# T42 LOGIN cache-defeating + non-portable scheme requires login evidence
# --------------------------------------------------------------------------- #
def test_T42a_login_cache_masks_until_defeated(tmp_path):
    p = _shadow(tmp_path, [f"demobox:{fake_crypt('A')}:1::::::"])
    oracle = mod.AuthOracle(p, "demobox", cache_ttl=100)
    assert oracle.login("A", now=0) is True  # cached positive
    guarded_rewrite(p, "demobox", fake_crypt("A"), fake_crypt("B"), ids=IDS, tools=_tools(),
                    fixture_root=str(tmp_path))
    assert oracle.login("A", now=50) is True   # STALE positive (masked by cache)
    assert oracle.login("A", now=250) is False  # cache-defeated -> truth


def test_T42b_scheme_gate_nonportable_requires_login(tmp_path):
    assert mod.scheme_gate("$1$salt$x", login_available=False)[0] is True   # MD5 portable
    assert mod.scheme_gate("$1$salt$x", login_available=False)[2] == "portable_md5_warn"
    assert mod.scheme_gate("$2y$10$abc", login_available=False)[0] is False  # bcrypt, no login
    assert mod.scheme_gate("$2y$10$abc", login_available=True)[0] is True    # bcrypt + login evidence
    for bad in ("", "!locked", "*", "plainweird"):
        assert mod.scheme_gate(bad, login_available=True)[0] is False


# --------------------------------------------------------------------------- #
# T43 premig line-scoped + orphan-sweep + crash-safe cleanup
# --------------------------------------------------------------------------- #
def test_T43_orphan_sweep_and_crash_safe_cleanup(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    d = os.path.dirname(p)
    # orphan sweep
    (Path(d) / ".shadow.premig.old1").write_bytes(b"x")
    (Path(d) / ".shadow.premig.old2").write_bytes(b"y")
    removed = mod.sweep_orphan_premigs(d)
    assert len(removed) == 2
    assert not any(f.startswith(".shadow.premig.") for f in os.listdir(d))

    # crash-safe cleanup: a SAFE_ABORT that happens AFTER premig creation must
    # not leave the premig behind
    def mutate(path):
        with open(path, "ab") as f:
            f.write(b"z:x:1::::::\n")

    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path),
                        hooks=Hooks(before_fingerprint2=mutate))
    assert r.state is State.SAFE_ABORT
    assert not any(f.startswith(".shadow.premig.") for f in os.listdir(d))


# --------------------------------------------------------------------------- #
# T44 discriminating negativo cache-masked + stuck-at-H_tw recovery
# --------------------------------------------------------------------------- #
def test_T44a_masking_passdb_aborts_authority(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    # a preceding passdb accepts the OLD password regardless of the shadow
    oracle = mod.AuthOracle(p, "demobox", masking={"old-password"}, cache_ttl=100)
    holder = {"now": 0.0}
    res = mod.spike_discriminating_and_confirm(
        p, "demobox",
        p_pre="old-password", h_pre=H_PRE,
        p_tw="throwaway-xyz", h_tw=H_TW,
        p_src="source-password", h_new=H_NEW,
        oracle=oracle, ids=IDS, tools=_tools(), fixture_root=str(tmp_path),
        clock_holder=holder, cache_ttl=100,
    )
    assert res["result"] == "ABORT_AUTHORITY"
    assert res["stage"] == "negative_control"


def test_T44b_stuck_at_htw_recovers_to_pre(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    oracle = mod.AuthOracle(p, "demobox", cache_ttl=100)
    holder = {"now": 0.0}
    # inject a failed 2nd write (H_tw -> H_new): SAFE_ABORT before mv, file stays H_tw
    res = mod.spike_discriminating_and_confirm(
        p, "demobox",
        p_pre="old-password", h_pre=H_PRE,
        p_tw="throwaway-xyz", h_tw=H_TW,
        p_src="source-password", h_new=H_NEW,
        oracle=oracle, ids=IDS, tools=_tools(), fixture_root=str(tmp_path),
        clock_holder=holder, cache_ttl=100,
        second_write_hooks=Hooks(abort_before_mv=True),
    )
    assert res["result"] == "STUCK_RECOVERED_TO_PRE"
    assert _field2(p) == H_PRE  # recovered to the original hash


# --------------------------------------------------------------------------- #
# T45 $2 vuoto -> warn/confirm no-auth->auth
# --------------------------------------------------------------------------- #
def test_T45_empty_field2_requires_confirm(tmp_path):
    p = _shadow(tmp_path, ["demobox::19000:0:99999:7:::"] + _std_lines()[1:])
    # without confirmation -> SAFE_ABORT + warning
    r = guarded_rewrite(p, "demobox", "", H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path), allow_empty=False)
    assert r.state is State.SAFE_ABORT
    assert r.reason == "empty_needs_confirm"
    assert "empty_field2_no_auth_to_auth" in r.warnings
    assert _field2(p) == ""  # untouched
    # with explicit confirmation -> proceeds
    r2 = guarded_rewrite(p, "demobox", "", H_NEW, ids=IDS, tools=_tools(),
                         fixture_root=str(tmp_path), allow_empty=True)
    assert r2.state is State.UPDATED
    assert _field2(p) == H_NEW


# --------------------------------------------------------------------------- #
# T46 CageFS/tooling assente -> SAFE_ABORT (perl) or DURABILITY_WARNING (context)
# --------------------------------------------------------------------------- #
def test_T46_context_tooling_absent_durability_warning(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS,
                        tools=_tools(context_available=False), fixture_root=str(tmp_path))
    assert r.state is State.UPDATED
    assert r.audit["durability_warning"] is True
    assert "context_tooling_absent_durability_warning" in r.warnings


def test_T46_perl_absent_is_hard_abort(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    broken = Tools(perl=None, splice_pl=str(mod._SPLICE_PL))
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=broken,
                        fixture_root=str(tmp_path))
    assert r.state is State.SAFE_ABORT
    assert r.reason == "perl_missing"
    assert _field2(p) == H_PRE


# --------------------------------------------------------------------------- #
# T47 line-count ridondante rispetto a diff-scope byte-level
# --------------------------------------------------------------------------- #
def test_T47_line_count_equal_but_diff_scope_catches(tmp_path, monkeypatch):
    p = _shadow(tmp_path, _std_lines())

    def same_linecount_tamper(tools, path, user, expected, new, minf, allow_empty):
        # correct $2 splice, SAME number of lines, but a sibling byte changed
        data = Path(path).read_bytes().replace(expected.encode(), new.encode())
        data = data.replace(b"sales:", b"saleX:", 1)  # out-of-field, line-count unchanged
        tmp = os.path.join(os.path.dirname(path), ".shadow.mig.LC")
        Path(tmp).write_bytes(data)
        return True, os.path.basename(tmp)

    monkeypatch.setattr(mod, "_run_worker", same_linecount_tamper)
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.SAFE_ABORT
    assert r.reason == "diff_scope"  # byte-level caught it even though line-count matched


# --------------------------------------------------------------------------- #
# T48 perl assente -> hard-abort (explicit, no divergent path)
# --------------------------------------------------------------------------- #
def test_T48_perl_missing_hard_abort_no_mutation(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS,
                        tools=Tools(perl="/nonexistent/perl", splice_pl=str(mod._SPLICE_PL)),
                        fixture_root=str(tmp_path))
    assert r.state is State.SAFE_ABORT
    assert r.reason == "perl_missing"
    assert _field2(p) == H_PRE


# --------------------------------------------------------------------------- #
# Security: hashes travel on stdin ONLY, never argv/env; audit is redacted
# --------------------------------------------------------------------------- #
def test_hashes_never_in_argv_only_stdin(tmp_path, monkeypatch):
    p = _shadow(tmp_path, _std_lines())
    seen = {}
    real_run = subprocess.run

    def spy(argv, *a, **kw):
        seen["argv"] = list(argv)
        seen["input"] = kw.get("input")
        return real_run(argv, *a, **kw)

    monkeypatch.setattr(mod.subprocess, "run", spy)
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.UPDATED
    joined_argv = " ".join(seen["argv"])
    assert H_PRE not in joined_argv and H_NEW not in joined_argv  # never in argv
    assert H_PRE.encode() in seen["input"] and H_NEW.encode() in seen["input"]  # on stdin


def test_audit_and_result_carry_no_secrets(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    records = []
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path), audit_sink=records.append)
    blob = json.dumps(r.audit) + json.dumps(records)
    assert H_PRE not in blob and H_NEW not in blob
    assert "$6$" not in blob  # no crypt-hash shape leaked
    assert r.audit["run_id"] == "run-abc" and r.audit["correlation_id"] == "corr-99"


# =========================================================================== #
# ADVERSARIAL-REVIEW FIXES F1-F9 (+ F8-minor worker)
# =========================================================================== #

# --------------------------------------------------------------------------- #
# F1/F2 worker temp is reclaimed on the temp_unreadable SAFE_ABORT branch, and
# the orphan sweep now also covers .shadow.mig.* worker temps.
# --------------------------------------------------------------------------- #
def test_F1_temp_unreadable_branch_leaves_no_worker_temp(tmp_path, monkeypatch):
    p = _shadow(tmp_path, _std_lines())
    d = os.path.dirname(p)

    def worker_makes_unreadable_temp(tools, path, user, expected, new, minf, allow_empty):
        # a valid-looking OK response, but the temp is deleted before the Python
        # side can read it -> forces the temp_unreadable SAFE_ABORT branch (F1).
        tmp = os.path.join(os.path.dirname(path), ".shadow.mig.GONE")
        return True, os.path.basename(tmp)  # never actually created -> unreadable

    monkeypatch.setattr(mod, "_run_worker", worker_makes_unreadable_temp)
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.SAFE_ABORT
    assert r.reason == "temp_unreadable"
    assert not any(f.startswith(".shadow.mig.") for f in os.listdir(d))  # no orphan
    assert _field2(p) == H_PRE  # live intact


def test_F2_orphan_sweep_covers_worker_mig_temp(tmp_path):
    d = str(tmp_path)
    (tmp_path / ".shadow.premig.r1").write_bytes(b"premig")
    # a worker temp orphaned by a crash between temp-create and rename: it would
    # otherwise sit on disk carrying the NEW field-2 value.
    (tmp_path / ".shadow.mig.ABC123").write_bytes(b"orphan-worker-temp")
    removed = mod.sweep_orphan_premigs(d)
    assert len(removed) == 2
    assert not any(f.startswith(".shadow.mig.") for f in os.listdir(d))
    assert not any(f.startswith(".shadow.premig.") for f in os.listdir(d))


def test_F2_sweep_keep_still_protects_named_premig(tmp_path):
    d = str(tmp_path)
    keep = tmp_path / ".shadow.premig.keepme"
    keep.write_bytes(b"k")
    (tmp_path / ".shadow.mig.other").write_bytes(b"x")
    removed = mod.sweep_orphan_premigs(d, keep=str(keep))
    assert str(keep) not in [os.path.abspath(x) for x in removed]
    assert keep.exists()  # backward-compat: keep is preserved
    assert not (tmp_path / ".shadow.mig.other").exists()  # mig orphan still swept


# --------------------------------------------------------------------------- #
# F3 an exception in the mutative section (e.g. worker timeout) does not leak a
# premig/temp and returns a safe state (SAFE_ABORT pre-rename).
# --------------------------------------------------------------------------- #
def test_F3_worker_exception_pre_mv_is_safe_and_clean(tmp_path, monkeypatch):
    p = _shadow(tmp_path, _std_lines())
    d = os.path.dirname(p)

    def timing_out_worker(*a, **kw):
        raise subprocess.TimeoutExpired(cmd="perl", timeout=30)

    monkeypatch.setattr(mod, "_run_worker", timing_out_worker)
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.SAFE_ABORT
    assert r.reason == "exception_pre_mv"
    assert _field2(p) == H_PRE  # live intact, never renamed
    # neither the premig nor a worker temp is left behind
    assert not any(f.startswith(".shadow.premig.") for f in os.listdir(d))
    assert not any(f.startswith(".shadow.mig.") for f in os.listdir(d))


# --------------------------------------------------------------------------- #
# F4 audit_sink failure on a PRE-mutation SAFE_ABORT path: no exception escapes,
# no orphan left, and the audit failure is NOT silent.
# --------------------------------------------------------------------------- #
def test_F4_pre_mutation_audit_failure_no_orphan_no_raise(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    d = os.path.dirname(p)

    def mutate(path):  # force a pre-mv SAFE_ABORT after premig + worker temp exist
        with open(path, "ab") as f:
            f.write(b"zz:x:1::::::\n")

    def boom(_audit):
        raise RuntimeError("audit sink down")

    # must return normally (no propagation) even though the sink raises
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path),
                        hooks=Hooks(before_fingerprint2=mutate), audit_sink=boom)
    assert r.state is State.SAFE_ABORT
    assert r.reason == "fingerprint_mismatch"     # live intact; real abort reason kept
    assert "audit_write_failed" in r.warnings      # audit failure surfaced, not silent
    assert _field2(p) == H_PRE                      # our rewrite never committed
    assert not any(f.startswith(".shadow.premig.") for f in os.listdir(d))
    assert not any(f.startswith(".shadow.mig.") for f in os.listdir(d))


# --------------------------------------------------------------------------- #
# F5 barrier-live: exercise the REAL perl worker's own guards directly. The
# Python pre-checks normally shadow these branches; here we bypass them.
# --------------------------------------------------------------------------- #
def _run_splice(shadow_path, username, min_fields, allow_empty, expected, new):
    proc = subprocess.run(
        [PERL, SPLICE, str(shadow_path), username, str(min_fields),
         "1" if allow_empty else "0"],
        input=(expected + "\n" + new + "\n").encode("latin-1"),
        capture_output=True, timeout=30,
    )
    return proc.returncode, proc.stdout.decode("latin-1").strip()


def _mkshadow(tmp_path, body, name="shadow"):
    p = tmp_path / name
    p.write_bytes(body.encode("latin-1"))
    return p


@requires_perl
def test_F5_worker_ok_direct_and_byte_exact_splice(tmp_path):
    body = f"demobox:{H_PRE}:19000:0:99999:7:::\ninfo:{fake_crypt('i')}:1::::::\n"
    p = _mkshadow(tmp_path, body)
    rc, out = _run_splice(p, "demobox", 2, False, H_PRE, H_NEW)
    assert rc == 0 and out.startswith("OK ")
    temp = tmp_path / out.split()[1]
    spliced = temp.read_bytes()
    assert spliced == body.replace(H_PRE, H_NEW).encode("latin-1")  # byte-exact $2 splice
    temp.unlink()


@requires_perl
def test_F5_worker_seen_zero_and_two(tmp_path):
    p0 = _mkshadow(tmp_path, "info:x:1::::::\n", name="s0")
    rc, out = _run_splice(p0, "demobox", 2, False, "x", "y")
    assert rc == 4 and out == "ERR seen 0"
    p2 = _mkshadow(tmp_path, "demobox:a:1::::::\ndemobox:b:1::::::\n", name="s2")
    rc, out = _run_splice(p2, "demobox", 2, False, "a", "y")
    assert rc == 4 and out == "ERR seen 2"


@requires_perl
def test_F5_worker_nf_too_few_fields(tmp_path):
    p = _mkshadow(tmp_path, "demobox:aaa\n")  # only 2 colon-fields
    rc, out = _run_splice(p, "demobox", 5, False, "aaa", "yyy")
    assert rc == 5 and out == "ERR nf 2"


@requires_perl
def test_F5_worker_cas_mismatch(tmp_path):
    p = _mkshadow(tmp_path, "demobox:aaa:1::::::\n")
    rc, out = _run_splice(p, "demobox", 2, False, "WRONG", "yyy")
    assert rc == 8 and out == "ERR cas"


@requires_perl
def test_F5_worker_reject_new_colon_and_cr(tmp_path):
    p = _mkshadow(tmp_path, "demobox:aaa:1::::::\n")
    rc, out = _run_splice(p, "demobox", 2, False, "aaa", "ne:w")
    assert rc == 7 and out == "ERR reject_new"          # ':' in new
    rc, out = _run_splice(p, "demobox", 2, False, "aaa", "ne\rw")
    assert rc == 7 and out == "ERR reject_new"          # F8-minor: '\r' in new


@requires_perl
def test_F5_worker_encoding_cr_and_nul_in_file(tmp_path):
    p = _mkshadow(tmp_path, "demobox:aaa:1::::::\r\n", name="cr")  # CR in FILE
    rc, out = _run_splice(p, "demobox", 2, False, "aaa", "yyy")
    assert rc == 6 and out == "ERR encoding"
    p2 = tmp_path / "nul"
    p2.write_bytes(b"demobox:aaa:1::::::\x00\n")                    # NUL in FILE
    rc, out = _run_splice(p2, "demobox", 2, False, "aaa", "yyy")
    assert rc == 6 and out == "ERR encoding"


@requires_perl
def test_F5_worker_empty_needs_confirm_then_allow(tmp_path):
    p = _mkshadow(tmp_path, "demobox::1::::::\n", name="e1")
    rc, out = _run_splice(p, "demobox", 2, False, "", "yyy")
    assert rc == 9 and out == "ERR empty_needs_confirm"
    # allow_empty=1 lets the same no-auth -> auth splice proceed
    p2 = _mkshadow(tmp_path, "demobox::1::::::\n", name="e2")
    rc, out = _run_splice(p2, "demobox", 2, True, "", "yyy")
    assert rc == 0 and out.startswith("OK ")
    (tmp_path / out.split()[1]).unlink()


# --------------------------------------------------------------------------- #
# F6 the negative control's cache-defeat (clock += ttl+1) is load-bearing: a
# stale positive in the auth cache must NOT mask the (now-dead) old password.
# --------------------------------------------------------------------------- #
def test_F6_negative_control_defeats_stale_auth_cache(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    oracle = mod.AuthOracle(p, "demobox", cache_ttl=100)
    holder = {"now": 0.0}
    # Pre-seed a STALE positive for the OLD password. Without the clock-advance
    # in step 3 the negative control would read this stale True and wrongly
    # ABORT_AUTHORITY; with the defeat it re-evaluates and proceeds.
    assert oracle.login("old-password", now=0.0) is True  # cached positive @ t=0
    res = mod.spike_discriminating_and_confirm(
        p, "demobox",
        p_pre="old-password", h_pre=H_PRE,
        p_tw="throwaway-xyz", h_tw=H_TW,
        p_src="source-password", h_new=H_NEW,
        oracle=oracle, ids=IDS, tools=_tools(), fixture_root=str(tmp_path),
        clock_holder=holder, cache_ttl=100,
    )
    assert res["result"] == "LOGIN_CONFIRMED"  # would be ABORT_AUTHORITY without the defeat
    assert _field2(p) == H_NEW


# --------------------------------------------------------------------------- #
# F7 discriminating double-failure: 2nd write fails AND recovery to H_pre fails
# -> ALERT_MANUAL.
# --------------------------------------------------------------------------- #
def test_F7_discriminating_double_failure_is_alert_manual(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    oracle = mod.AuthOracle(p, "demobox", cache_ttl=100)
    holder = {"now": 0.0}

    def corrupt(path):
        # 2nd write's pre-mv hook: shove field-2 off H_tw so BOTH the 2nd write
        # (fingerprint_mismatch) AND the H_tw->H_pre recovery (CAS mismatch) fail.
        data = Path(path).read_bytes().replace(H_TW.encode(), fake_crypt("corrupt-xyz").encode())
        Path(path).write_bytes(data)

    res = mod.spike_discriminating_and_confirm(
        p, "demobox",
        p_pre="old-password", h_pre=H_PRE,
        p_tw="throwaway-xyz", h_tw=H_TW,
        p_src="source-password", h_new=H_NEW,
        oracle=oracle, ids=IDS, tools=_tools(), fixture_root=str(tmp_path),
        clock_holder=holder, cache_ttl=100,
        second_write_hooks=Hooks(before_fingerprint2=corrupt),
    )
    assert res["result"] == "ALERT_MANUAL"
    assert res["stage"] == "write_new"


# --------------------------------------------------------------------------- #
# F8 end-to-end (real worker): special localpart, literal '$'/'/'/'.' splice,
# and a large file where only the target line changes.
# --------------------------------------------------------------------------- #
def test_F8_special_localpart_no_false_match(tmp_path):
    h_t = fake_crypt("t")
    h_s = fake_crypt("s")
    # "u.a+b" must be literal: it must NOT match sibling "uxaab" (which an
    # unescaped regex `u.a+b` would capture -> seen!=1 or a wrong write).
    p = _shadow(tmp_path, [
        f"u.a+b:{h_t}:19000:0:99999:7:::",
        f"uxaab:{h_s}:19000:0:99999:7:::",
    ])
    before = Path(p).read_bytes()
    r = guarded_rewrite(p, "u.a+b", h_t, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.UPDATED
    assert _field2(p, "u.a+b") == H_NEW
    assert _field2(p, "uxaab") == h_s  # sibling untouched
    assert Path(p).read_bytes() == before.replace(h_t.encode(), H_NEW.encode())


def test_F8_new_value_dollar_slash_dot_spliced_literally(tmp_path):
    special = "a$b/c.d"  # '$' must NOT be interpolated; '/' and '.' are literal
    p = _shadow(tmp_path, _std_lines())
    r = guarded_rewrite(p, "demobox", H_PRE, special, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.UPDATED
    assert _field2(p) == special


def test_F8_large_file_only_target_line_changes(tmp_path):
    lines = [f"demobox:{H_PRE}:19000:0:99999:7:::"]
    lines += [f"user{i}:{fake_crypt('pw' + str(i))}:19000:0:99999:7:::" for i in range(400)]
    p = _shadow(tmp_path, lines)
    before = Path(p).read_bytes()
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.UPDATED
    after = Path(p).read_bytes()
    assert after == before.replace(H_PRE.encode(), H_NEW.encode())  # only $2 changed
    assert after.count(b"\n") == before.count(b"\n")  # every sibling line intact


# --------------------------------------------------------------------------- #
# F9 OFFLINE confinement guard: refuse paths outside fixture_root, the system
# shadow, and symlink escapes — fail closed BEFORE any read/write.
# --------------------------------------------------------------------------- #
def test_F9_guard_rejects_path_outside_fixture_root(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    other_root = tmp_path / "elsewhere"
    other_root.mkdir()
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(other_root))
    assert r.state is State.SAFE_ABORT
    assert r.reason == "offline_guard"
    assert _field2(p) == H_PRE  # untouched


def test_F9_guard_rejects_system_shadow(tmp_path):
    # The guard fails closed before any I/O: /etc/shadow is NEVER opened here.
    # (dummy CAS/new tokens chosen so they aren't substrings of the audit JSON)
    r = guarded_rewrite("/etc/shadow", "root", "GUARD_EXP", "GUARD_NEW", ids=IDS,
                        tools=_tools(), fixture_root="/etc")
    assert r.state is State.SAFE_ABORT
    assert r.reason == "offline_guard"


def test_F9_guard_blocks_symlink_escape(tmp_path):
    # a symlink inside fixture_root that points OUT must not smuggle in a path
    # the guard would otherwise reject (realpath containment defeats it).
    outside = tmp_path / "outside"
    outside.mkdir()
    real = outside / "shadow"
    real.write_bytes(f"demobox:{H_PRE}:1::::::\n".encode("latin-1"))
    root = tmp_path / "fixtures"
    root.mkdir()
    link = root / "shadow"
    os.symlink(real, link)
    r = guarded_rewrite(str(link), "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(root))
    assert r.state is State.SAFE_ABORT
    assert r.reason == "offline_guard"
    assert real.read_bytes() == f"demobox:{H_PRE}:1::::::\n".encode("latin-1")  # untouched


def test_F9_guard_allows_path_inside_fixture_root(tmp_path):
    # positive control: a genuine fixture under fixture_root still works.
    p = _shadow(tmp_path, _std_lines())
    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.UPDATED
    assert _field2(p) == H_NEW


# =========================================================================== #
# COVERAGE COMPLETION: reachable guard branches, redaction tripwire, crypt-
# scheme classifier, discriminating tails, and the post-mv ALERT transitions.
# =========================================================================== #

def test_redaction_tripwire_fires_on_secret_and_hash_shape():
    # security tripwire: it must RAISE on a leaked secret value or a crypt shape
    with pytest.raises(RuntimeError):
        mod._assert_no_secrets('{"x": "topsecret"}', ["topsecret"])
    with pytest.raises(RuntimeError):
        mod._assert_no_secrets('{"leak": "$6$salt$abc"}', [])
    mod._assert_no_secrets('{"ok": "nothing-here"}', ["absent"])  # must NOT raise


def test_guard_reject_value_with_colon(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    r = guarded_rewrite(p, "demobox", H_PRE, "ab:cd", ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.SAFE_ABORT and r.reason == "reject_value"
    assert _field2(p) == H_PRE


def test_guard_python_encoding_cr_in_file(tmp_path):
    p = tmp_path / "shadow"
    p.write_bytes(f"demobox:{H_PRE}:1::::::\r\n".encode("latin-1"))  # CR in file
    r = guarded_rewrite(str(p), "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.SAFE_ABORT and r.reason == "encoding"


def test_guard_python_seen_duplicate(tmp_path):
    p = tmp_path / "shadow"
    p.write_bytes(f"demobox:{H_PRE}:1::::::\ndemobox:{H_PRE}:1::::::\n".encode("latin-1"))
    r = guarded_rewrite(str(p), "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.SAFE_ABORT and r.reason == "seen"


def test_guard_python_nf_too_few(tmp_path):
    p = tmp_path / "shadow"
    p.write_bytes(b"demobox:aaa\n")  # only 2 colon-fields
    r = guarded_rewrite(str(p), "demobox", "aaa", H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path), min_fields=5)
    assert r.state is State.SAFE_ABORT and r.reason == "nf"


def test_guard_empty_and_unreadable_file(tmp_path):
    empty = tmp_path / "empty"
    empty.write_bytes(b"")
    r = guarded_rewrite(str(empty), "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path))
    assert r.state is State.SAFE_ABORT and r.reason == "shadow_empty"
    missing = tmp_path / "nope"  # resolves inside fixture_root (parent exists) -> passes guard
    r2 = guarded_rewrite(str(missing), "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                         fixture_root=str(tmp_path))
    assert r2.state is State.SAFE_ABORT and r2.reason == "shadow_unreadable"


def test_classify_scheme_all_families():
    assert mod.classify_scheme("$6$s$h") == "SHA-512"
    assert mod.classify_scheme("$5$s$h") == "SHA-256"
    assert mod.classify_scheme("$1$s$h") == "MD5"
    assert mod.classify_scheme("$2b$10$x") == "bcrypt"
    assert mod.classify_scheme("$y$j9T$x") == "yescrypt"
    assert mod.classify_scheme("$argon2id$v=19$x") == "Argon2"
    assert mod.classify_scheme("") == "EMPTY"
    assert mod.classify_scheme("!x") == "LOCKED"
    assert mod.classify_scheme("weird") == "UNKNOWN"


def test_contained_handles_mixed_abs_relative():
    assert mod._contained("relative/path", "/abs/root") is False


def test_discriminating_abort_when_first_write_fails(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    oracle = mod.AuthOracle(p, "demobox", cache_ttl=100)
    holder = {"now": 0.0}
    res = mod.spike_discriminating_and_confirm(
        p, "demobox",
        p_pre="old-password", h_pre=fake_crypt("WRONG-pre"),  # wrong CAS -> first write aborts
        p_tw="throwaway-xyz", h_tw=H_TW,
        p_src="source-password", h_new=H_NEW,
        oracle=oracle, ids=IDS, tools=_tools(), fixture_root=str(tmp_path),
        clock_holder=holder, cache_ttl=100,
    )
    assert res["result"] == "ABORT" and res["stage"] == "write_tw"


def test_discriminating_abort_when_throwaway_login_fails(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    oracle = mod.AuthOracle(p, "demobox", cache_ttl=100)
    holder = {"now": 0.0}
    res = mod.spike_discriminating_and_confirm(
        p, "demobox",
        p_pre="old-password", h_pre=H_PRE,
        p_tw="MISMATCH", h_tw=H_TW,  # p_tw doesn't match h_tw -> login_tw fails
        p_src="source-password", h_new=H_NEW,
        oracle=oracle, ids=IDS, tools=_tools(), fixture_root=str(tmp_path),
        clock_holder=holder, cache_ttl=100,
    )
    assert res["result"] == "ABORT" and res["stage"] == "login_tw"


def test_discriminating_login_failed_after_ttl(tmp_path):
    p = _shadow(tmp_path, _std_lines())
    oracle = mod.AuthOracle(p, "demobox", cache_ttl=100)
    holder = {"now": 0.0}
    res = mod.spike_discriminating_and_confirm(
        p, "demobox",
        p_pre="old-password", h_pre=H_PRE,
        p_tw="throwaway-xyz", h_tw=H_TW,
        p_src="WRONG-src", h_new=H_NEW,  # preserved plaintext doesn't match h_new
        oracle=oracle, ids=IDS, tools=_tools(), fixture_root=str(tmp_path),
        clock_holder=holder, cache_ttl=100,
    )
    assert res["result"] == "LOGIN_FAILED_AFTER_TTL"
    assert _field2(p) == H_NEW  # bytes still landed


def test_readback_mismatch_after_mv_is_alert_manual(tmp_path):
    p = _shadow(tmp_path, _std_lines())

    def corrupt_after_mv(path):
        data = Path(path).read_bytes().replace(H_NEW.encode(), fake_crypt("intruder").encode())
        Path(path).write_bytes(data)

    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path), hooks=Hooks(after_mv=corrupt_after_mv))
    assert r.state is State.ALERT_MANUAL and r.reason == "readback_mismatch"
    assert r.premig_path is not None and os.path.exists(r.premig_path)  # recoverable
    os.unlink(r.premig_path)


def test_exception_after_mv_is_alert_manual(tmp_path):
    p = _shadow(tmp_path, _std_lines())

    def raise_after_mv(_path):
        raise RuntimeError("post-rename fault")

    r = guarded_rewrite(p, "demobox", H_PRE, H_NEW, ids=IDS, tools=_tools(),
                        fixture_root=str(tmp_path), hooks=Hooks(after_mv=raise_after_mv))
    assert r.state is State.ALERT_MANUAL and r.reason == "exception_after_mv"
    assert r.premig_path is not None and os.path.exists(r.premig_path)  # recoverable
    os.unlink(r.premig_path)
