"""Execution contract v1 — the versioned documents exchanged with the Go executor.

Three documents, as decided in ``docs/ADR_V2_GO_EXECUTOR.md``:

    execution-spec-v1    platform -> executor   (new; the input spec)
    execution-event-v1   executor -> platform   (derived from internal/events.Event)
    execution-result-v1  executor -> platform   (derived from internal/events.RunReport)

``format_version`` is the DOCUMENT version. It is not the result's ``version``
field, which carries the executor build. The two never mean the same thing.

Policy: version 1 is supported; absent, zero, and future versions are rejected.
No best-effort interpretation, no silent downgrade.

Strictness differs by direction, on purpose. The input spec rejects unknown
fields at every level — a field the executor does not understand may be a field
the operator believes is being honoured. The outputs tolerate extra top-level
keys so the executor can add purely additive fields without breaking an older
platform; an incompatible change requires a new ``format_version``.

This module is the Python half of a cross-language contract. Its error messages
are asserted, substring by substring, against the Go validator in
``internal/executioncontract`` via ``testdata/execution-contract/manifest.json``.
Changing a message here without changing it there breaks the corpus.

Pure: no I/O, no network, no database.
"""

from __future__ import annotations

import json
import re
from datetime import datetime
from typing import Any, Final

from pydantic import BaseModel, ConfigDict

__all__ = [
    "CURRENT_FORMAT_VERSION",
    "SPEC_MODE_DRY_RUN",
    "RESULT_MODES",
    "SENSITIVE_SUBSTRINGS",
    "REDACTED_PLACEHOLDER",
    "ContractError",
    "ExecutionSpec",
    "SpecScope",
    "parse_spec",
    "validate_spec_json",
    "validate_event_json",
    "validate_result_json",
]


class ContractError(ValueError):
    """A document violates the execution contract.

    The message names the offending field. It never carries the document, and
    never the value of a sensitive key.
    """


CURRENT_FORMAT_VERSION: Final = 1

#: The only mode execution-spec-v1 accepts: the first spec governs a dry run.
#: Note the underscore. The *result* uses "dry-run" (hyphen) — see RESULT_MODES.
SPEC_MODE_DRY_RUN: Final = "dry_run"

#: Values buildRunReport and runAccountInventory actually emit today
#: (cmd/cpanel-self-migration/main.go). Deliberately not the spec's vocabulary.
RESULT_MODES: Final = frozenset({"dry-run", "apply", "account-inventory"})

_EXIT_STATUSES: Final = frozenset({"success", "failed", "interrupted"})
_LEVELS: Final = frozenset({"info", "warn", "error"})

_EVENT_TYPES: Final = frozenset(
    {
        "phase_started",
        "phase_completed",
        "phase_skipped",
        "phase_failed",
        "run_started",
        "run_completed",
        "run_failed",
    }
)

_PHASES: Final = frozenset(
    {
        "connect",
        "analyze_mail",
        "analyze_files",
        "analyze_db",
        "gather_data",
        "compare_mail",
        "compare_files",
        "compare_db",
        "create_domains",
        "migrate_mail",
        "verify_mail",
        "copy_files",
        "verify_files",
        "migrate_db",
        "verify_db",
    }
)

#: Mirrors ``sensitiveSubstrings`` in internal/events/redact.go. A test parses
#: that Go file and asserts this tuple still matches it, so the two cannot drift.
SENSITIVE_SUBSTRINGS: Final = (
    "token",
    "secret",
    "pass",
    "key",
    "auth",
    "cred",
    "cookie",
    "session",
    "bearer",
)

#: Mirrors ``redactedPlaceholder`` in internal/events/redact.go.
REDACTED_PLACEHOLDER: Final = "<redacted>"

# Applied before parsing so Go and Python agree on what a timestamp is.
_RFC3339 = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$")

#: The only characters JSON calls whitespace. Python's bare ``strip()`` also eats
#: U+00A0, U+2028, form feed and more, which Go's decoder rejects.
_JSON_WHITESPACE: Final = " \t\n\r"

#: Go decodes integers into int64. Python's int is unbounded; these bounds keep
#: the two validators from disagreeing on values Go cannot represent.
_INT64_MAX: Final = 2**63 - 1
_INT64_MIN: Final = -(2**63)

