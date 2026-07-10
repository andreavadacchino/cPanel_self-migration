"""Python half of the cross-language execution-contract gate.

The Go half lives in ``internal/executioncontract/contract_test.go``. Both read
``testdata/execution-contract/manifest.json`` — the same fixtures, the same
expected verdicts, the same expected error substrings. A corpus each language
kept privately would prove nothing about agreement.
"""

from __future__ import annotations

import json
import os
import re
from pathlib import Path

import pytest
from pydantic import ValidationError

from domain.execution_contract import (
    CURRENT_FORMAT_VERSION,
    REDACTED_PLACEHOLDER,
    RESULT_MODES,
    SENSITIVE_SUBSTRINGS,
    SPEC_MODE_DRY_RUN,
    ContractError,
    ExecutionSpec,
    SpecScope,
    _artifact_path_error,
    _EVENT_REQUIRED,
    _EVENT_TYPES,
    _EXIT_STATUSES,
    _is_sensitive_key,
    _LEVELS,
    _PHASES,
    _redacted_ok,
    _RESULT_REQUIRED,
    _SPEC_SCOPE_KEYS,
    _SPEC_TOP_KEYS,
    parse_spec,
    validate_event_json,
    validate_result_json,
    validate_spec_json,
)


def _repo_root() -> Path:
    """packages/domain/tests -> packages/domain -> packages -> migration-platform -> repo."""
    if env := os.environ.get("EXECUTION_CONTRACT_FIXTURES"):
        return Path(env).resolve().parents[1]
    return Path(__file__).resolve().parents[4]


FIXTURE_ROOT = _repo_root() / "testdata" / "execution-contract"

VALIDATORS = {
    "spec": validate_spec_json,
    "event": validate_event_json,
    "result": validate_result_json,
}


def _manifest() -> dict:
    return json.loads((FIXTURE_ROOT / "manifest.json").read_text())


def _fixtures() -> list[dict]:
    fixtures = _manifest()["fixtures"]
    assert fixtures, "manifest declares no fixtures"
    return fixtures


def _ids(fixtures: list[dict]) -> list[str]:
    return [f["path"] for f in fixtures]


_ALL = _fixtures()


@pytest.mark.parametrize("fixture", _ALL, ids=_ids(_ALL))
def test_manifest_fixture(fixture: dict) -> None:
    raw = (FIXTURE_ROOT / fixture["path"]).read_text()
    validator = VALIDATORS[fixture["kind"]]

    if fixture["expected_valid"]:
        validator(raw)  # must not raise
        return

    with pytest.raises(ContractError) as excinfo:
        validator(raw)
    # A fixture must fail for the declared reason, not merely fail.
    assert fixture["expected_error_substring"] in str(excinfo.value), (
        f"error {str(excinfo.value)!r} does not contain "
        f"{fixture['expected_error_substring']!r} — the fixture may be failing "
        "for the wrong reason"
    )


def test_manifest_covers_every_fixture_on_disk() -> None:
    declared = {f["path"] for f in _fixtures()}
    on_disk = {
        f"{sub}/{p.name}"
        for sub in ("valid", "invalid")
        for p in (FIXTURE_ROOT / sub).iterdir()
        if p.is_file()
    }
    assert on_disk - declared == set(), "fixtures on disk are not declared in the manifest"
    assert declared - on_disk == set(), "manifest declares fixtures that do not exist"


def test_fixture_corpus_is_not_trivial() -> None:
    """A corpus of only-valid or only-invalid documents proves nothing."""
    fixtures = _fixtures()
    valid = [f for f in fixtures if f["expected_valid"]]
    invalid = [f for f in fixtures if not f["expected_valid"]]
    assert len(valid) >= 5
    assert len(invalid) >= 20
    assert {f["kind"] for f in fixtures} == {"spec", "event", "result"}


# --- drift guards -----------------------------------------------------------


def test_sensitive_substrings_match_the_go_source() -> None:
    """The redaction net must be one list, not two that drift apart.

    Go exports the predicate; Python cannot import it, so this parses the Go
    source and compares. If someone adds a substring on one side only, this
    fails instead of quietly letting a secret through the Python validator.
    """
    go_src = (_repo_root() / "internal" / "events" / "redact.go").read_text()
    block = re.search(r"var sensitiveSubstrings = \[\]string\{(.*?)\}", go_src, re.S)
    assert block, "could not locate sensitiveSubstrings in internal/events/redact.go"
    go_list = tuple(re.findall(r'"([^"]+)"', block.group(1)))
    assert go_list == SENSITIVE_SUBSTRINGS


