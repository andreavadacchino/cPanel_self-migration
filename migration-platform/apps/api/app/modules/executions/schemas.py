"""Pydantic schemas for the execution API.

Every field is safe to expose. The spec body is not stored and not returned:
what travels is its digest and the ids it was built from. ``artifact_manifest``
holds workspace-relative paths, never absolute ones — execution-result-v1
rejects those before they reach the database.

The request carries a scope and nothing else. It does not carry a plan id, a
snapshot id or a mode the client picked: the server resolves the anchors and
recomputes every gate, because a client that could name its own plan could name
a stale one.
"""

from __future__ import annotations

from datetime import datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict


class ExecutionScopeCreate(BaseModel):
    """What to migrate. The shape of execution-spec-v1's ``scope``.

    ``extra="forbid"``: an unknown key is a key the operator believes is being
    honoured and that nothing reads. The contract forbids them too — this
    refuses them one layer earlier, with a field-level 422.

    The combination rules live elsewhere and on purpose: the contract
    (execution-spec-v1) rejects an empty scope and a filter without its scope;
    ``domain.execution_gates`` rejects the three combinations the executor
    itself refuses to run. Neither can be expressed as a field constraint.
    """

    model_config = ConfigDict(extra="forbid")

    mail: bool = False
    files: bool = False
    databases: bool = False
    domain_filter: str | None = None
    mailbox_filter: str | None = None


class ExecutionCreate(BaseModel):
    """The body of a create-execution request.

    ``mode`` is a Literal, not a string: an apply is not "not implemented yet"
    here, it is not expressible. A client asking for one gets a 422 from the
    schema, before a service, a gate or a database ever sees it.
    """

    model_config = ConfigDict(extra="forbid")

    mode: Literal["dry_run"] = "dry_run"
    scope: ExecutionScopeCreate


class MigrationExecutionRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    migration_id: int
    job_id: int | None = None

    # The plan, snapshots and comparison this execution is anchored to.
    plan_id: int
    source_snapshot_id: int
    destination_snapshot_id: int
    comparison_report_id: int

    mode: str
    status: str
    scope: dict

    run_id: str | None = None
    executor_version: str | None = None
    spec_version: int
    spec_sha256: str

    artifact_manifest: dict | None = None
    result_summary: dict | None = None
    error_code: str | None = None
    error_summary: str | None = None

    created_at: datetime
    started_at: datetime | None = None
    finished_at: datetime | None = None