_SPEC_TOP_KEYS: Final = frozenset(
    {
        "format_version",
        "run_id",
        "plan_id",
        "source_snapshot_id",
        "destination_snapshot_id",
        "comparison_report_id",
        "mode",
        "scope",
    }
)
_SPEC_SCOPE_KEYS: Final = frozenset(
    {"mail", "files", "databases", "domain_filter", "mailbox_filter"}
)

_EVENT_REQUIRED: Final = (
    "format_version",
    "run_id",
    "ts",
    "level",
    "phase",
    "event",
    "message",
    "source",
    "destination",
)

_RESULT_REQUIRED: Final = (
    "format_version",
    "run_id",
    "version",
    "mode",
    "scope",
    "source",
    "destination",
    "started_at",
    "finished_at",
    "exit_status",
    "phases_completed",
    "warnings",
    "errors",
)


class SpecScope(BaseModel):
    """Scope of a governed run.

    The three booleans are required and never defaulted: an absent ``mail``
    must not silently mean ``false`` in one language and an error in the other.
    """

    model_config = ConfigDict(extra="forbid", strict=True, frozen=True)

    mail: bool
    files: bool
    databases: bool
    domain_filter: str | None = None
    mailbox_filter: str | None = None


class ExecutionSpec(BaseModel):
    """The platform -> executor input.

    References and non-secret data only: no host, no path, no argv, no
    credential. The worker resolves credentials at run time and never persists
    them here.
    """

    model_config = ConfigDict(extra="forbid", strict=True, frozen=True)

    format_version: int
    run_id: str
    plan_id: int
    source_snapshot_id: int
    destination_snapshot_id: int
    comparison_report_id: int
    mode: str
    scope: SpecScope


# --- entry points -----------------------------------------------------------


def validate_spec_json(raw: str | bytes) -> None:
    """Raise :class:`ContractError` unless *raw* is a valid execution-spec-v1."""
    parse_spec(raw)


def parse_spec(raw: str | bytes) -> ExecutionSpec:
    """Validate and decode an execution-spec-v1 document."""
    doc = _decode_single_object(raw)
    _reject_unknown(doc, _SPEC_TOP_KEYS, "")
    _check_format_version(doc)

    run_id = _require_str(doc, "run_id")
    _validate_run_id(run_id)

    ids = {
        key: _require_positive_int(doc, key)
        for key in (
            "plan_id",
            "source_snapshot_id",
            "destination_snapshot_id",
            "comparison_report_id",
        )
    }

    mode = _require_str(doc, "mode")
    if mode != SPEC_MODE_DRY_RUN:
        raise ContractError(
            f"invalid field mode: {mode!r} is not supported (only {SPEC_MODE_DRY_RUN!r})"
        )

    if "scope" not in doc:
        raise ContractError("missing field: scope")
    scope = doc["scope"]
    if not isinstance(scope, dict):
        raise ContractError("invalid field scope: expected an object")
    _reject_unknown(scope, _SPEC_SCOPE_KEYS, "scope.")

    mail = _require_bool(scope, "scope.mail", "mail")
    files = _require_bool(scope, "scope.files", "files")
    databases = _require_bool(scope, "scope.databases", "databases")
    if not (mail or files or databases):
        raise ContractError(
            "invalid field scope: at least one of mail, files, databases must be true"
        )

    domain_filter = _optional_str(scope, "scope.domain_filter", "domain_filter")
    mailbox_filter = _optional_str(scope, "scope.mailbox_filter", "mailbox_filter")
    if mailbox_filter and not mail:
        raise ContractError(
            "invalid field mailbox_filter: allowed only when scope.mail is true"
        )
    if domain_filter and not (mail or files):
        raise ContractError(
            "invalid field domain_filter: allowed only when scope.mail or scope.files is true"
        )

    return ExecutionSpec(
        format_version=CURRENT_FORMAT_VERSION,
        run_id=run_id,
        mode=mode,
        scope=SpecScope(
            mail=mail,
            files=files,
            databases=databases,
            domain_filter=domain_filter,
            mailbox_filter=mailbox_filter,
        ),
        **ids,
    )


