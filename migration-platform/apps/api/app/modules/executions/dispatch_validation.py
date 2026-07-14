"""Strict validation for checkpoint, compensation and JSON shape (B4e-iii-c-iii-b R1-bis).

Rejects non-JSON types, forbidden keys, unknown categories, invalid schemas.
Never logs or includes rejected values in error messages.
"""

from __future__ import annotations

import json
import math

from app.core.errors import ConflictError
from app.modules.executions.email_phase_registry import EMAIL_CATEGORIES

_VALID_CAT_STATUS = frozenset({"completed", "pending", "failed"})
_VALID_REASONS = frozenset({
    "stopped_by_prior", "cancelled", "disabled", "category_gate_rejected",
    "snapshot_invalid", "evidence_unresolved", "blocked_items",
    "category_execution_conflict", "category_phase_failed", "category_pending",
})
_FORBIDDEN_KEYS = frozenset({
    "raw", "payload", "body", "subject", "from", "rules", "actions",
    "password", "token", "ciphertext", "secret", "credentials",
    "snapshot", "contract", "kwargs",
})
_MAX_SIZE = 64 * 1024
_MAX_DEPTH = 8
_COMP_KEYS = {
    "email_forwarders": frozenset({"action", "item", "reverse", "step_id"}),
    "default_address": frozenset({"action", "domain", "reverse", "requires_confirmation", "backup_ref", "step_id"}),
    "email_routing": frozenset({"action", "domain", "reverse", "requires_confirmation", "backup_ref", "step_id"}),
    "email_filters": frozenset({"action", "scope", "name", "fingerprint", "reverse", "requires_confirmation", "step_id"}),
    "email_autoresponders": frozenset({"action", "domain", "address", "fingerprint", "reverse", "requires_confirmation", "step_id"}),
}
_BACKUP_REF_CATS = frozenset({"default_address", "email_routing"})
_TERMINAL_KEYS = frozenset({"domains", "email", "pending_categories", "email_categories", "attempt_id", "completed"})


def assert_strict_json(obj, depth=0) -> None:
    if depth > _MAX_DEPTH:
        raise ConflictError("Nesting eccessivo")
    if obj is None or isinstance(obj, bool):
        return
    if isinstance(obj, int):
        return
    if isinstance(obj, float):
        if math.isnan(obj) or math.isinf(obj):
            raise ConflictError("Float non finito")
        return
    if isinstance(obj, str):
        return
    if isinstance(obj, list):
        for item in obj:
            assert_strict_json(item, depth + 1)
        return
    if isinstance(obj, dict):
        for k, v in obj.items():
            if not isinstance(k, str):
                raise ConflictError("Chiave dict non stringa")
            assert_strict_json(v, depth + 1)
        return
    raise ConflictError("Tipo non JSON")


def _assert_size(obj) -> None:
    raw = json.dumps(obj)
    if len(raw.encode("utf-8")) > _MAX_SIZE:
        raise ConflictError("Payload eccessivo")


def _scan_forbidden(obj, depth=0) -> bool:
    if depth > _MAX_DEPTH:
        return True
    if isinstance(obj, dict):
        for k, v in obj.items():
            if isinstance(k, str) and k.lower() in _FORBIDDEN_KEYS:
                return True
            if _scan_forbidden(v, depth + 1):
                return True
    elif isinstance(obj, list):
        for item in obj:
            if _scan_forbidden(item, depth + 1):
                return True
    return False


