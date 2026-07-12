"""Additive-only email autoresponder writer engine (task B4e-ii).

Consumes only the B4e-i contract/fingerprint/rules and reuses the B4a
``execute_email_phase`` engine (no ``email_write.py`` change). The mock orchestration in
``autoresponder_writer.py`` stays intact; this is the *real* additive engine, still
unreachable from the runtime until B4e-iii wires it under
``AUTORESPONDER_WRITER_MODE=enabled`` + ``REAL_EXECUTION_MODE``.

``Email::add_auto_responder`` is an **UPSERT**, so a write is reached only on a live-absent
address — proven by *two distinct* fresh reads: the initial ``read_live`` behind the
decision, and a second ``list_auto_responders`` guard executed **inside** the write gateway
immediately adjacent to ``add_auto_responder``. A same-address different responder is blocked
(never overwritten); nothing is ever deleted.

**Source payload provenance.** The complete operational payload
(``from``/``subject``/``body``/``interval``/``is_html``/``charset``/``start``/``stop``) is
resolved *only* from the immutable source snapshot
(``source_snapshot.data["email_autoresponders"]``) and is bound to the B4e-i contract: the
fingerprint rebuilt from the snapshot entry must equal the fingerprint the contract recorded
for that address, and the address' domain and local part must coincide. A missing / duplicate
/ detail-failed / fingerprint-mismatched entry yields ``manual``/``blocked`` with zero write.
The complete payload is kept only in the in-memory ``EmailItem.payload`` — never persisted to
an event, planned call, audit, error, or compensation record; only the opaque fingerprint and
the non-sensitive B4e-i metadata surface.

Framework order (unchanged ``email_write.py``): ``read_live`` → ``decide`` → ``before_write``
(the B4e-iii authorize/fencing seam) → ``gateway.create`` → verify. The UPSERT guard is the
first thing ``create`` does, so the effective order is exactly fresh-read → decide →
before_write → fresh-list guard → immediate ``add_auto_responder``; after the guard no other
fallible logic runs before the API call. The non-idempotent create is never auto-retried; an
ambiguous/timeout outcome is resolved by a fresh read, never a second write. Post-write
verification is by the *complete* fingerprint over a fresh list+detail read — a
template/mismatch detail never yields success. The redacted compensation
(``manual_remove_created_autoresponder``, domain+address+fingerprint) is attached **only** for
a create the gateway actually wrote and the live re-read verified — never for
``already_present`` and never for a guard-skipped write, so it can never remove a pre-existing
responder. No ``delete`` primitive exists. Effectful solely through an injected
destination-only client (tests use a fake).
"""

from __future__ import annotations

from app.modules.executions import autoresponder_rules as rules
from app.modules.executions.autoresponder_rules import (
    AutoresponderDecision,
    AutoresponderEvidence,
    fingerprint,
)
from app.modules.executions.email_write import (
    EmailItem,
    EmailPhaseResult,
    ItemDecision,
    WriteAction,
    execute_email_phase,
)

# B4e-i decisions → framework write actions (``create`` reaches the gated write path).
_ACTION_MAP = {
    AutoresponderDecision.create: WriteAction.create,
    AutoresponderDecision.already_present: WriteAction.already_present,
    AutoresponderDecision.blocked: WriteAction.blocked,
    AutoresponderDecision.manual: WriteAction.manual,
}

# Source-resolution outcome kept in ``payload["source_status"]``. Only ``ST_VERIFIED`` reaches
# the write; every other value is a non-verified source status the B4e-i ``decide`` treats as
# ``manual`` (never a write). ``_SRC_CONFLICT`` covers a snapshot/contract binding failure
# (fingerprint / domain / local-part mismatch, or no single authoritative contract record).
_SRC_CONFLICT = "conflict"


def _norm_address(value: object) -> str | None:
    """Stripped address, matching the B4e-i contract's normalization (no case folding, so a
    rebuilt fingerprint compares byte-for-byte against the recorded one)."""
    if isinstance(value, str) and value.strip():
        return value.strip()
    return None


def _entry_address(entry: object) -> str | None:
    if isinstance(entry, dict):
        return _norm_address(entry.get("email"))
    return None


def _split_address(address: str) -> tuple[str | None, str | None]:
    parts = address.split("@")
    if len(parts) == 2 and parts[0] and parts[1]:
        return parts[0], parts[1]
    return None, None


def address_absent(payload: object, address: str) -> bool | None:
    """Absence check over a live ``list_auto_responders`` payload, by enumeration only (never
    a ``get_auto_responder`` probe). ``True`` = provably absent; ``False`` = present; ``None``
    = not provable (unreadable/malformed) → the guard must not write."""
    if not isinstance(payload, list):
        return None
    absent = True
    for entry in payload:
        addr = _entry_address(entry)
        if addr is None:
            return None  # malformed → the list cannot prove absence
        if addr == address:
            absent = False
    return absent


# -- source payload resolution (immutable snapshot ↔ contract fingerprint) -----


