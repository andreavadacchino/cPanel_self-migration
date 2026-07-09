#!/usr/bin/env python3
"""OFFLINE pure-function parser for mail auth-mechanism advertisements.

This module is **pure and offline**: it takes RAW fixture/response text and
returns a structured verdict. It NEVER opens a socket, never connects to
IMAP/POP3/SMTP/ManageSieve, never touches SSH, cPanel/UAPI, or a shadow file.
It is the offline building block for the (future) live digest pre-check that a
shadow-rewrite runner would run BEFORE any mutation — but the parsing itself is
network-free and side-effect-free.

Purpose: the crypt-only shadow-rewrite path (DESIGN FIX v4) preserves passwords
ONLY for plaintext SASL (PLAIN / LOGIN over TLS), which cover IMAP/POP3/SMTP and
webmail-via-IMAP. Challenge-response mechanisms (CRAM-MD5, DIGEST-MD5, APOP,
SCRAM-*, NTLM) verify against a scheme-specific secret derived from the *plain*
password, NOT the crypt hash — so a crypt-only rewrite would leave them stale and
break any client configured for them. If a service advertises any of those, the
pre-check must FAIL closed (out of scope) unless the risk is explicitly accepted.

The result never echoes raw text (an APOP greeting carries a ``<pid.clock@host>``
token whose hostname must not leak): only mechanism NAMES, booleans and static
structural notes are emitted.
"""

from __future__ import annotations

import re
from dataclasses import asdict, dataclass, field
from typing import Any, Callable, Iterable

# Decision constants
PASS_PASSWORD_AUTH_ONLY = "PASS_PASSWORD_AUTH_ONLY"
FAIL_DIGEST_OR_CHALLENGE_RESPONSE_OFFERED = "FAIL_DIGEST_OR_CHALLENGE_RESPONSE_OFFERED"
INCONCLUSIVE = "INCONCLUSIVE"

# Mechanism classification
_RISKY_EXACT = frozenset({"CRAM-MD5", "DIGEST-MD5", "APOP", "NTLM"})
_RISKY_PREFIXES = ("SCRAM-",)          # SCRAM-SHA-1, SCRAM-SHA-256, SCRAM-SHA-256-PLUS, ...
_SAFE_PASSWORD = frozenset({"PLAIN", "LOGIN"})

SERVICES = ("imap", "pop3", "smtp", "managesieve")


@dataclass
class DigestProbeResult:
    service: str
    mechanisms_offered: list[str]
    risky_mechanisms: list[str]
    safe_password_mechanisms: list[str]
    digest_out_of_scope_required: bool
    decision: str
    notes: list[str] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


def _is_risky(mech: str) -> bool:
    return mech in _RISKY_EXACT or any(mech.startswith(p) for p in _RISKY_PREFIXES)


# --------------------------------------------------------------------------- #
# Per-service mechanism extraction. Each returns an ordered list of UPPERCASE
# mechanism names. Tolerant to case, extra spaces, multiline, reply prefixes.
# --------------------------------------------------------------------------- #
def _mechs_imap(text: str) -> list[str]:
    # IMAP CAPABILITY advertises SASL mechs as AUTH=<MECH> tokens.
    return [m.upper() for m in re.findall(r"(?i)\bAUTH=([A-Za-z0-9][A-Za-z0-9._-]*)", text)]


def _mechs_smtp(text: str) -> list[str]:
    # SMTP EHLO: lines like "250-AUTH PLAIN LOGIN CRAM-MD5" or legacy
    # "250-AUTH=PLAIN LOGIN ...". Mechanisms are whitespace-separated.
    out: list[str] = []
    for line in text.splitlines():
        s = re.sub(r"(?i)^\s*250[- ]\s*", "", line.strip())
        m = re.match(r"(?i)^AUTH[= ]\s*(.+)$", s)
        if m:
            out.extend(tok.upper() for tok in m.group(1).split())
    return out


