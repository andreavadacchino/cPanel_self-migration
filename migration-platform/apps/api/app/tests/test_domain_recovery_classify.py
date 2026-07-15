"""R2-b2: pure recovery classifier — the decision table, no DB, no gateway.

fence-gone is a PRECONDITION (the caller has claimed the recovery lease before
classifying); `previous_fence_still_active` is the caller's concern, not the
classifier's. The classifier never returns an action that deletes a domain.
"""
from __future__ import annotations

import pytest

from app.modules.executions.domain_recovery import (
    ACTION_MANUAL,
    ACTION_RECORD_MANUAL,
    ACTION_SAFE_RETRY,
    ACTION_SKIP,
    RecoveryDecision,
    classify,
)
from app.modules.executions.models import DomainWriteStatus as S


@pytest.mark.parametrize("present,stable,expected", [
    (False, True, RecoveryDecision(ACTION_SAFE_RETRY, "domain_recovery_safe_retry")),
    (False, False, RecoveryDecision(ACTION_SAFE_RETRY, "domain_recovery_safe_retry")),
    (True, True, RecoveryDecision(ACTION_MANUAL, "domain_recovery_target_present_ownership_unknown")),
])
def test_planned(present, stable, expected):
    # planned: the create was never issued (mark_started gates it), so absence is
    # safe to retry regardless of stability; presence is never assumed ours.
    assert classify(S.planned.value, target_present=present, absence_stable=stable) == expected


@pytest.mark.parametrize("present,stable,expected", [
    (True, True, RecoveryDecision(ACTION_MANUAL, "domain_recovery_target_present_ownership_unknown")),
    (True, False, RecoveryDecision(ACTION_MANUAL, "domain_recovery_target_present_ownership_unknown")),
    (False, False, RecoveryDecision(ACTION_MANUAL, "domain_recovery_absence_not_stable")),
    (False, True, RecoveryDecision(ACTION_SAFE_RETRY, "domain_recovery_safe_retry")),
])
def test_side_effect_started(present, stable, expected):
    # started: the create WAS issued; an in-flight old call could still land, so
    # absence must be stable before a retry, and presence is never assumed ours.
    assert classify(S.side_effect_started.value, target_present=present, absence_stable=stable) == expected


def test_applied_is_manual_removal_never_delete():
    d = classify(S.applied.value, target_present=True, absence_stable=True)
    assert d == RecoveryDecision(ACTION_RECORD_MANUAL, "domain_recovery_applied_confirmed")


def test_reconciliation_required_is_manual():
    d = classify(S.reconciliation_required.value, target_present=False, absence_stable=True)
    assert d == RecoveryDecision(ACTION_MANUAL, "domain_recovery_manual_intervention_required")


def test_compensation_states_are_manual_not_driven():
    assert classify(S.compensation_started.value, target_present=True, absence_stable=True) == \
        RecoveryDecision(ACTION_MANUAL, "domain_recovery_compensation_resumed")
    assert classify(S.compensation_failed.value, target_present=True, absence_stable=True) == \
        RecoveryDecision(ACTION_MANUAL, "domain_recovery_compensation_failed")


def test_compensated_is_skipped():
    assert classify(S.compensated.value, target_present=True, absence_stable=True).action == ACTION_SKIP


def test_no_action_ever_deletes():
    # Exhaustive: whatever the state, the classifier never emits a destructive action.
    for status in [s.value for s in S]:
        for present in (True, False):
            for stable in (True, False):
                d = classify(status, target_present=present, absence_stable=stable)
                assert d.action in {ACTION_SAFE_RETRY, ACTION_RECORD_MANUAL, ACTION_MANUAL, ACTION_SKIP}
                assert "delete" not in d.action and "remove" not in d.action