def _snapshot_entries(snapshot_data: object, address: str) -> list[dict]:
    """All flat ``email_autoresponders`` entries whose address matches — from the immutable
    source snapshot only. Never the request body, preview, events, or destination."""
    responders = snapshot_data.get("email_autoresponders") if isinstance(snapshot_data, dict) else None
    if not isinstance(responders, list):
        return []
    return [e for e in responders if isinstance(e, dict) and _entry_address(e) == address]


def _contract_record(contract: object, address: str) -> dict | None:
    """The single authoritative contract record for the address; zero or duplicate → ``None``
    (cannot bind a payload to an ambiguous contract)."""
    if not isinstance(contract, dict):
        return None
    matches: list[dict] = []
    for domain in contract.get("domains") or []:
        if not isinstance(domain, dict):
            continue
        for record in domain.get("records") or []:
            if isinstance(record, dict) and _norm_address(record.get("address")) == address:
                matches.append(record)
    return matches[0] if len(matches) == 1 else None


def _resolve_source(snapshot_data: object, contract: object, address: str) -> tuple[str, str | None, dict | None]:
    """Bind the immutable-snapshot payload to the contract. Returns
    ``(source_status, source_fingerprint, fields)``; ``fields`` (the complete in-memory
    payload) is populated only on ``ST_VERIFIED``. Never invents a default."""
    entries = _snapshot_entries(snapshot_data, address)
    if len(entries) != 1:
        return rules.ST_MISSING, None, None  # absent or duplicate in the snapshot
    entry = entries[0]
    if entry.get("_detail_status") != "succeeded":
        return rules.ST_MISSING, None, None
    record = _contract_record(contract, address)
    if record is None:
        return _SRC_CONFLICT, None, None
    completeness = record.get("completeness")
    if completeness == rules.INCOMPLETE:
        return rules.ST_INCOMPLETE, None, None
    if completeness == rules.UNSUPPORTED:
        return rules.ST_UNSUPPORTED, None, None
    if completeness != rules.COMPLETE or record.get("issue") is not None:
        return _SRC_CONFLICT, None, None
    # The fingerprint rebuilt from the snapshot must equal the one the contract recorded, and
    # the address' domain + local part must coincide with the contract's domain.
    local, domain_of_address = _split_address(address)
    if local is None or fingerprint(address, entry) != record.get("fingerprint"):
        return _SRC_CONFLICT, None, None
    if domain_of_address != record.get("domain") or entry.get("_domain") != record.get("domain"):
        return _SRC_CONFLICT, None, None
    # Re-validate completeness on the live snapshot payload (never trust the status string).
    live_completeness = rules.classify_completeness(entry)
    if live_completeness == rules.INCOMPLETE:
        return rules.ST_INCOMPLETE, None, None
    if live_completeness == rules.UNSUPPORTED:
        return rules.ST_UNSUPPORTED, None, None
    fields = {key: entry[key] for key in rules.KNOWN_FIELDS if key in entry}
    return rules.ST_VERIFIED, record.get("fingerprint"), fields


def resolve_autoresponder_items(snapshot_data: object, contract: object, specs: list[dict]) -> list[EmailItem]:
    """Resolve typed autoresponder specs into items. Each spec carries a target ``address``
    (from the plan/step), an optional ``step_id`` and ``domain_present`` (whether the
    destination domain exists). The complete payload is fetched from the immutable snapshot
    and bound to the contract fingerprint; it lives only in the returned item."""
    items: list[EmailItem] = []
    for spec in specs:
        address = _norm_address(spec.get("address"))
        step_id = spec.get("step_id") or f"email_autoresponders:{address or 'invalid'}"
        if address is None:
            status, fp, fields, local, domain = rules.ST_MISSING, None, None, None, None
        else:
            status, fp, fields = _resolve_source(snapshot_data, contract, address)
            local, domain = _split_address(address)
        items.append(EmailItem(
            step_id=step_id,
            label=address or "invalid",
            payload={
                "address": address,
                "local": local,
                "domain": domain,
                "fields": fields or {},          # complete payload, in-memory only
                "source_status": status,
                "source_fingerprint": fp,
                "domain_present": bool(spec.get("domain_present", True)),
            }))
    return items


# -- category hooks (pure, secret-free) ---------------------------------------


def _source_evidence(item: EmailItem) -> AutoresponderEvidence:
    status = item.payload["source_status"]
    if status != rules.ST_VERIFIED:
        return AutoresponderEvidence(status)
    return AutoresponderEvidence(
        rules.ST_VERIFIED, fingerprint=item.payload["source_fingerprint"], completeness=rules.COMPLETE)