def validate_event_json(raw: str | bytes) -> None:
    """Raise :class:`ContractError` unless *raw* is a valid execution-event-v1.

    Extra top-level keys are tolerated (additive evolution), but every document
    is scanned recursively for unredacted sensitive keys — extensibility must
    not become a leak channel.
    """
    doc = _decode_single_object(raw)
    _check_format_version(doc)
    _require_present(doc, _EVENT_REQUIRED)

    _validate_run_id(_require_str(doc, "run_id"))
    _require_timestamp(doc, "ts")

    level = _require_str(doc, "level")
    if level not in _LEVELS:
        raise ContractError(f"invalid field level: unknown level {level!r}")

    event_type = _require_str(doc, "event")
    if event_type not in _EVENT_TYPES:
        raise ContractError(f"invalid field event: unknown event type {event_type!r}")

    # Run-level events carry no phase, and `phase` has no omitempty in Go, so
    # the writer emits "". Empty is valid; an unknown non-empty one is not.
    phase = _require_str(doc, "phase")
    if phase != "" and phase not in _PHASES:
        raise ContractError(f"invalid field phase: unknown phase {phase!r}")

    _require_str(doc, "message")
    for key in ("source", "destination"):
        _check_host_ref(doc, key)

    _check_redacted(doc, "")


def validate_result_json(raw: str | bytes) -> None:
    """Raise :class:`ContractError` unless *raw* is a valid execution-result-v1."""
    doc = _decode_single_object(raw)
    _check_format_version(doc)
    _require_present(doc, _RESULT_REQUIRED)

    _validate_run_id(_require_str(doc, "run_id"))

    # The executor build version. Never the document format version.
    version = _require_str(doc, "version")
    if not version.strip():
        raise ContractError("invalid field version: must not be empty")

    mode = _require_str(doc, "mode")
    if mode not in RESULT_MODES:
        raise ContractError(f"invalid field mode: unknown mode {mode!r}")

    status = _require_str(doc, "exit_status")
    if status not in _EXIT_STATUSES:
        raise ContractError(f"invalid field exit_status: unknown exit status {status!r}")

    # Compared at full nanosecond precision, as Go does: truncating to
    # microseconds first would accept a report that finished 900ns before it
    # started, which the executor's own validator rejects.
    started = _require_timestamp(doc, "started_at")
    finished = _require_timestamp(doc, "finished_at")
    if finished < started:
        raise ContractError("invalid field finished_at: finished_at is before started_at")

    # The report's scope records what ran. Unlike the spec's scope it has no
    # "at least one true" rule: an account-inventory report has all three false.
    scope = doc["scope"]
    if not isinstance(scope, dict):
        raise ContractError("invalid field scope: expected an object")
    for key in ("mail", "files", "databases"):
        _require_bool(scope, f"scope.{key}", key)

    for key in ("source", "destination"):
        _check_host_ref(doc, key)

    completed = doc["phases_completed"]
    if not isinstance(completed, list):
        raise ContractError("invalid field phases_completed: expected an array")
    for i, phase in enumerate(completed):
        if not isinstance(phase, str):
            raise ContractError(f"invalid field phases_completed[{i}]: expected a string")
        if phase not in _PHASES:
            raise ContractError(f"invalid field phases_completed[{i}]: unknown phase {phase!r}")

    for key in ("warnings", "errors"):
        arr = doc[key]
        if not isinstance(arr, list):
            raise ContractError(f"invalid field {key}: expected an array")
        for i, item in enumerate(arr):
            if not isinstance(item, str):
                raise ContractError(f"invalid field {key}[{i}]: expected a string")

    # artifacts is omitempty on the Go side: absent is valid.
    if "artifacts" in doc:
        artifacts = doc["artifacts"]
        if not isinstance(artifacts, dict):
            raise ContractError("invalid field artifacts: expected an object")
        for name, value in artifacts.items():
            if not isinstance(value, str):
                raise ContractError(f"invalid artifact path for {name!r}: expected a string")
            reason = _artifact_path_error(value)
            if reason:
                raise ContractError(f"invalid artifact path for {name!r}: {reason}")

    _check_redacted(doc, "")


# --- shared helpers ---------------------------------------------------------