def test_redacted_placeholder_matches_the_go_source() -> None:
    go_src = (_repo_root() / "internal" / "events" / "redact.go").read_text()
    match = re.search(r'const redactedPlaceholder = "([^"]+)"', go_src)
    assert match and match.group(1) == REDACTED_PLACEHOLDER


def test_spec_mode_is_not_a_result_mode() -> None:
    """Input and output speak different mode vocabularies. Never merge them."""
    assert SPEC_MODE_DRY_RUN == "dry_run"
    assert SPEC_MODE_DRY_RUN not in RESULT_MODES
    assert "dry-run" in RESULT_MODES


# --- schema / validator agreement -------------------------------------------


def _schema(name: str) -> dict:
    return json.loads((_repo_root() / "schemas" / f"{name}.json").read_text())


def test_schemas_are_draft_2020_12_and_self_describing() -> None:
    for name in ("execution-spec-v1", "execution-event-v1", "execution-result-v1"):
        schema = _schema(name)
        assert schema["$schema"] == "https://json-schema.org/draft/2020-12/schema"
        assert schema["$id"].startswith("urn:cpanel-self-migration:schema:")
        assert schema["title"] == name
        assert schema["description"] and schema["type"] == "object"
        assert schema["required"] and schema["properties"]


def test_spec_schema_forbids_unknown_fields_everywhere() -> None:
    schema = _schema("execution-spec-v1")
    assert schema["additionalProperties"] is False
    assert schema["$defs"]["scope"]["additionalProperties"] is False


def test_output_schemas_tolerate_additive_fields() -> None:
    """The outputs must not set additionalProperties:false, or additive evolution breaks."""
    for name in ("execution-event-v1", "execution-result-v1"):
        assert "additionalProperties" not in _schema(name)


def test_schema_enums_match_the_validator_constants() -> None:
    """A schema that drifts from the code is worse than no schema."""
    event = _schema("execution-event-v1")
    assert set(event["properties"]["level"]["enum"]) == set(_LEVELS)
    assert set(event["properties"]["event"]["enum"]) == set(_EVENT_TYPES)
    # The empty phase is real: run-level events carry it.
    assert set(event["properties"]["phase"]["enum"]) == set(_PHASES) | {""}
    assert tuple(event["required"]) == _EVENT_REQUIRED

    result = _schema("execution-result-v1")
    assert set(result["properties"]["exit_status"]["enum"]) == set(_EXIT_STATUSES)
    assert set(result["properties"]["mode"]["enum"]) == set(RESULT_MODES)
    assert set(result["properties"]["phases_completed"]["items"]["enum"]) == set(_PHASES)
    assert tuple(result["required"]) == _RESULT_REQUIRED

    spec = _schema("execution-spec-v1")
    assert spec["properties"]["mode"]["const"] == SPEC_MODE_DRY_RUN
    assert spec["properties"]["format_version"]["const"] == CURRENT_FORMAT_VERSION
    assert set(spec["required"]) == set(_SPEC_TOP_KEYS)
    assert set(spec["$defs"]["scope"]["properties"]) == set(_SPEC_SCOPE_KEYS)


def test_spec_schema_declares_no_secret_and_no_workspace_field() -> None:
    """v1 carries references only. No credential, no path, no argv, no output_dir."""
    spec = _schema("execution-spec-v1")
    declared = set(spec["properties"]) | set(spec["$defs"]["scope"]["properties"])
    forbidden = {
        "output_dir", "host", "ip", "port", "ssh_user", "ssh_pass", "ssh_key_path",
        "password", "token", "credential_ref", "env", "argv", "command", "dns",
    }
    assert declared & forbidden == set()
    # And no declared field name may itself look like a secret.
    assert not [name for name in declared if _is_sensitive_key(name)]


# --- pydantic strictness ----------------------------------------------------


def test_scope_booleans_are_not_coerced_from_strings() -> None:
    """`{"mail": "true"}` must never become True."""
    with pytest.raises(ValidationError):
        SpecScope(mail="true", files=False, databases=False)  # type: ignore[arg-type]
    with pytest.raises(ValidationError):
        SpecScope(mail=1, files=False, databases=False)  # type: ignore[arg-type]


def test_spec_model_forbids_extra_fields() -> None:
    with pytest.raises(ValidationError):
        ExecutionSpec(
            format_version=1,
            run_id="run-x",
            plan_id=1,
            source_snapshot_id=2,
            destination_snapshot_id=3,
            comparison_report_id=4,
            mode="dry_run",
            scope=SpecScope(mail=True, files=False, databases=False),
            output_dir="/tmp/ws",  # type: ignore[call-arg]
        )