def _destination_evidence(live: object, item: EmailItem) -> AutoresponderEvidence:
    """This domain's live evidence for the target address, from ``read_live`` (list + detail).
    A missing destination domain is ``domain_missing``; an unreadable read is ``unreadable``; a
    template/address-mismatch detail is ``ambiguous`` (never a valid responder); otherwise the
    address' complete fingerprint is compared by the caller."""
    if not item.payload.get("domain_present", True):
        return AutoresponderEvidence(rules.ST_DOMAIN_MISSING)
    if not isinstance(live, list):
        return AutoresponderEvidence(rules.ST_UNREADABLE)
    address = item.payload["address"]
    matches = [e for e in live if isinstance(e, dict) and _norm_address(e.get("email")) == address]
    if not matches:
        return AutoresponderEvidence(rules.ST_ABSENT)
    detail = matches[0].get("detail")
    if not isinstance(detail, dict):
        return AutoresponderEvidence(rules.ST_AMBIGUOUS)
    detail_addr = _norm_address(detail.get("email"))
    if detail_addr is not None and detail_addr != address:
        return AutoresponderEvidence(rules.ST_AMBIGUOUS)  # template-on-absent / mismatch
    return AutoresponderEvidence(rules.ST_VERIFIED, fingerprint=fingerprint(address, detail))


def decide_autoresponder_live(item: EmailItem, live: object) -> ItemDecision:
    """Framework decider: build both sides' evidence from the live read and decide via the
    B4e-i additive-only rules."""
    decision = rules.decide(_source_evidence(item), _destination_evidence(live, item))
    return ItemDecision(_ACTION_MAP[decision.action], decision.reason)


def plan_autoresponder_call(item: EmailItem) -> dict:
    # Redacted planned call: domain + address + fingerprint + non-sensitive B4e-i metadata
    # only — never ``from``/``subject``/``body``.
    p = item.payload
    fields = p["fields"]
    return {"api": "uapi", "module": "Email", "function": "add_auto_responder",
            "arguments": {"domain": p["domain"], "email": p["address"],
                          "payload_fingerprint": p["source_fingerprint"],
                          **{k: fields[k] for k in rules.METADATA_FIELDS if k in fields}}}


def _compensation_of(gateway: "AutoresponderGateway"):
    """Redacted compensation attached ONLY for a create this gateway actually wrote; a
    guard-skipped or already-present step yields ``None`` (never appended), so a pre-existing
    responder can never be targeted for removal."""
    def compensation(item: EmailItem) -> dict | None:
        if item.step_id not in gateway.stored:
            return None
        p = item.payload
        return {"action": "add_auto_responder", "domain": p["domain"], "address": p["address"],
                "fingerprint": p["source_fingerprint"],
                "reverse": "manual_remove_created_autoresponder", "requires_confirmation": True}
    return compensation


class AutoresponderGateway:
    """Destination-only, domain-bound gateway. ``read_live`` enumerates via
    ``list_auto_responders`` then details each enumerated address via ``get_auto_responder``
    (existence-gated). ``create`` runs the UPSERT guard — a SECOND fresh
    ``list_auto_responders`` in the SAME domain, absence by enumeration only — immediately
    before the single ``add_auto_responder``. No source primitive; the non-idempotent add is
    never auto-retried."""

    def __init__(self, destination_client, domain: str) -> None:
        self._client = destination_client
        self._domain = domain
        self.stored: set[str] = set()    # step_ids for which add_auto_responder was attempted

    def read_live(self) -> list | None:
        listed = self._client.read(rules.list_auto_responders_op(self._domain)).data
        if not isinstance(listed, list):
            return None
        out: list[dict] = []
        seen: set[str] = set()
        for entry in listed:
            address = _entry_address(entry)
            if address is None or address in seen:
                continue
            seen.add(address)
            detail = self._client.read(rules.get_auto_responder_op(address)).data
            out.append({"email": address, "detail": detail})
        return out

    def create(self, item: EmailItem) -> None:
        address = item.payload["address"]
        # UPSERT guard: a fresh list in THIS domain, distinct from read_live's list. Absence is
        # proven by enumeration only — never by get_auto_responder. Any non-absent / non-provable
        # result aborts the write.
        guard = self._client.read(rules.list_auto_responders_op(self._domain)).data
        if address_absent(guard, address) is not True:
            return
        # Provably absent: record the attempt BEFORE the write (an ambiguous timeout may still
        # apply) and issue the single, immediately-adjacent add_auto_responder.
        self.stored.add(item.step_id)
        self._client.write(rules.add_auto_responder_op(
            item.payload["domain"], item.payload["local"], item.payload["fields"]))


def run_autoresponder_phase(run, snapshot_data: object, contract: object,
                            specs: list[dict], gateway: AutoresponderGateway, *,
                            before_write=None) -> EmailPhaseResult:
    """Run the additive-only autoresponder phase over the given typed specs (one domain's
    gateway). No ``backup_of`` seam: an additive create has no previous value to back up; the
    redacted compensation records only the just-created responder and is attached solely when
    the gateway wrote and the live re-read verified it."""
    items = resolve_autoresponder_items(snapshot_data, contract, specs)
    return execute_email_phase(
        run, items, gateway, phase="autoresponder_write",
        decide=decide_autoresponder_live, plan_call=plan_autoresponder_call,
        compensation_of=_compensation_of(gateway), before_write=before_write)


__all__ = [
    "address_absent",
    "resolve_autoresponder_items",
    "decide_autoresponder_live",
    "plan_autoresponder_call",
    "AutoresponderGateway",
    "run_autoresponder_phase",
]