def _decode_single_object(raw: str | bytes) -> dict[str, Any]:
    """Decode exactly one JSON object.

    A second document, or any trailing bytes, is an error: a JSONL consumer
    must never silently accept two records glued together.

    Only the four characters JSON calls whitespace are stripped. Python's bare
    ``str.strip()`` also eats U+00A0, U+2028, form feed and friends, which Go's
    decoder rejects — a document one language accepts and the other refuses.
    """
    if isinstance(raw, bytes):
        # Go's encoding/json silently replaces invalid UTF-8 inside strings with
        # U+FFFD, so a truncated artifact would decode into mojibake there while
        # raising UnicodeDecodeError here — opposite verdicts, and an exception
        # this module promises never to raise. Both validators reject instead.
        try:
            text = raw.decode("utf-8")
        except UnicodeDecodeError:
            raise ContractError("invalid JSON: input is not valid UTF-8") from None
    else:
        text = raw
    stripped = text.lstrip(_JSON_WHITESPACE)
    try:
        doc, end = json.JSONDecoder().raw_decode(stripped)
    except json.JSONDecodeError as exc:
        raise ContractError(f"invalid JSON: {exc.msg}") from None
    if stripped[end:].strip(_JSON_WHITESPACE):
        raise ContractError("trailing JSON after document")
    if not isinstance(doc, dict):
        raise ContractError("invalid JSON: expected an object")
    return doc


def _check_format_version(doc: dict[str, Any]) -> None:
    if "format_version" not in doc:
        raise ContractError("missing field: format_version")
    value = doc["format_version"]
    # bool is a subclass of int in Python; `true` is not a version.
    if isinstance(value, bool) or not isinstance(value, int):
        if isinstance(value, float):
            raise ContractError(
                f"invalid field format_version: expected an integer, got {json.dumps(value)}"
            )
        raise ContractError("invalid field format_version: expected an integer")
    # Go decodes into int64. Python's int is unbounded, so a value Go cannot
    # represent must be rejected here too, with Go's message.
    if not _INT64_MIN <= value <= _INT64_MAX:
        raise ContractError("invalid field format_version: expected an integer")
    if value != CURRENT_FORMAT_VERSION:
        raise ContractError(
            f"unsupported format_version: {value} (supported: {CURRENT_FORMAT_VERSION})"
        )


def _reject_unknown(doc: dict[str, Any], allowed: frozenset[str], prefix: str) -> None:
    for key in doc:
        if key not in allowed:
            raise ContractError(f"unknown field: {prefix}{key}")


def _require_present(doc: dict[str, Any], keys: tuple[str, ...]) -> None:
    for key in keys:
        if key not in doc:
            raise ContractError(f"missing field: {key}")


def _require_str(doc: dict[str, Any], key: str) -> str:
    if key not in doc:
        raise ContractError(f"missing field: {key}")
    value = doc[key]
    if not isinstance(value, str):
        raise ContractError(f"invalid field {key}: expected a string")
    return value


def _optional_str(doc: dict[str, Any], label: str, key: str) -> str | None:
    if key not in doc:
        return None
    value = doc[key]
    if not isinstance(value, str):
        raise ContractError(f"invalid field {label}: expected a string")
    return value


def _require_bool(doc: dict[str, Any], label: str, key: str) -> bool:
    """Refuse "true" and 1: a scope boolean is explicit or it is nothing."""
    if key not in doc:
        raise ContractError(f"missing field: {label}")
    value = doc[key]
    if not isinstance(value, bool):
        raise ContractError(f"invalid field {label}: expected a boolean")
    return value


def _require_positive_int(doc: dict[str, Any], key: str) -> int:
    if key not in doc:
        raise ContractError(f"missing field: {key}")
    value = doc[key]
    # bool first: `True` is an int in Python but never a valid id.
    if isinstance(value, bool) or not isinstance(value, int):
        raise ContractError(f"invalid field {key}: expected an integer")
    # Go decodes into int64 and fails on overflow. Python's int is unbounded, so
    # without this the platform would accept a spec the executor cannot read.
    if not _INT64_MIN <= value <= _INT64_MAX:
        raise ContractError(f"invalid field {key}: expected an integer")
    if value <= 0:
        raise ContractError(f"invalid field {key}: must be a positive integer, got {value}")
    return value


def _validate_run_id(run_id: str) -> None:
    """Mirrors events.ValidateRunID.

    Go measures ``len(id)`` in BYTES. Measuring code points here would accept a
    65-character run_id of two-byte runes that the executor rejects.
    """
    if run_id == "":
        raise ContractError("invalid field run_id: run-id must not be empty")
    if len(run_id.encode("utf-8")) > 128:
        raise ContractError("invalid field run_id: run-id must not exceed 128 characters")
    if any(ch in run_id for ch in ("/", "\\", "\x00")):
        raise ContractError("invalid field run_id: run-id must not contain slashes or null bytes")


