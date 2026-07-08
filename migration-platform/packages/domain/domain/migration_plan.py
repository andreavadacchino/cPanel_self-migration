"""Pure read-only Migration Plan builder.

Given the normalized source/destination inventories and the comparison output,
produce a classified, **descriptive** plan: what is aligned, what is missing,
what blocks, what needs manual work, what is unknown because it could not be
read, and what must not be automated yet.

It executes nothing. It is a projection of the current read state: severities
are inherited from the comparison (the single source of truth) and routed into
sections — the plan never invents a new blocker, and a category that could not
be read becomes an *unknown*, never a fabricated blocker.

Infrastructure-free: no DB, no network, no FastAPI, no cPanel, no secrets. Each
section item carries only a natural ``key`` and human text, never the raw item
nor a token/password.
"""

from __future__ import annotations

from dataclasses import dataclass

# Comparison entry categories that are read-gaps (a capability/coverage signal),
# not real data deltas → always routed to *unknown*, never a blocker.
_UNKNOWN_CATEGORIES = frozenset({"capabilities", "coverage"})

# Data categories whose changes are not automatable and need manual handling; a
# non-blocker comparison delta here becomes a manual task.
_MANUAL_CATEGORIES = frozenset({"dns_records", "cron_jobs", "ssl"})

# Categories the inventory reads but the comparison never diffs item-by-item
# (Sprint 3.5 keeps them only in the coverage matrix). If present and readable on
# the source, they are manual tasks — never auto-migrated.
_INVENTORY_MANUAL_CATEGORIES: tuple[tuple[str, str], ...] = (
    ("email_forwarders", "Inoltri email"),
    ("email_autoresponders", "Risponditori automatici"),
    ("ftp_accounts", "Account FTP"),
)

# List categories eligible for a descriptive "aligned" ready-step.
_READY_STEP_CATEGORIES: tuple[str, ...] = (
    "domains", "email_accounts", "databases", "mysql_users",
    "cron_jobs", "ssl", "dns_records",
)

_READABLE_COVERAGE = frozenset({"succeeded", "empty"})

_CATEGORY_LABEL = {
    "domains": "Domini",
    "email_accounts": "Account email",
    "databases": "Database",
    "mysql_users": "Utenti MySQL",
    "cron_jobs": "Cron job",
    "ssl": "Certificati SSL",
    "dns_records": "Record DNS",
    "email_forwarders": "Inoltri email",
    "email_autoresponders": "Risponditori automatici",
    "ftp_accounts": "Account FTP",
}


@dataclass(frozen=True)
class MigrationPlanOutput:
    status: str
    summary: dict
    sections: dict


def _plan_item(entry: dict) -> dict:
    """Descriptive projection of a comparison entry: only the natural key and
    human text — never the raw item nor the opaque fingerprint side-objects."""
    return {
        "category": entry.get("category"),
        "key": entry.get("key"),
        "state": entry.get("state"),
        "severity": entry.get("severity"),
        "title": entry.get("title"),
        "message": entry.get("message"),
    }


def _coverage_status(coverage: object, key: str) -> str | None:
    if not isinstance(coverage, dict):
        return None
    entry = coverage.get(key)
    return entry.get("status") if isinstance(entry, dict) else None


def _inventory_manual_tasks(source_inventory: dict) -> list[dict]:
    coverage = source_inventory.get("coverage") or {}
    out: list[dict] = []
    for category, label in _INVENTORY_MANUAL_CATEGORIES:
        items = source_inventory.get(category) or []
        readable = _coverage_status(coverage, category) in _READABLE_COVERAGE
        if readable and items:
            out.append(
                {
                    "category": category,
                    "key": category,
                    "state": None,
                    "severity": "warning",
                    "title": f"{label}: intervento manuale",
                    "message": (
                        f"{len(items)} elemento/i presenti sul source non sono "
                        "confrontati e non vengono migrati automaticamente: "
                        "vanno ricreati/verificati manualmente."
                    ),
                }
            )
    return out


def _ready_steps(summary: dict) -> list[dict]:
    by_category = (summary or {}).get("by_category") or {}
    out: list[dict] = []
    for category in _READY_STEP_CATEGORIES:
        stats = by_category.get(category)
        if not isinstance(stats, dict):
            continue
        match = stats.get("match") or 0
        if match > 0:
            label = _CATEGORY_LABEL.get(category, category)
            out.append(
                {
                    "category": category,
                    "key": category,
                    "title": f"{label}: allineati",
                    "message": (
                        f"{match} {label.lower()} risultano allineati tra "
                        "source e destination."
                    ),
                }
            )
    return out


def _cutover_notes(source_inventory: dict) -> list[dict]:
    notes = [
        {
            "category": "cutover",
            "key": "readonly",
            "title": "Piano read-only",
            "message": (
                "Questo piano è read-only. Non esegue modifiche sui server."
            ),
        }
    ]
    coverage = source_inventory.get("coverage") or {}
    if source_inventory.get("dns_records") and (
        _coverage_status(coverage, "dns_records") in _READABLE_COVERAGE
    ):
        notes.append(
            {
                "category": "cutover",
                "key": "dns",
                "title": "DNS al cutover",
                "message": (
                    "I record DNS vanno ripuntati al cutover; la scrittura DNS "
                    "non è automatizzata da questo piano."
                ),
            }
        )
    if source_inventory.get("ssl"):
        notes.append(
            {
                "category": "cutover",
                "key": "ssl",
                "title": "SSL al cutover",
                "message": (
                    "I certificati SSL possono richiedere re-issue/verifica "
                    "sulla destinazione dopo il cutover."
                ),
            }
        )
    return notes


def build_migration_plan(
    source_inventory: dict,
    destination_inventory: dict,
    comparison: dict,
) -> MigrationPlanOutput:
    """Route the comparison's own classifications into an operator-facing plan.

    Severity is never re-invented: a comparison blocker is a plan blocker; a
    read-gap (capabilities/coverage) is an unknown, never a blocker; a
    non-blocker delta in a non-automatable category is a manual task.
    """
    source_inventory = source_inventory or {}
    comparison = comparison or {}
    entries = comparison.get("entries") or []
    summary = comparison.get("summary") or {}

    blockers: list[dict] = []
    warnings: list[dict] = []
    manual_tasks: list[dict] = []
    unknowns: list[dict] = []

    for entry in entries:
        category = entry.get("category")
        severity = entry.get("severity")
        if category in _UNKNOWN_CATEGORIES:
            unknowns.append(_plan_item(entry))
        elif severity == "blocker":
            blockers.append(_plan_item(entry))
        elif category in _MANUAL_CATEGORIES:
            manual_tasks.append(_plan_item(entry))
        else:
            warnings.append(_plan_item(entry))

    manual_tasks.extend(_inventory_manual_tasks(source_inventory))
    ready_steps = _ready_steps(summary)
    cutover_notes = _cutover_notes(source_inventory)

    status = "blocked" if blockers else "ready_for_review"
    plan_summary = {
        "blockers_count": len(blockers),
        "warnings_count": len(warnings),
        "manual_tasks_count": len(manual_tasks),
        "unknowns_count": len(unknowns),
        "ready_steps_count": len(ready_steps),
    }
    sections = {
        "blockers": blockers,
        "manual_tasks": manual_tasks,
        "warnings": warnings,
        "unknowns": unknowns,
        "ready_steps": ready_steps,
        "cutover_notes": cutover_notes,
    }
    return MigrationPlanOutput(
        status=status, summary=plan_summary, sections=sections
    )
