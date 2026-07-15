"""R2-b2: strict boolean validation of a dispatch ``completed`` flag.

A ``completed`` signal that decides whether a phase may terminalise must be a REAL
boolean — a truthy string/int/list is a malformed payload, not a completion. Invalid
input raises a machine reason code and yields no dispatch.
"""
from __future__ import annotations

import pytest

from app.core.errors import ConflictError
from app.modules.executions.dispatch_validation import validate_completed_flag


@pytest.mark.parametrize("value", [True, False])
def test_real_bool_accepted(value):
    validate_completed_flag({"completed": value})  # no raise


def test_missing_key_rejected():
    with pytest.raises(ConflictError, match="completed_missing"):
        validate_completed_flag({})


@pytest.mark.parametrize("value", [1, 0, "true", "false", "", "1", [], [True], {}, {"x": 1}, None, 1.0])
def test_non_bool_rejected(value):
    # Critically: int 1/0 and truthy strings/lists are NOT booleans.
    with pytest.raises(ConflictError, match="completed_not_bool"):
        validate_completed_flag({"completed": value})


def test_payload_not_dict_rejected():
    with pytest.raises(ConflictError, match="completed_payload_invalid"):
        validate_completed_flag([("completed", True)])


def test_optional_absence_allowed_when_not_required():
    validate_completed_flag({}, required=False)  # no raise when the flag is optional
    with pytest.raises(ConflictError, match="completed_not_bool"):
        validate_completed_flag({"completed": "yes"}, required=False)  # but a present one must be bool