def test_spec_ids_are_not_coerced_from_strings() -> None:
    with pytest.raises(ValidationError):
        ExecutionSpec(
            format_version=1,
            run_id="run-x",
            plan_id="1",  # type: ignore[arg-type]
            source_snapshot_id=2,
            destination_snapshot_id=3,
            comparison_report_id=4,
            mode="dry_run",
            scope=SpecScope(mail=True, files=False, databases=False),
        )


# --- typed parse ------------------------------------------------------------


def test_parse_spec_returns_typed_values() -> None:
    spec = parse_spec((FIXTURE_ROOT / "valid" / "spec-mailbox.json").read_text())
    assert spec.format_version == CURRENT_FORMAT_VERSION
    assert spec.mode == SPEC_MODE_DRY_RUN
    assert (spec.plan_id, spec.comparison_report_id) == (1, 4)
    assert spec.scope.mail and spec.scope.files and not spec.scope.databases
    assert spec.scope.mailbox_filter == "user@example.com"


def test_valid_documents_round_trip_canonically() -> None:
    """Re-serializing a validated document must not change its meaning."""
    for name in ("event-run-started", "event-phase-completed", "event-redacted-nested"):
        raw = (FIXTURE_ROOT / "valid" / f"{name}.json").read_text()
        doc = json.loads(raw)
        again = json.dumps(doc)
        validate_event_json(again)
        assert json.loads(again) == doc

    for name in ("result-success", "result-interrupted"):
        raw = (FIXTURE_ROOT / "valid" / f"{name}.json").read_text()
        doc = json.loads(raw)
        again = json.dumps(doc)
        validate_result_json(again)
        assert json.loads(again) == doc


def test_parsed_spec_reserializes_and_revalidates() -> None:
    raw = (FIXTURE_ROOT / "valid" / "spec-mailbox.json").read_text()
    spec = parse_spec(raw)
    dumped = spec.model_dump(exclude_none=True)
    validate_spec_json(json.dumps(dumped))
    assert json.loads(raw) == dumped


# --- unit rules -------------------------------------------------------------


@pytest.mark.parametrize(
    "path", ["events.jsonl", "logs/migration_report.log", "a/b/c.txt", "./events.jsonl"]
)
def test_artifact_path_accepts_workspace_relative(path: str) -> None:
    assert _artifact_path_error(path) is None


@pytest.mark.parametrize(
    "path",
    [
        "",
        "/etc/shadow",
        "C:\\temp\\x",
        "C:/temp/x",
        "\\\\host\\share\\x",
        "../escape",
        "logs/../../etc/shadow",
        "logs\\..\\..\\system32",
        "a/\x00b",
    ],
)
def test_artifact_path_rejects_escapes(path: str) -> None:
    assert _artifact_path_error(path) is not None


def test_artifact_path_rejects_any_colon() -> None:
    """Drive letters and Windows alternate data streams, in any position.

    Rejecting the character (rather than checking index 1) is what keeps Go and
    Python identical: Go indexes bytes, Python indexes code points, so 'é:x'
    would otherwise be accepted by Go and rejected here.
    """
    for path in ("a:b", "C:/x", "é:x", "logs/a:b"):
        assert _artifact_path_error(path) is not None
    # A name that merely starts with dots is not a traversal.
    for path in ("..foo", "foo..bar", "...", "a/..b"):
        assert _artifact_path_error(path) is None


def test_integer_bounds_match_go_int64() -> None:
    """Python's int is unbounded; Go decodes into int64. Reject what Go cannot read."""
    doc = json.loads((FIXTURE_ROOT / "valid" / "spec-minimal.json").read_text())

    doc["plan_id"] = 2**63 - 1
    validate_spec_json(json.dumps(doc))  # at the limit: accepted

    doc["plan_id"] = 2**63
    with pytest.raises(ContractError, match="invalid field plan_id"):
        validate_spec_json(json.dumps(doc))


def test_run_id_length_is_measured_in_bytes() -> None:
    """events.ValidateRunID counts bytes. Counting code points accepts ids Go rejects."""
    doc = json.loads((FIXTURE_ROOT / "valid" / "event-run-started.json").read_text())

    doc["run_id"] = "é" * 64  # 128 bytes, 64 characters — at the limit
    validate_event_json(json.dumps(doc))

    doc["run_id"] = "é" * 65  # 130 bytes, but only 65 characters
    with pytest.raises(ContractError, match="invalid field run_id"):
        validate_event_json(json.dumps(doc))


