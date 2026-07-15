"""R2-c2: pure conservative email recovery classifier.

Policy: live == desired does NOT prove ownership — without provider-side CAS/version/
audit there is no automatic reverse after a crash. The classifier automates only a
safe re-apply (a create/overwrite that provably never landed); everything else is
manual, and no action is ever a delete or an automatic restore.
"""
from __future__ import annotations

import pytest

from app.modules.executions.email_recovery import (
    ACTION_MANUAL,
    ACTION_RECORD_MANUAL,
    ACTION_SAFE_RETRY,
    ACTION_SKIP,
    LIVE_ABSENT,
    LIVE_DIVERGENT,
    LIVE_EQUALS_DESIRED,
    LIVE_EQUALS_PREVIOUS,
    LIVE_PRESENT,
    EmailRecoveryDecision,
    classify,
)
from app.modules.executions.models import EmailWriteStatus as S


# -- additive (no reverse op; never delete) ----------------------------------

@pytest.mark.parametrize("status", [S.planned.value, S.side_effect_started.value])
def test_additive_absent_is_safe_retry(status):
    d = classify("additive_create", status, live_state=LIVE_ABSENT, absence_stable=True, fencing_valid=True)
    assert d == EmailRecoveryDecision(ACTION_SAFE_RETRY, "email_recovery_safe_retry")


def test_additive_started_present_is_manual_removal():
    d = classify("additive_create", S.side_effect_started.value, live_state=LIVE_PRESENT,
                 absence_stable=True, fencing_valid=True)
    assert d == EmailRecoveryDecision(ACTION_RECORD_MANUAL, "email_recovery_applied_manual_removal")


def test_additive_planned_present_is_ownership_unknown():
    d = classify("additive_create", S.planned.value, live_state=LIVE_PRESENT,
                 absence_stable=True, fencing_valid=True)
    assert d == EmailRecoveryDecision(ACTION_MANUAL, "email_recovery_present_ownership_unknown")


# -- overwrite (durable previous backup; NEVER an automatic reverse) ----------

def test_overwrite_equals_previous_stable_is_safe_retry():
    d = classify("overwrite", S.side_effect_started.value, live_state=LIVE_EQUALS_PREVIOUS,
                 absence_stable=True, fencing_valid=True)
    assert d == EmailRecoveryDecision(ACTION_SAFE_RETRY, "email_recovery_safe_retry")


@pytest.mark.parametrize("stable,fencing", [(False, True), (True, False)])
def test_overwrite_equals_previous_unstable_or_unfenced_is_manual(stable, fencing):
    d = classify("overwrite", S.side_effect_started.value, live_state=LIVE_EQUALS_PREVIOUS,
                 absence_stable=stable, fencing_valid=fencing)
    assert d.action == ACTION_MANUAL and d.reason == "email_recovery_previous_not_stable"


def test_overwrite_equals_desired_is_applied_or_external_ambiguous():
    d = classify("overwrite", S.side_effect_started.value, live_state=LIVE_EQUALS_DESIRED,
                 absence_stable=True, fencing_valid=True)
    # live == desired never proves ownership -> manual plan with the previous backup.
    assert d == EmailRecoveryDecision(ACTION_RECORD_MANUAL, "email_recovery_applied_or_external_ambiguous")


def test_overwrite_divergent_is_manual_reconciliation():
    d = classify("overwrite", S.side_effect_started.value, live_state=LIVE_DIVERGENT,
                 absence_stable=True, fencing_valid=True)
    assert d == EmailRecoveryDecision(ACTION_MANUAL, "email_recovery_manual_reconciliation")


# -- terminal / safety -------------------------------------------------------

def test_reconciliation_required_is_manual():
    d = classify("additive_create", S.reconciliation_required.value, live_state=LIVE_PRESENT,
                 absence_stable=True, fencing_valid=True)
    assert d.action == ACTION_MANUAL


def test_compensated_is_skipped():
    d = classify("overwrite", S.compensated.value, live_state=LIVE_EQUALS_PREVIOUS,
                 absence_stable=True, fencing_valid=True)
    assert d.action == ACTION_SKIP


def test_no_action_ever_deletes_or_auto_restores():
    for op in ("additive_create", "overwrite"):
        for status in [s.value for s in S]:
            for live in (LIVE_ABSENT, LIVE_PRESENT, LIVE_EQUALS_PREVIOUS, LIVE_EQUALS_DESIRED, LIVE_DIVERGENT):
                d = classify(op, status, live_state=live, absence_stable=True, fencing_valid=True)
                assert d.action in {ACTION_SAFE_RETRY, ACTION_RECORD_MANUAL, ACTION_MANUAL, ACTION_SKIP}
                assert "delete" not in d.action and "restore" not in d.action
