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

from pydantic import BaseModel, ConfigDict, Field, ValidationInfo, field_validator

#: RFC 1035: a fully qualified domain name is at most 253 characters.
MAX_DOMAIN_FILTER = 253
#: RFC 3696: a local-part (64) + "@" + a domain (255).
MAX_MAILBOX_FILTER = 320


class ExecutionScopeCreate(BaseModel):
    """What to migrate. The shape of execution-spec-v1's ``scope``.

    ``extra="forbid"``: an unknown key is a key the operator believes is being
    honoured and that nothing reads. The contract forbids them too — this
    refuses them one layer earlier, with a field-level 422.

    The filters are bounded and checked for being *usable strings* here, because
    that is a property of the request. Two other families of rule live elsewhere,
    on purpose: the contract (execution-spec-v1) rejects an empty scope and a
    filter without its scope, and ``domain.execution_gates`` rejects the
    combinations the executor itself refuses to run — including a BLANK filter,
    which the engine would read as *no filter* and silently widen to the whole
    account. Neither is expressible as a field constraint.
    """

    model_config = ConfigDict(extra="forbid")

    mail: bool = False
    files: bool = False
    databases: bool = False
    domain_filter: str | None = Field(default=None, max_length=MAX_DOMAIN_FILTER)
    mailbox_filter: str | None = Field(default=None, max_length=MAX_MAILBOX_FILTER)

    @field_validator("domain_filter", "mailbox_filter")
    @classmethod
    def _must_be_a_usable_string(
        cls, value: str | None, info: ValidationInfo
    ) -> str | None:
        """A filter must be a string a domain or an address could actually be.

        Two ways it can fail to be one, both reachable from a well-formed JSON
        body:

        A **lone surrogate**. JSON's grammar does not require surrogates to be
        paired, and Python decodes ``"\\ud800"`` without complaint — but the spec
        is serialized with ``.encode("utf-8")``, where it raises. Unguarded, the
        platform's most carefully gated route answers a malformed filter with an
        unhandled 500 while every other bad scope gets a clean 422.

        A **control character**. No domain and no address contains one; a NUL or a
        newline in a filter is a corrupted value, and the contract's own run-id
        rule already refuses exactly these. Refused, not stripped: trimming it
        would silently change which mailbox the operator asked for.
        """
        if value is None:
            return value
        if any(ord(ch) < 0x20 or ord(ch) == 0x7F for ch in value):
            raise ValueError(
                f"{info.field_name} contains a control character; a domain or an "
                "address never does"
            )
        try:
            value.encode("utf-8")
        except UnicodeEncodeError as exc:
            raise ValueError(
                f"{info.field_name} is not valid UTF-8 (a lone surrogate); it could "
                "not be written into an execution spec"
            ) from exc
        return value


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