def test_only_json_whitespace_is_stripped() -> None:
    """Python's bare strip() eats characters Go's decoder rejects."""
    raw = (FIXTURE_ROOT / "valid" / "event-run-started.json").read_text()
    validate_event_json("\n\t " + raw + " \r\n")  # the four real JSON whitespace chars
    # form feed, NBSP, LINE SEPARATOR: str.strip() eats them, Go's decoder does not.
    for exotic in ("\x0c", "\u00a0", "\u2028"):
        with pytest.raises(ContractError, match="invalid JSON"):
            validate_event_json(exotic + raw)
        with pytest.raises(ContractError, match="trailing JSON"):
            validate_event_json(raw.rstrip() + exotic)


def test_calendar_invalid_timestamps_raise_contract_error_not_valueerror() -> None:
    """The regex pins the shape, not the calendar. The contract promises ContractError."""
    doc = json.loads((FIXTURE_ROOT / "valid" / "event-run-started.json").read_text())
    for ts in ("2026-02-30T00:00:00Z", "2026-13-01T00:00:00Z", "0000-01-01T00:00:00Z"):
        doc["ts"] = ts
        with pytest.raises(ContractError, match="invalid field ts"):
            validate_event_json(json.dumps(doc))


def test_sub_microsecond_ordering_is_not_lost_to_truncation() -> None:
    """A report that finished 800ns before it started must be rejected here too.

    datetime tops out at microseconds; comparing truncated values would accept it
    while the Go validator, which keeps nanoseconds, rejects it.
    """
    doc = json.loads((FIXTURE_ROOT / "valid" / "result-success.json").read_text())
    doc["started_at"] = "2026-07-10T12:00:00.0000009Z"
    doc["finished_at"] = "2026-07-10T12:00:00.0000001Z"
    with pytest.raises(ContractError, match="finished_at is before started_at"):
        validate_result_json(json.dumps(doc))

    # Equal instants, and a legitimate nanosecond-forward step, still pass.
    doc["finished_at"] = "2026-07-10T12:00:00.0000009Z"
    validate_result_json(json.dumps(doc))
    doc["finished_at"] = "2026-07-10T12:00:00.000001Z"
    validate_result_json(json.dumps(doc))


def test_redacted_ok_mirrors_the_writer() -> None:
    for value in (None, "", REDACTED_PLACEHOLDER):
        assert _redacted_ok(value)
    # The writer replaces every non-empty value, including falsey non-strings.
    for value in ("plaintext", False, 0, {}, []):
        assert not _redacted_ok(value)


def test_error_messages_never_carry_the_secret_value() -> None:
    raw = (FIXTURE_ROOT / "invalid" / "event-unredacted-password.json").read_text()
    with pytest.raises(ContractError) as excinfo:
        validate_event_json(raw)
    message = str(excinfo.value)
    assert "password" in message  # names the key
    assert "hunter2" not in message  # never the value

    raw = (FIXTURE_ROOT / "invalid" / "event-unredacted-token-nested.json").read_text()
    with pytest.raises(ContractError) as excinfo:
        validate_event_json(raw)
    assert "abc123" not in str(excinfo.value)


def test_future_version_is_rejected_not_downgraded() -> None:
    doc = json.loads((FIXTURE_ROOT / "valid" / "spec-minimal.json").read_text())
    doc["format_version"] = CURRENT_FORMAT_VERSION + 1
    with pytest.raises(ContractError, match="unsupported format_version"):
        validate_spec_json(json.dumps(doc))


def test_zero_and_missing_version_are_rejected() -> None:
    doc = json.loads((FIXTURE_ROOT / "valid" / "spec-minimal.json").read_text())
    doc["format_version"] = 0
    with pytest.raises(ContractError, match="unsupported format_version"):
        validate_spec_json(json.dumps(doc))

    del doc["format_version"]
    with pytest.raises(ContractError, match="missing field: format_version"):
        validate_spec_json(json.dumps(doc))


def test_nanosecond_timestamps_from_go_are_accepted() -> None:
    """Go emits nanoseconds; datetime tops out at microseconds.

    A document the executor writes must not be one the platform refuses to read.
    """
    doc = json.loads((FIXTURE_ROOT / "valid" / "event-run-started.json").read_text())
    doc["ts"] = "2026-07-10T12:00:00.123456789Z"
    validate_event_json(json.dumps(doc))


def test_trailing_json_is_rejected() -> None:
    raw = (FIXTURE_ROOT / "valid" / "event-run-started.json").read_text()
    with pytest.raises(ContractError, match="trailing JSON"):
        validate_event_json(raw + "\n" + raw)
