"""Build and anchor the execution-spec-v1 document.

Pure: no I/O, no database, no network. Given the references the operator
approved, produce the exact bytes that will one day be handed to the Go
executor, and the SHA-256 that anchors an execution row to them.

Nothing here executes anything. The spec carries references and non-secret data
only — no host, no path, no argv, no credential — and the worker resolves
credentials at run time.

Why hash the bytes and not the object: a hash over a re-serialized dict is a
hash over whichever canonicalization the serializer happened to choose that day.
``canonical_spec_bytes`` fixes one serialization, and ``spec_sha256`` hashes
exactly what would be written. There is no second opinion to disagree with.
"""

from __future__ import annotations

import hashlib
import json
from typing import Any, Final

from domain.execution_contract import (
    CURRENT_FORMAT_VERSION,
    SPEC_MODE_DRY_RUN,
    validate_spec_json,
)

__all__ = [
    "SPEC_VERSION",
    "build_execution_spec",
    "canonical_spec_bytes",
    "spec_sha256",
]

#: The document version of every spec this module produces. Mirrors
#: execution_contract.CURRENT_FORMAT_VERSION; kept as its own name because the
#: execution row stores it under `spec_version`.
SPEC_VERSION: Final = CURRENT_FORMAT_VERSION


def build_execution_spec(
    *,
    run_id: str,
    plan_id: int,
    source_snapshot_id: int,
    destination_snapshot_id: int,
    comparison_report_id: int,
    mail: bool,
    files: bool,
    databases: bool,
    domain_filter: str | None = None,
    mailbox_filter: str | None = None,
) -> dict[str, Any]:
    """Assemble a valid execution-spec-v1 document.

    Every argument is keyword-only: four consecutive integer ids are exactly the
    kind of signature a caller silently transposes.

    The result is validated before it is returned, so an invalid spec can never
    be hashed into an execution row. Only ``dry_run`` is reachable: v1 accepts
    no other mode, and the first governed run writes nothing.

    Raises :class:`domain.execution_contract.ContractError` if the arguments do
    not form a valid spec — for example an empty scope, or a ``mailbox_filter``
    without ``mail``.
    """
    scope: dict[str, Any] = {"mail": mail, "files": files, "databases": databases}
    # Absent, not null: v1 forbids unknown fields and gives the two filters no
    # null form. A filter that was not chosen simply does not appear.
    if domain_filter is not None:
        scope["domain_filter"] = domain_filter
    if mailbox_filter is not None:
        scope["mailbox_filter"] = mailbox_filter

    spec: dict[str, Any] = {
        "format_version": SPEC_VERSION,
        "run_id": run_id,
        "plan_id": plan_id,
        "source_snapshot_id": source_snapshot_id,
        "destination_snapshot_id": destination_snapshot_id,
        "comparison_report_id": comparison_report_id,
        "mode": SPEC_MODE_DRY_RUN,
        "scope": scope,
    }

    # The contract is the authority, not this function. If the two ever disagree
    # about what a valid spec is, the contract wins and this raises.
    validate_spec_json(canonical_spec_bytes(spec))
    return spec


def canonical_spec_bytes(spec: dict[str, Any]) -> bytes:
    """Serialize a spec to the one byte sequence that gets hashed and written.

    Sorted keys and no insignificant whitespace, so two runs of the same spec
    hash identically. UTF-8, never escaped to ASCII: the Go decoder reads UTF-8,
    and ``\\uXXXX`` escapes would make the bytes depend on the writer.
    """
    return json.dumps(
        spec, sort_keys=True, separators=(",", ":"), ensure_ascii=False
    ).encode("utf-8")


def spec_sha256(raw: bytes) -> str:
    """Hex SHA-256 of the exact spec bytes.

    Takes bytes, not a dict, so the digest can never be computed over a
    different serialization than the one handed to the executor.
    """
    return hashlib.sha256(raw).hexdigest()
