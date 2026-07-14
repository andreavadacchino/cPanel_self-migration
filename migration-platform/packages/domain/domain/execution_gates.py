"""The gates that decide whether an execution may be created.

Pure: no I/O, no database, no network. The caller fetches the anchors; this
module decides, and when it refuses it says exactly why.

This is where the ADR's rule is cashed in — *"L'API ricalcola tutti i gate lato
server. La UI non è una barriera di sicurezza"* (docs/ADR_V2_GO_EXECUTOR.md).
The client sends a scope, never a verdict. Nothing here trusts anything the
client asserted about the plan being current or the scope being legal.

Two families, because they answer different questions and deserve different
answers:

``evaluate_state_gates``
    Is the plan still a truthful description of these servers? A plan is built
    from two snapshots and one comparison. A newer preflight or a newer
    comparison means the operator approved a picture of the servers as they no
    longer are. The server state forbids the request — HTTP 409.

``evaluate_scope_gates``
    Is this scope one the executor can actually run? Three combinations pass
    execution-spec-v1 and are then rejected by the engine at the Run boundary
    (``validateScopeCombos``, internal/migrate/runner.go). Without these gates
    the platform would build a spec, resolve credentials, dial two servers and
    only then fail. The request itself cannot be processed — HTTP 422.

The scope rules are duplicated from Go on purpose: the contract is the parser,
not the engine, and it does not know that a cPanel database is account-wide. The
duplication is the price of failing before the connection instead of after it; a
spec fed to the binary by hand is still caught by the engine, so this is an
earlier failure, never the only one.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Final, Mapping

__all__ = [
    "Anchors",
    "Gate",
    "PLAN_FAILED",
    "PLAN_STALE_COMPARISON",
    "PLAN_STALE_DESTINATION_SNAPSHOT",
    "PLAN_STALE_SOURCE_SNAPSHOT",
    "SCOPE_DOMAIN_FILTER_WITH_DATABASES",
    "SCOPE_MAILBOX_AND_DOMAIN_FILTER",
    "SCOPE_MAILBOX_FILTER_NOT_MAIL_ONLY",
    "evaluate_scope_gates",
    "evaluate_state_gates",
]


@dataclass(frozen=True)
class Gate:
    """One reason an execution may not be created.

    ``code`` is for the platform (stable, matchable); ``message`` is for the
    operator. Neither ever carries a secret — there is none to carry: gates see
    ids, statuses and a scope.
    """

    code: str
    message: str


@dataclass(frozen=True)
class Anchors:
    """The three ids a plan is anchored to: two snapshots and one comparison.

    ``None`` means "the platform cannot see one" — which is the absence of
    evidence of freshness, not evidence of it. It is treated as stale.
    """

    source_snapshot_id: int | None
    destination_snapshot_id: int | None
    comparison_report_id: int | None


PLAN_FAILED: Final = "plan_failed"
PLAN_STALE_SOURCE_SNAPSHOT: Final = "plan_stale_source_snapshot"
PLAN_STALE_DESTINATION_SNAPSHOT: Final = "plan_stale_destination_snapshot"
PLAN_STALE_COMPARISON: Final = "plan_stale_comparison"

SCOPE_BLANK_FILTER: Final = "scope_blank_filter"
SCOPE_DOMAIN_FILTER_WITH_DATABASES: Final = "scope_domain_filter_with_databases"
SCOPE_MAILBOX_FILTER_NOT_MAIL_ONLY: Final = "scope_mailbox_filter_not_mail_only"
SCOPE_MAILBOX_AND_DOMAIN_FILTER: Final = "scope_mailbox_and_domain_filter"

_STALE: Final = (
    (PLAN_STALE_SOURCE_SNAPSHOT, "source_snapshot_id", "source inventory snapshot"),
    (
        PLAN_STALE_DESTINATION_SNAPSHOT,
        "destination_snapshot_id",
        "destination inventory snapshot",
    ),
    (PLAN_STALE_COMPARISON, "comparison_report_id", "comparison report"),
)


def evaluate_state_gates(
    *, plan_status: str, plan_anchors: Anchors, latest: Anchors
) -> list[Gate]:
    """Why this plan may not be executed, given what the migration now holds.

    ``blocked`` is deliberately NOT a gate. A dry-run writes nothing, and it is
    how an operator investigates the blockers — refusing it would remove the
    tool for diagnosing the very state being complained about. The ADR blocks a
    ``blocked`` plan from an APPLY, and that gate belongs to the apply PR, where
    it will be reachable.

    Every stale anchor is reported, not only the first: an operator who has to
    re-run the preflight AND the comparison should learn that once.
    """
    gates: list[Gate] = []

    if plan_status == "failed":
        gates.append(
            Gate(
                PLAN_FAILED,
                "This migration plan failed to generate. Regenerate it before executing.",
            )
        )

    for code, field, label in _STALE:
        approved = getattr(plan_anchors, field)
        current = getattr(latest, field)
        if current is None or current != approved:
            gates.append(
                Gate(
                    code,
                    f"The plan was built from {label} {approved}, but the migration's "
                    f"current one is {current if current is not None else 'missing'}. "
                    "Regenerate the plan.",
                )
            )

    return gates


def evaluate_scope_gates(scope: Mapping[str, Any]) -> list[Gate]:
    """Scope combinations the executor refuses to run.

    Mirrors ``validateScopeCombos`` (internal/migrate/runner.go): the spec parser
    accepts these, the engine does not. Reported all at once, so a scope with two
    problems does not take two round trips to fix.
    """
    gates: list[Gate] = []
    mailbox = scope.get("mailbox_filter")
    domain = scope.get("domain_filter")

    # A blank filter is the most dangerous input this module sees, because it is
    # the one that FAILS OPEN. The spec accepts `"domain_filter": ""` — it checks
    # the type, not the content — and the engine maps it to `OnlyDomain: ""`,
    # where an empty string means NO FILTER. The run would silently cover the
    # whole account instead of the single domain the operator named: a scope
    # WIDER than the one approved, with nothing in the artifacts to say so.
    #
    # Refused, not normalised. Trimming it into absence would be the same silent
    # decision pointed the other way, and the operator would never learn their
    # request was empty.
    blank = [
        name
        for name, value in (("domain_filter", domain), ("mailbox_filter", mailbox))
        if isinstance(value, str) and not value.strip()
    ]
    if blank:
        gates.append(
            Gate(
                SCOPE_BLANK_FILTER,
                f"{' and '.join(blank)} is blank. A filter must name something: leave the "
                "field out to run without it — an empty one would silently widen the scope "
                "to the whole account.",
            )
        )
        # The remaining rules read these as booleans; a blank one would look like
        # "no filter" to them too, and reporting "your mailbox filter conflicts
        # with your domain filter" about two empty strings helps no one.
        mailbox = mailbox if not (isinstance(mailbox, str) and not mailbox.strip()) else None
        domain = domain if not (isinstance(domain, str) and not domain.strip()) else None

    if mailbox and domain:
        gates.append(
            Gate(
                SCOPE_MAILBOX_AND_DOMAIN_FILTER,
                "A mailbox filter and a domain filter are mutually exclusive: a mailbox "
                "already names its domain.",
            )
        )
    if mailbox and (scope.get("files") or scope.get("databases")):
        gates.append(
            Gate(
                SCOPE_MAILBOX_FILTER_NOT_MAIL_ONLY,
                "A mailbox filter is mail-only: remove the files and databases scope, or "
                "drop the filter.",
            )
        )
    if domain and scope.get("databases"):
        gates.append(
            Gate(
                SCOPE_DOMAIN_FILTER_WITH_DATABASES,
                "A domain filter does not support databases: cPanel databases are "
                "account-wide, not per-domain. Drop one of the two.",
            )
        )

    return gates
