"""Pure decision rules for the additive email forwarder writer (task B4a).

A forwarder is identified by the composite key ``source→destination``.
``Email::add_forwarder`` is additive and deduped, so the only automatic path is
creating an exact pair that is missing on the destination; an existing pair is a
verified no-op and is never modified or replaced. Anything that cannot be
expressed as a plain additive forward, or any unreadable/ambiguous live evidence,
fails closed (blocked/manual) rather than writing.

Pure: no I/O, no secrets. The live evidence is the destination's own
``Email::list_forwarders`` list (fresh-read by the caller).
"""

from __future__ import annotations

from adapters.cpanel.contract import DestinationWrite, SafeRead, destination_write, safe_read
from app.modules.executions.email_write import EmailItem, ItemDecision, WriteAction


def _is_valid_source(source: str) -> bool:
    return source.count("@") == 1 and all(source.split("@"))


def _is_plain_forward(destination: str) -> bool:
    """``add_forwarder`` only expresses a plain email/local-address forward.

    A pipe (``|prog``), a file/path (``/dir``), a system target (``:fail:``,
    ``:blackhole:``), or a quoted target is a different, non-additive form and is
    not expressible here — it must be handled manually, never blindly created.
    """
    d = destination.strip().lower()
    if not d:
        return False
    return d[0] not in "|/:\""


def parse_live_pairs(live: list) -> tuple[set[tuple[str, str]], bool]:
    """Return ``(pairs, had_malformed)`` from a destination forwarder list.

    A malformed entry sets the flag so the caller can fail closed instead of
    mistaking an unparseable list for "pair absent".
    """
    pairs: set[tuple[str, str]] = set()
    malformed = False
    for entry in live:
        if not isinstance(entry, dict):
            malformed = True
            continue
        source = str(entry.get("dest") or entry.get("source") or "").strip().lower()
        destination = str(entry.get("forward") or entry.get("destination") or "").strip().lower()
        if not _is_valid_source(source) or not destination:
            malformed = True
            continue
        pairs.add((source, destination))
    return pairs, malformed


def decide_forwarder(item: EmailItem, live: list | None) -> ItemDecision:
    """Decide one forwarder item against the live destination evidence."""
    source = str(item.payload.get("source", "")).strip().lower()
    destination = str(item.payload.get("destination", "")).strip().lower()
    if not _is_valid_source(source):
        return ItemDecision(WriteAction.blocked, "forwarder_source_invalid")
    if live is None:
        return ItemDecision(WriteAction.manual, "forwarder_evidence_unreadable")
    pairs, malformed = parse_live_pairs(live)
    if (source, destination) in pairs:
        return ItemDecision(WriteAction.already_present)
    if not _is_plain_forward(destination):
        # Present would have matched above; an unexpressible target is blocked.
        return ItemDecision(WriteAction.blocked, "forward_not_plain_expressible")
    if malformed:
        # Ambiguous live evidence: cannot prove the pair is truly absent.
        return ItemDecision(WriteAction.manual, "forwarder_evidence_ambiguous")
    return ItemDecision(WriteAction.create)


CONTRACT_VERSION = 1
SUCCEEDED = "succeeded"
FAILED = "failed"
UNAVAILABLE = "unavailable"
FRESH_READ_STRATEGY = "list_forwarders_exact_pair"


def is_write_eligible(envelope: object) -> bool:
    if not isinstance(envelope, dict) or envelope.get("version") != CONTRACT_VERSION:
        return False
    if envelope.get("status") != SUCCEEDED:
        return False
    mappings = envelope.get("mappings")
    if not isinstance(mappings, list):
        return False
    invalid = envelope.get("invalid_sources")
    if not isinstance(invalid, list) or invalid:
        return False
    seen: set[str] = set()
    for m in mappings:
        if not isinstance(m, dict):
            return False
        src = m.get("source")
        dst = m.get("destination")
        if not isinstance(src, str) or not _is_valid_source(src):
            return False
        if not isinstance(dst, str) or not _is_plain_forward(dst):
            return False
        key = f"{src}\0{dst}"
        if key in seen:
            return False
        seen.add(key)
    if envelope.get("fresh_read_strategy") != FRESH_READ_STRATEGY:
        return False
    return True


def list_forwarders_op() -> SafeRead:
    return safe_read("Email", "list_forwarders")


def add_forwarder_op(source: str, destination: str) -> DestinationWrite:
    domain = source.split("@", 1)[1] if "@" in source else ""
    email = source.split("@", 1)[0] if "@" in source else source
    return destination_write("Email", "add_forwarder",
                             {"domain": domain, "email": email, "fwdopt": "fwd",
                              "fwddomain": destination},
                             idempotent=False)


__all__ = ["decide_forwarder", "parse_live_pairs", "CONTRACT_VERSION", "is_write_eligible",
           "list_forwarders_op", "add_forwarder_op"]