def _require_timestamp(doc: dict[str, Any], key: str) -> tuple[datetime, int]:
    """Parse an RFC3339 timestamp into (whole-second datetime, nanoseconds).

    The nanoseconds are kept separate rather than folded into the datetime:
    Go emits nanosecond precision and ``datetime`` tops out at microseconds, so
    a document the executor writes must not be one the platform refuses to read —
    but truncating before an ordering comparison would let a report that finished
    900ns before it started slip through here while Go rejects it.
    """
    value = _require_str(doc, key)
    if not _RFC3339.match(value):
        raise ContractError(
            f"invalid field {key}: {value!r} is not an RFC3339 timestamp with a timezone"
        )

    text = value[:-1] + "+00:00" if value.endswith("Z") else value
    nanos = 0
    match = re.match(r"^(.*?)\.(\d+)([+-]\d{2}:\d{2})$", text)
    if match:
        head, frac, offset = match.groups()
        nanos = int(frac[:9].ljust(9, "0"))
        text = f"{head}{offset}"

    # The regex pins the SHAPE, not the calendar: "2026-02-30T00:00:00Z" and
    # "0000-01-01T00:00:00Z" match it. fromisoformat raises a bare ValueError on
    # those; the contract promises ContractError, and Go rejects them cleanly.
    try:
        parsed = datetime.fromisoformat(text)
    except ValueError as exc:
        raise ContractError(f"invalid field {key}: {exc}") from None
    if parsed.tzinfo is None:  # pragma: no cover - the regex already requires an offset
        raise ContractError(f"invalid field {key}: missing timezone")
    return parsed, nanos


def _check_host_ref(doc: dict[str, Any], key: str) -> None:
    """Pins the shape events.HostRef actually marshals to.

    It is a non-pointer struct, so Go's ``omitempty`` never fires: the key is
    always present, and both members are always present, possibly as "".
    """
    if key not in doc:
        raise ContractError(f"missing field: {key}")
    host = doc[key]
    if not isinstance(host, dict):
        raise ContractError(f"invalid field {key}: expected an object")
    for member in ("ip", "user"):
        if member not in host:
            raise ContractError(f"missing field: {key}.{member}")
        if not isinstance(host[member], str):
            raise ContractError(f"invalid field {key}.{member}: expected a string")


def _is_sensitive_key(key: str) -> bool:
    lower = key.strip().lower()
    return any(sub in lower for sub in SENSITIVE_SUBSTRINGS)


def _redacted_ok(value: Any) -> bool:
    """Mirrors redactValue in internal/events/redact.go.

    The writer leaves ``null`` and ``""`` alone (nothing to hide) and replaces
    every other value with the placeholder. A sensitive key holding ``false``,
    ``0``, an object, or an array therefore never went through the writer.
    """
    if value is None:
        return True
    if not isinstance(value, str):
        return False
    return value == "" or value == REDACTED_PLACEHOLDER


def _check_redacted(node: Any, path: str) -> None:
    """Walk the whole document; a secret must not have got past the writer.

    The message names the key, never its value.
    """
    if isinstance(node, dict):
        for key, child in node.items():
            child_path = f"{path}.{key}" if path else key
            if _is_sensitive_key(key) and not _redacted_ok(child):
                raise ContractError(f"sensitive key {child_path} is not redacted")
            _check_redacted(child, child_path)
    elif isinstance(node, list):
        for i, child in enumerate(node):
            _check_redacted(child, f"{path}[{i}]")


def _artifact_path_error(path: str) -> str | None:
    """Confine an artifact to the run workspace.

    The path is data from the executor and the platform resolves it against a
    directory it owns, so a path that escapes is a write-anywhere primitive.
    Both separators count: a report produced on Windows must not smuggle
    ``..\\..\\etc`` past a Unix-only check.
    """
    if path == "":
        return "must not be empty"
    if "\x00" in path:
        return "must not contain NUL bytes"
    norm = path.replace("\\", "/")
    if norm.startswith("/"):
        return f"must be relative to the workspace, got absolute path {path!r}"
    # A colon is never legitimate in an engine-produced artifact name, and it
    # carries two Windows meanings we must not resolve: a drive letter (C:\) and
    # an alternate data stream (a:b). Rejecting the character outright keeps Go
    # and Python identical — an index-based drive check disagrees the moment the
    # path holds a multi-byte rune, because Go indexes bytes and Python indexes
    # code points.
    if ":" in path:
        return f"must not contain ':' (drive letters and alternate data streams), got {path!r}"
    if any(segment == ".." for segment in norm.split("/")):
        return f"must not contain '..' segments, got {path!r}"
    return None