def _mechs_pop3(text: str) -> list[str]:
    # POP3 CAPA: a "SASL PLAIN LOGIN CRAM-MD5" line. APOP is advertised via a
    # challenge token "<pid.clock@host>" in the "+OK ..." greeting.
    out: list[str] = []
    for line in text.splitlines():
        m = re.match(r"(?i)^\s*SASL\s+(.+)$", line.strip())
        if m:
            out.extend(tok.upper() for tok in m.group(1).split())
    # APOP capability: a msg-id-style challenge in the greeting (host must NOT leak).
    if re.search(r"(?im)^\s*\+OK\b.*<[^<>@\s]+@[^<>@\s]+>", text):
        out.append("APOP")
    return out


def _mechs_managesieve(text: str) -> list[str]:
    # ManageSieve: `"SASL" "PLAIN LOGIN CRAM-MD5"` (quoted, possibly multiline).
    out: list[str] = []
    for group in re.findall(r'(?i)"SASL"\s+"([^"]*)"', text):
        out.extend(tok.upper() for tok in group.split())
    return out


_EXTRACTORS: dict[str, Callable[[str], list[str]]] = {
    "imap": _mechs_imap,
    "pop3": _mechs_pop3,
    "smtp": _mechs_smtp,
    "managesieve": _mechs_managesieve,
}


def _dedupe(items: Iterable[str]) -> list[str]:
    seen: list[str] = []
    for it in items:
        if it not in seen:
            seen.append(it)
    return seen


def parse_capability(service: str, text: str) -> DigestProbeResult:
    """Parse one service's raw CAPABILITY/greeting text into a verdict.

    Pure and offline. ``decision`` is:
      * FAIL_DIGEST_OR_CHALLENGE_RESPONSE_OFFERED — a challenge-response mech
        (CRAM-MD5/DIGEST-MD5/APOP/SCRAM-*/NTLM) is offered;
      * PASS_PASSWORD_AUTH_ONLY — a plaintext mech (PLAIN/LOGIN) is offered and
        no risky mech is;
      * INCONCLUSIVE — nothing recognizable (empty/malformed, or only unrelated
        token/cert mechanisms with no PLAIN/LOGIN).
    """
    svc = (service or "").strip().lower()
    extractor = _EXTRACTORS.get(svc)
    if extractor is None:
        return DigestProbeResult(svc or "unknown", [], [], [], False, INCONCLUSIVE,
                                 ["unknown service"])

    mechs = _dedupe(extractor(text or ""))
    risky = [m for m in mechs if _is_risky(m)]
    safe = [m for m in mechs if m in _SAFE_PASSWORD]

    if risky:
        decision, digest_required = FAIL_DIGEST_OR_CHALLENGE_RESPONSE_OFFERED, True
        notes = ["challenge-response mechanism(s) offered; crypt-only rewrite is "
                 "out of scope for these"]
    elif safe:
        decision, digest_required = PASS_PASSWORD_AUTH_ONLY, False
        notes = ["only plaintext password mechanisms (PLAIN/LOGIN) offered"]
    else:
        decision, digest_required = INCONCLUSIVE, False
        notes = ["no recognized SASL/AUTH mechanisms parsed"]

    return DigestProbeResult(svc, mechs, risky, safe, digest_required, decision, notes)


def aggregate(results: Iterable[DigestProbeResult]) -> dict[str, Any]:
    """Combine several per-service results into one overall verdict for the
    (future) live pre-check. FAIL if any service FAILs; PASS only if at least one
    service PASSes and none FAILs; INCONCLUSIVE otherwise. Never leaks raw text."""
    results = list(results)
    decisions = [r.decision for r in results]
    if FAIL_DIGEST_OR_CHALLENGE_RESPONSE_OFFERED in decisions:
        overall = FAIL_DIGEST_OR_CHALLENGE_RESPONSE_OFFERED
    elif PASS_PASSWORD_AUTH_ONLY in decisions:
        overall = PASS_PASSWORD_AUTH_ONLY
    else:
        overall = INCONCLUSIVE
    risky_by_service = {
        r.service: r.risky_mechanisms for r in results if r.risky_mechanisms
    }
    return {
        "overall_decision": overall,
        "digest_out_of_scope_required": overall == FAIL_DIGEST_OR_CHALLENGE_RESPONSE_OFFERED,
        "per_service": [r.to_dict() for r in results],
        "risky_by_service": risky_by_service,
    }