def validate_progress_checkpoint(checkpoint: dict) -> None:
    if not isinstance(checkpoint, dict):
        raise ConflictError("Checkpoint non valido")
    assert_strict_json(checkpoint)
    _assert_size(checkpoint)
    allowed_top = {"categories", "completed_step_ids"}
    if set(checkpoint.keys()) != allowed_top:
        raise ConflictError("Checkpoint chiavi non conformi")
    cats = checkpoint["categories"]
    if not isinstance(cats, list):
        raise ConflictError("categories non lista")
    all_completed: list[str] = []
    seen_cats: set[str] = set()
    for entry in cats:
        if not isinstance(entry, dict):
            raise ConflictError("Category entry non dict")
        required = {"category", "status", "completed"}
        optional = {"reason"}
        if not required <= set(entry.keys()) or set(entry.keys()) - required - optional:
            raise ConflictError("Category entry chiavi non conformi")
        cat = entry["category"]
        if cat not in EMAIL_CATEGORIES:
            raise ConflictError("Category sconosciuta")
        if cat in seen_cats:
            raise ConflictError("Category duplicata")
        seen_cats.add(cat)
        if entry["status"] not in _VALID_CAT_STATUS:
            raise ConflictError("Status non valido")
        completed = entry["completed"]
        if not isinstance(completed, list) or not all(isinstance(s, str) and s for s in completed):
            raise ConflictError("Completed non valido")
        if len(completed) != len(set(completed)):
            raise ConflictError("Step duplicato")
        all_completed.extend(completed)
        reason = entry.get("reason")
        if reason is not None and reason not in _VALID_REASONS:
            raise ConflictError("Reason non consentito")
    sids = checkpoint["completed_step_ids"]
    if not isinstance(sids, list) or not all(isinstance(s, str) for s in sids):
        raise ConflictError("Step IDs non validi")
    if len(sids) != len(set(sids)):
        raise ConflictError("Step ID duplicato")
    if sorted(sids) != sorted(all_completed):
        raise ConflictError("Step IDs non corrispondono ai completed")


def validate_compensation(compensation: dict) -> None:
    if not isinstance(compensation, dict):
        raise ConflictError("Compensation non valida")
    assert_strict_json(compensation)
    _assert_size(compensation)
    if _scan_forbidden(compensation):
        raise ConflictError("Compensation contiene chiave vietata")
    for cat, descriptors in compensation.items():
        if cat not in EMAIL_CATEGORIES:
            raise ConflictError("Compensation categoria sconosciuta")
        allowed = _COMP_KEYS.get(cat)
        if allowed is None:
            raise ConflictError("Compensation schema mancante")
        if not isinstance(descriptors, list):
            raise ConflictError("Compensation descriptors non lista")
        for desc in descriptors:
            if not isinstance(desc, dict):
                raise ConflictError("Compensation descriptor non dict")
            if set(desc.keys()) - allowed:
                raise ConflictError("Compensation chiave non consentita")
            if "backup_ref" in desc and cat not in _BACKUP_REF_CATS:
                raise ConflictError("backup_ref in categoria non compensabile")
            for v in desc.values():
                if not isinstance(v, (str, bool, int, float, type(None))):
                    raise ConflictError("Compensation valore non scalare")


def validate_terminal_checkpoint(checkpoint: dict) -> None:
    if not isinstance(checkpoint, dict):
        raise ConflictError("Terminal checkpoint non valido")
    assert_strict_json(checkpoint)
    _assert_size(checkpoint)
    if _scan_forbidden(checkpoint):
        raise ConflictError("Terminal checkpoint contiene chiave vietata")
    if set(checkpoint.keys()) - _TERMINAL_KEYS:
        raise ConflictError("Terminal checkpoint chiave non consentita")
    for key in ("domains", "email"):
        val = checkpoint.get(key)
        if val is not None:
            if not isinstance(val, list) or not all(isinstance(s, str) for s in val):
                raise ConflictError("Terminal checkpoint lista non valida")
    pc = checkpoint.get("pending_categories")
    if pc is not None:
        if not isinstance(pc, list) or not all(isinstance(s, str) for s in pc):
            raise ConflictError("pending_categories non valido")


def validate_terminal_compensation(compensation: dict | None) -> None:
    if compensation is None:
        return
    if not isinstance(compensation, dict):
        raise ConflictError("Terminal compensation non valida")
    assert_strict_json(compensation)
    _assert_size(compensation)
    if _scan_forbidden(compensation):
        raise ConflictError("Terminal compensation contiene chiave vietata")
