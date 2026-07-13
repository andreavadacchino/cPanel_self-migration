"""Typed email category registry and evidence-bound source payload resolvers (B4e-iii-c-i).

Maps the five email category IDs to their engine metadata and provides
evidence-bound resolvers that extract the authoritative source payload from the
immutable snapshot and its contract — never from step IDs, preview, events, or
requests. Step IDs are selectors only; a step whose pair/record is not uniquely
present in the snapshot is blocked. No gateway, no dispatch wiring, no backup
binding, no cPanel call. Unreachable from the runtime until c-ii/c-iii wire it.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from app.modules.executions import autoresponder_rules
from app.modules.executions import default_address_rules
from app.modules.executions import filter_rules
from app.modules.executions import forwarder_rules
from app.modules.executions import routing_rules

EMAIL_CATEGORIES = frozenset({
    "email_forwarders", "default_address", "email_routing",
    "email_filters", "email_autoresponders",
})


@dataclass(frozen=True)
class CategoryEntry:
    category: str
    flag_property: str
    needs_backup: bool
    scope_strategy: str

REGISTRY: dict[str, CategoryEntry] = {
    "email_forwarders": CategoryEntry("email_forwarders", "forwarder_real_writer_enabled", False, "account"),
    "default_address": CategoryEntry("default_address", "default_address_real_writer_enabled", True, "account"),
    "email_routing": CategoryEntry("email_routing", "routing_real_writer_enabled", True, "account"),
    "email_filters": CategoryEntry("email_filters", "filter_real_writer_enabled", False, "per_scope"),
    "email_autoresponders": CategoryEntry("email_autoresponders", "autoresponder_real_writer_enabled", False, "per_domain"),
}


@dataclass
class ResolvedEvidence:
    category: str
    resolved: bool
    reason: str | None = None
    kwargs: dict = field(default_factory=dict)
    blocked: list[dict] = field(default_factory=list)


def _forwarder_flat_pairs(data: dict) -> dict[str, list[dict]]:
    raw = data.get("email_forwarders")
    if isinstance(raw, dict):
        raw = raw.get("forwarders", raw.get("data", []))
    by_key: dict[str, list[dict]] = {}
    for item in (raw if isinstance(raw, list) else []):
        if not isinstance(item, dict):
            continue
        source = str(item.get("dest") or item.get("source") or "").strip().lower()
        dest = str(item.get("forward") or item.get("destination") or "").strip().lower()
        if source and dest:
            by_key.setdefault(f"{source} -> {dest}", []).append({"source": source, "destination": dest})
    return by_key


def _reconcile_endpoint(data: dict, side: str) -> tuple[bool, str | None]:
    contract = data.get("forwarder_contract")
    if not forwarder_rules.is_write_eligible(contract):
        return False, f"forwarder_contract_{side}_not_eligible"
    raw = data.get("email_forwarders")
    if isinstance(raw, dict):
        raw = raw.get("forwarders", raw.get("data", []))
    if raw is None:
        raw = []
    elif not isinstance(raw, list):
        return False, f"forwarder_contract_{side}_flat_malformed"
    flat_keys: set[str] = set()
    for item in raw:
        if not isinstance(item, dict):
            return False, f"forwarder_contract_{side}_flat_item_not_dict"
        s_raw = item.get("dest") or item.get("source")
        d_raw = item.get("forward") or item.get("destination")
        if s_raw is not None and not isinstance(s_raw, str):
            return False, f"forwarder_contract_{side}_flat_source_invalid"
        if d_raw is not None and not isinstance(d_raw, str):
            return False, f"forwarder_contract_{side}_flat_destination_not_string"
        s = str(s_raw or "").strip().lower()
        d = str(d_raw or "").strip().lower()
        if not s or not d:
            return False, f"forwarder_contract_{side}_flat_item_incomplete"
        if not forwarder_rules._is_valid_source(s):
            return False, f"forwarder_contract_{side}_flat_source_invalid"
        key = f"{s} -> {d}"
        if key in flat_keys:
            return False, f"forwarder_contract_{side}_flat_duplicate"
        flat_keys.add(key)
    contract_mappings = contract.get("mappings", []) if isinstance(contract, dict) else []
    contract_keys: set[str] = set()
    for m in contract_mappings:
        if isinstance(m, dict) and isinstance(m.get("source"), str) and isinstance(m.get("destination"), str):
            contract_keys.add(f"{m['source'].strip().lower()} -> {m['destination'].strip().lower()}")
    if flat_keys != contract_keys:
        return False, f"forwarder_contract_{side}_flat_contract_mismatch"
    return True, None


def resolve_forwarder(source_data: dict, dest_data: dict, selected: list[str]) -> ResolvedEvidence:
    src_ok, src_reason = _reconcile_endpoint(source_data, "source")
    if not src_ok:
        return ResolvedEvidence("email_forwarders", False, src_reason)
    dst_ok, dst_reason = _reconcile_endpoint(dest_data, "destination")
    if not dst_ok:
        return ResolvedEvidence("email_forwarders", False, dst_reason)
    flat_pairs = _forwarder_flat_pairs(source_data)
    valid_ids: list[str] = []
    verified_pairs: dict[str, dict] = {}
    blocked: list[dict] = []
    for step_id in selected:
        suffix = step_id.split(":", 1)[1] if ":" in step_id else step_id
        matches = flat_pairs.get(suffix, [])
        if len(matches) != 1:
            reason = "duplicate_in_snapshot" if len(matches) > 1 else "not_in_snapshot"
            blocked.append({"step_id": step_id, "reason": reason})
        else:
            valid_ids.append(step_id)
            verified_pairs[step_id] = matches[0]
    return ResolvedEvidence("email_forwarders", True,
                            kwargs={"step_ids": valid_ids, "verified_pairs": verified_pairs},
                            blocked=blocked)


def resolve_default_address(source_data: dict, dest_data: dict, selected: list[str]) -> ResolvedEvidence:
    src_contract = source_data.get("default_address_contract")
    dst_contract = dest_data.get("default_address_contract")
    if not default_address_rules.is_write_eligible(src_contract) or not default_address_rules.is_write_eligible(dst_contract):
        side = "source" if not default_address_rules.is_write_eligible(src_contract) else "destination"
        return ResolvedEvidence("default_address", False, f"default_address_contract_{side}_not_eligible")
    records = src_contract.get("records", []) if isinstance(src_contract, dict) else []
    by_domain: dict[str, list[dict]] = {}
    for r in records:
        if isinstance(r, dict) and r.get("domain"):
            by_domain.setdefault(r["domain"], []).append(r)
    source_records: dict[str, dict] = {}
    valid_ids: list[str] = []
    blocked: list[dict] = []
    dest_username = dst_contract.get("account_username") if isinstance(dst_contract, dict) else None
    for step_id in selected:
        domain = (step_id.split(":", 1)[1] if ":" in step_id else step_id).strip().lower()
        matches = by_domain.get(domain, [])
        if len(matches) != 1 or matches[0].get("completeness") != "complete":
            reason = "duplicate" if len(matches) > 1 else "not_in_contract" if not matches else "incomplete"
            blocked.append({"step_id": step_id, "reason": reason})
        else:
            source_records[domain] = matches[0]
            valid_ids.append(step_id)
    return ResolvedEvidence("default_address", True,
                            kwargs={"step_ids": valid_ids, "source_records": source_records, "dest_username": dest_username},
                            blocked=blocked)


def resolve_routing(source_data: dict, dest_data: dict, selected: list[str]) -> ResolvedEvidence:
    src_contract = source_data.get("email_routing_contract")
    dst_contract = dest_data.get("email_routing_contract")
    if not routing_rules.is_write_eligible(src_contract) or not routing_rules.is_write_eligible(dst_contract):
        side = "source" if not routing_rules.is_write_eligible(src_contract) else "destination"
        return ResolvedEvidence("email_routing", False, f"routing_contract_{side}_not_eligible")
    records = src_contract.get("records", []) if isinstance(src_contract, dict) else []
    by_domain: dict[str, list[dict]] = {}
    for r in records:
        if isinstance(r, dict) and r.get("domain"):
            by_domain.setdefault(r["domain"], []).append(r)
    source_records: dict[str, dict] = {}
    valid_ids: list[str] = []
    blocked: list[dict] = []
    for step_id in selected:
        domain = (step_id.split(":", 1)[1] if ":" in step_id else step_id).strip().lower()
        matches = by_domain.get(domain, [])
        if len(matches) != 1 or matches[0].get("completeness") != "complete":
            reason = "duplicate" if len(matches) > 1 else "not_in_contract" if not matches else "incomplete"
            blocked.append({"step_id": step_id, "reason": reason})
        else:
            source_records[domain] = matches[0]
            valid_ids.append(step_id)
    return ResolvedEvidence("email_routing", True,
                            kwargs={"step_ids": valid_ids, "source_records": source_records, "policies": {}},
                            blocked=blocked)


def resolve_filters(source_data: dict, dest_data: dict, selected: list[str]) -> ResolvedEvidence:
    src_contract = source_data.get("email_filters_contract")
    dst_contract = dest_data.get("email_filters_contract")
    if not filter_rules.is_write_eligible(src_contract) or not filter_rules.is_write_eligible(dst_contract):
        side = "source" if not filter_rules.is_write_eligible(src_contract) else "destination"
        return ResolvedEvidence("email_filters", False, f"filter_contract_{side}_not_eligible")
    scopes = src_contract.get("scopes", []) if isinstance(src_contract, dict) else []
    by_scope_name: dict[str, list[dict]] = {}
    for scope_block in scopes:
        if not isinstance(scope_block, dict):
            continue
        scope = scope_block.get("scope", "account")
        for record in scope_block.get("records", []):
            if not isinstance(record, dict) or not isinstance(record.get("name"), str):
                continue
            key = f"{scope}:{record['name']}"
            enriched = {**record, "scope": scope,
                        "scope_account": None if scope == "account" else scope}
            by_scope_name.setdefault(key, []).append(enriched)
    specs_by_scope: dict[str, list[dict]] = {}
    valid_ids: list[str] = []
    blocked: list[dict] = []
    for step_id in selected:
        suffix = step_id.split(":", 1)[1] if ":" in step_id else step_id
        matches = by_scope_name.get(suffix, [])
        if len(matches) != 1:
            reason = "duplicate_in_contract" if len(matches) > 1 else "not_in_contract"
            blocked.append({"step_id": step_id, "reason": reason})
            continue
        record = matches[0]
        if record.get("completeness") != filter_rules.COMPLETE:
            blocked.append({"step_id": step_id, "reason": f"completeness_{record.get('completeness')}"})
            continue
        rebuilt_fp = filter_rules.fingerprint(record["scope"], record["name"],
                                              record.get("rules"), record.get("actions"))
        if rebuilt_fp != record.get("fingerprint"):
            blocked.append({"step_id": step_id, "reason": "fingerprint_mismatch"})
            continue
        scope = record["scope"]
        spec = {"step_id": step_id, "scope": scope, "filtername": record["name"],
                "rules": record.get("rules"), "actions": record.get("actions"),
                "source_status": filter_rules.ST_VERIFIED,
                "source_fingerprint": rebuilt_fp,
                "scope_account": record.get("scope_account"),
                "scope_present": True}
        specs_by_scope.setdefault(scope, []).append(spec)
        valid_ids.append(step_id)
    return ResolvedEvidence("email_filters", True,
                            kwargs={"specs_by_scope": specs_by_scope}, blocked=blocked)


def resolve_autoresponders(source_data: dict, dest_data: dict, selected: list[str]) -> ResolvedEvidence:
    src_contract = source_data.get("autoresponder_contract")
    dst_contract = dest_data.get("autoresponder_contract")
    if not autoresponder_rules.is_write_eligible(src_contract) or not autoresponder_rules.is_write_eligible(dst_contract):
        side = "source" if not autoresponder_rules.is_write_eligible(src_contract) else "destination"
        return ResolvedEvidence("email_autoresponders", False, f"autoresponder_contract_{side}_not_eligible")
    flat = source_data.get("email_autoresponders", [])
    if not isinstance(flat, list):
        return ResolvedEvidence("email_autoresponders", False, "snapshot_autoresponders_missing")
    contract_records: dict[str, list[dict]] = {}
    for domain_block in (src_contract or {}).get("domains", []):
        if not isinstance(domain_block, dict):
            continue
        for record in domain_block.get("records", []):
            if isinstance(record, dict) and isinstance(record.get("address"), str):
                addr = record["address"].strip()
                contract_records.setdefault(addr, []).append({**record, "_domain": domain_block.get("domain")})
    by_domain: dict[str, list[dict]] = {}
    verified_entries: list[dict] = []
    valid_ids: list[str] = []
    blocked: list[dict] = []
    seen_addresses: set[str] = set()
    for step_id in selected:
        address = (step_id.split(":", 1)[1] if ":" in step_id else step_id).strip()
        if address in seen_addresses:
            blocked.append({"step_id": step_id, "reason": "duplicate_step_id"})
            continue
        seen_addresses.add(address)
        cr_matches = contract_records.get(address, [])
        if len(cr_matches) != 1:
            reason = "duplicate_in_contract" if len(cr_matches) > 1 else "not_in_contract"
            blocked.append({"step_id": step_id, "reason": reason})
            continue
        cr = cr_matches[0]
        if cr.get("completeness") != autoresponder_rules.COMPLETE or cr.get("issue") is not None:
            blocked.append({"step_id": step_id, "reason": "record_incomplete"})
            continue
        snapshot_matches = [e for e in flat if isinstance(e, dict) and (e.get("email") or "").strip() == address]
        if len(snapshot_matches) != 1 or snapshot_matches[0].get("_detail_status") != "succeeded":
            reason = "duplicate_in_snapshot" if len(snapshot_matches) > 1 else "not_in_snapshot"
            blocked.append({"step_id": step_id, "reason": reason})
            continue
        entry = snapshot_matches[0]
        rebuilt_fp = autoresponder_rules.fingerprint(address, entry)
        if rebuilt_fp != cr.get("fingerprint"):
            blocked.append({"step_id": step_id, "reason": "fingerprint_mismatch"})
            continue
        domain = cr.get("_domain")
        if not domain:
            blocked.append({"step_id": step_id, "reason": "domain_missing"})
            continue
        if entry.get("_domain") != domain:
            blocked.append({"step_id": step_id, "reason": "domain_mismatch"})
            continue
        spec = {"step_id": step_id, "address": address, "domain_present": True}
        by_domain.setdefault(domain, []).append(spec)
        verified_entries.append(entry)
        valid_ids.append(step_id)
    projected_domains: dict[str, list[dict]] = {}
    for sid in valid_ids:
        addr = (sid.split(":", 1)[1] if ":" in sid else sid).strip()
        cr_list = contract_records.get(addr, [])
        if cr_list:
            cr = cr_list[0]
            dom = cr.get("_domain")
            if dom:
                projected_domains.setdefault(dom, []).append(
                    {k: v for k, v in cr.items() if k != "_domain"})
    projected_contract = {
        "version": (src_contract or {}).get("version"),
        "status": (src_contract or {}).get("status"),
        "domains": [{"domain": d, "status": "succeeded", "records": recs, "message": None}
                     for d, recs in projected_domains.items()],
    }
    projected_snapshot = {"email_autoresponders": verified_entries}
    return ResolvedEvidence("email_autoresponders", True,
                            kwargs={"by_domain": by_domain, "snapshot_data": projected_snapshot,
                                    "contract": projected_contract},
                            blocked=blocked)


_RESOLVERS = {
    "email_forwarders": resolve_forwarder,
    "default_address": resolve_default_address,
    "email_routing": resolve_routing,
    "email_filters": resolve_filters,
    "email_autoresponders": resolve_autoresponders,
}


def resolve_category(category: str, source_data: dict, dest_data: dict,
                     selected_step_ids: list[str]) -> ResolvedEvidence:
    resolver = _RESOLVERS.get(category)
    if resolver is None:
        return ResolvedEvidence(category, False, "unknown_category")
    return resolver(source_data, dest_data, selected_step_ids)
