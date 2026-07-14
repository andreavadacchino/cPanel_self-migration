"""The gates that decide whether an execution may be created.

They are pure, so they are tested pure: no database, no HTTP, no fixtures. The
API layer's job is only to fetch the anchors and hand them over.
"""

from __future__ import annotations

import pytest

from domain.execution_gates import (
    Anchors,
    evaluate_scope_gates,
    evaluate_state_gates,
)

FRESH = Anchors(source_snapshot_id=7, destination_snapshot_id=8, comparison_report_id=3)


def _codes(gates) -> set[str]:
    return {g.code for g in gates}


# --- state gates: is this plan still a truthful description of the servers? ---


def test_a_fresh_ready_plan_has_no_state_gate() -> None:
    assert evaluate_state_gates(plan_status="ready_for_review", plan_anchors=FRESH, latest=FRESH) == []


def test_a_failed_plan_is_never_executable() -> None:
    gates = evaluate_state_gates(plan_status="failed", plan_anchors=FRESH, latest=FRESH)
    assert _codes(gates) == {"plan_failed"}


def test_a_blocked_plan_still_allows_a_dry_run() -> None:
    """A dry-run writes nothing, and it is how an operator investigates a blocked
    plan. The ADR blocks `blocked` plans from APPLY, not from a dry-run; blocking
    it here would take away the tool for diagnosing the blockers."""
    assert evaluate_state_gates(plan_status="blocked", plan_anchors=FRESH, latest=FRESH) == []


@pytest.mark.parametrize(
    ("field", "code"),
    [
        ("source_snapshot_id", "plan_stale_source_snapshot"),
        ("destination_snapshot_id", "plan_stale_destination_snapshot"),
        ("comparison_report_id", "plan_stale_comparison"),
    ],
)
def test_a_newer_anchor_makes_the_plan_stale(field: str, code: str) -> None:
    """A new preflight or a new comparison means the operator approved a plan that
    describes servers as they no longer are. Refuse; do not silently execute the
    old one."""
    newer = Anchors(**{**FRESH.__dict__, field: getattr(FRESH, field) + 1})
    gates = evaluate_state_gates(plan_status="ready_for_review", plan_anchors=FRESH, latest=newer)
    assert _codes(gates) == {code}
    assert code.replace("_", " ") or gates[0].message  # a gate always explains itself
    assert gates[0].message


def test_every_stale_anchor_is_reported_not_just_the_first() -> None:
    newer = Anchors(source_snapshot_id=9, destination_snapshot_id=10, comparison_report_id=4)
    gates = evaluate_state_gates(plan_status="ready_for_review", plan_anchors=FRESH, latest=newer)
    assert _codes(gates) == {
        "plan_stale_source_snapshot",
        "plan_stale_destination_snapshot",
        "plan_stale_comparison",
    }


def test_a_missing_latest_anchor_is_stale_not_fresh() -> None:
    """`None` means the platform cannot see the snapshot the plan claims to be
    built on. That is not evidence of freshness — it is the absence of evidence,
    and the answer is to refuse."""
    gates = evaluate_state_gates(
        plan_status="ready_for_review",
        plan_anchors=FRESH,
        latest=Anchors(source_snapshot_id=None, destination_snapshot_id=8, comparison_report_id=3),
    )
    assert _codes(gates) == {"plan_stale_source_snapshot"}


# --- scope gates: combinations the EXECUTOR rejects at run time ---
#
# execution-spec-v1 accepts all three of these; internal/migrate.validateScopeCombos
# (runner.go) rejects them at the Run boundary. Without these gates the platform
# would happily build a spec, resolve credentials, dial two servers and only THEN
# fail. Refuse before the row is even created.


def test_a_plain_scope_passes() -> None:
    assert evaluate_scope_gates({"mail": True, "files": True, "databases": True}) == []


def test_domain_filter_with_databases_is_refused() -> None:
    gates = evaluate_scope_gates(
        {"mail": True, "files": False, "databases": True, "domain_filter": "example.com"}
    )
    assert _codes(gates) == {"scope_domain_filter_with_databases"}


def test_mailbox_filter_with_files_is_refused() -> None:
    gates = evaluate_scope_gates(
        {"mail": True, "files": True, "databases": False, "mailbox_filter": "bob@example.com"}
    )
    assert _codes(gates) == {"scope_mailbox_filter_not_mail_only"}


def test_mailbox_filter_with_databases_is_refused() -> None:
    gates = evaluate_scope_gates(
        {"mail": True, "files": False, "databases": True, "mailbox_filter": "bob@example.com"}
    )
    assert _codes(gates) == {"scope_mailbox_filter_not_mail_only"}


def test_mailbox_filter_and_domain_filter_are_mutually_exclusive() -> None:
    gates = evaluate_scope_gates(
        {
            "mail": True,
            "files": False,
            "databases": False,
            "mailbox_filter": "bob@example.com",
            "domain_filter": "example.com",
        }
    )
    assert _codes(gates) == {"scope_mailbox_and_domain_filter"}


@pytest.mark.parametrize("blank", ["", "   ", "\t"])
@pytest.mark.parametrize("field", ["domain_filter", "mailbox_filter"])
def test_a_blank_filter_is_refused_because_the_executor_reads_it_as_no_filter(
    field: str, blank: str
) -> None:
    """The worst kind of bug this module exists to stop.

    execution-spec-v1 accepts ``"domain_filter": ""`` — it only checks the type.
    The engine maps it to ``OnlyDomain: ""``, and an empty string there means NO
    filter: the run silently covers the whole account instead of the one domain
    the operator named. The scope would be WIDER than the one that was approved,
    and nothing in the artifacts would say so.

    A filter that was not chosen is absent, not empty. Blank is a request the
    platform refuses rather than reinterprets — normalising it away would be the
    same silent decision in the other direction.
    """
    scope = {"mail": True, "files": False, "databases": False, field: blank}
    assert _codes(evaluate_scope_gates(scope)) == {"scope_blank_filter"}


def test_an_absent_filter_is_not_a_blank_one() -> None:
    assert evaluate_scope_gates({"mail": True, "files": False, "databases": False}) == []
    assert (
        evaluate_scope_gates(
            {
                "mail": True,
                "files": False,
                "databases": False,
                "domain_filter": None,
                "mailbox_filter": None,
            }
        )
        == []
    )


def test_a_mail_only_mailbox_scope_passes() -> None:
    assert (
        evaluate_scope_gates(
            {"mail": True, "files": False, "databases": False, "mailbox_filter": "bob@example.com"}
        )
        == []
    )


def test_a_domain_scope_over_mail_and_files_passes() -> None:
    assert (
        evaluate_scope_gates(
            {"mail": True, "files": True, "databases": False, "domain_filter": "example.com"}
        )
        == []
    )
