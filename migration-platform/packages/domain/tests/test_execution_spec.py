"""The spec builder must produce documents the contract accepts, and only those."""

from __future__ import annotations

import hashlib
import json

import pytest

from domain.execution_contract import ContractError, validate_spec_json
from domain.execution_spec import (
    SPEC_VERSION,
    build_execution_spec,
    canonical_spec_bytes,
    spec_sha256,
)

ANCHORS = {
    "plan_id": 12,
    "source_snapshot_id": 34,
    "destination_snapshot_id": 35,
    "comparison_report_id": 56,
}


def _spec(**over):
    kwargs = {
        "run_id": "run-20260710-120000",
        **ANCHORS,
        "mail": True,
        "files": False,
        "databases": False,
    }
    kwargs.update(over)
    return build_execution_spec(**kwargs)


def test_built_spec_validates_against_the_contract() -> None:
    spec = _spec()
    validate_spec_json(canonical_spec_bytes(spec))
    assert spec["format_version"] == SPEC_VERSION
    assert spec["mode"] == "dry_run"
    assert spec["scope"] == {"mail": True, "files": False, "databases": False}


def test_anchors_are_carried_verbatim() -> None:
    spec = _spec()
    for key, value in ANCHORS.items():
        assert spec[key] == value


def test_absent_filters_are_omitted_not_null() -> None:
    """v1 forbids unknown fields and gives the filters no null form."""
    spec = _spec()
    assert "domain_filter" not in spec["scope"]
    assert "mailbox_filter" not in spec["scope"]


def test_filters_are_carried_when_given() -> None:
    spec = _spec(files=True, domain_filter="example.com", mailbox_filter="u@example.com")
    assert spec["scope"]["domain_filter"] == "example.com"
    assert spec["scope"]["mailbox_filter"] == "u@example.com"


def test_empty_scope_is_refused_by_the_contract() -> None:
    with pytest.raises(ContractError, match="at least one of mail, files, databases"):
        _spec(mail=False, files=False, databases=False)


def test_mailbox_filter_without_mail_is_refused() -> None:
    with pytest.raises(ContractError, match="invalid field mailbox_filter"):
        _spec(mail=False, databases=True, mailbox_filter="u@example.com")


@pytest.mark.parametrize("blank", ["", " ", "   ", "\t", "\n", " \t \n "])
def test_a_blank_domain_filter_can_never_become_a_whole_account_run(blank: str) -> None:
    """The mutation test the whole PR exists for.

    An empty domain_filter reaches the executor as ``OnlyDomain: ""``, which it
    reads as NO filter — the run covers the entire account instead of the one
    domain the operator named. The builder must refuse it outright, so no such
    spec can ever be hashed into an execution and handed to the binary. Refused,
    not trimmed to absence: an operator who typed spaces asked for a domain, not
    for "everything".
    """
    with pytest.raises(ContractError, match="domain_filter: must not be blank when present"):
        _spec(files=True, domain_filter=blank)


@pytest.mark.parametrize("blank", ["", " ", "\t\t", "\r\n"])
def test_a_blank_mailbox_filter_is_refused(blank: str) -> None:
    with pytest.raises(ContractError, match="mailbox_filter: must not be blank when present"):
        _spec(mailbox_filter=blank)


def test_a_present_nonblank_filter_still_passes() -> None:
    """The bound must not reject a real name: parity check for the negative tests."""
    spec = _spec(files=True, domain_filter="a.example.com", mailbox_filter="u@example.com")
    validate_spec_json(canonical_spec_bytes(spec))
    assert spec["scope"]["domain_filter"] == "a.example.com"


def test_invalid_run_id_is_refused() -> None:
    with pytest.raises(ContractError, match="invalid field run_id"):
        _spec(run_id="run/../x")


def test_non_positive_anchor_is_refused() -> None:
    with pytest.raises(ContractError, match="invalid field plan_id"):
        _spec(plan_id=0)


def test_builder_is_keyword_only() -> None:
    """Four consecutive integer ids are exactly what a caller transposes."""
    with pytest.raises(TypeError):
        build_execution_spec("run-x", 1, 2, 3, 4, True, False, False)  # type: ignore[misc]


# --- anchoring --------------------------------------------------------------


def test_canonical_bytes_are_stable_and_key_order_independent() -> None:
    spec = _spec()
    shuffled = dict(reversed(list(spec.items())))
    assert canonical_spec_bytes(spec) == canonical_spec_bytes(shuffled)
    assert b" " not in canonical_spec_bytes(spec)  # no insignificant whitespace


def test_canonical_bytes_are_utf8_not_escaped() -> None:
    """\\uXXXX escapes would make the bytes depend on the writer."""
    spec = _spec(files=True, domain_filter="exämple.com")
    raw = canonical_spec_bytes(spec)
    assert "exämple.com".encode() in raw
    assert b"\\u" not in raw


def test_sha256_is_over_the_exact_bytes() -> None:
    spec = _spec()
    raw = canonical_spec_bytes(spec)
    assert spec_sha256(raw) == hashlib.sha256(raw).hexdigest()
    assert len(spec_sha256(raw)) == 64


def test_sha256_changes_when_any_anchor_changes() -> None:
    """An execution anchored to a different plan must not share a digest."""
    base = spec_sha256(canonical_spec_bytes(_spec()))
    for key in ANCHORS:
        other = spec_sha256(canonical_spec_bytes(_spec(**{key: ANCHORS[key] + 1})))
        assert other != base, f"{key} does not affect the digest"


def test_sha256_changes_when_scope_changes() -> None:
    base = spec_sha256(canonical_spec_bytes(_spec()))
    wider = spec_sha256(canonical_spec_bytes(_spec(files=True)))
    assert wider != base


def test_digest_of_a_reserialized_spec_matches() -> None:
    """Round-tripping through JSON must not move the anchor."""
    spec = _spec()
    raw = canonical_spec_bytes(spec)
    assert spec_sha256(canonical_spec_bytes(json.loads(raw))) == spec_sha256(raw)
