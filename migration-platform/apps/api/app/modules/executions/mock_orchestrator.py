"""Orchestrazione mock end-to-end dei writer.

Coordina i writer mock in un unico execution run non dry-run, nell'ordine
deterministico definito in :mod:`app.modules.executions.phase`. L'orchestratore:

* pre-valida run, coerenza piano/comparazione/snapshot, modalità dei passi,
  password cifrate e dipendenze PRIMA di eseguire qualsiasi fase (un errore di
  pre-validazione non muta il run e non esegue nulla);
* invoca il contratto di fase condiviso di ogni writer (``validate_phase`` +
  ``apply_phase``) senza forzare ripetutamente ``run.status=queued`` e senza
  aggirare i guardrail di sicurezza dei writer;
* possiede lo stato terminale del run: nessuna singola fase marca il run
  ``succeeded`` mentre restano fasi da eseguire;
* al primo blocco o fallimento ferma le categorie successive, porta il run a
  ``failed`` e registra quali passi sono riusciti e quali non sono stati
  eseguiti; non compensa né cancella automaticamente le risorse simulate;
* al termine costruisce una verifica finale rileggendo lo STATO MOCK CONDIVISO
  ricostruito dagli eventi immutabili (non dai risultati restituiti dai writer).

Il flag ``MOCK_ORCHESTRATOR_MODE`` (default ``disabled``) gatea questo percorso;
i flag per-writer gateano invece il percorso standalone di ciascun ``execute``.
Nessuna route/UI accoda l'orchestratore in questo incremento.

Percorso reale futuro: le fasi mock e la rilettura dello stato mock saranno
sostituite da scritture account-level reali seguite da un nuovo preflight e una
nuova comparazione della destinazione.
"""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import (
    autoresponder_writer,
    cron_writer,
    database_writer,
    dns_writer,
    domain_writer,
    forwarder_writer,
    ftp_writer,
    mailing_list_writer,
    mysql_user_writer,
)
from app.modules.executions.models import ExecutionEvent, ExecutionRun
from app.modules.executions.phase import CATEGORY_ORDER, MockDestinationState, PhaseOutcome
from app.modules.plans.models import MigrationPlan

# Registro categoria → modulo writer che espone validate_phase/apply_phase.
PHASES = {
    "domains": domain_writer,
    "databases": database_writer,
    "mysql_users": mysql_user_writer,
    "email_forwarders": forwarder_writer,
    "cron_jobs": cron_writer,
    "ftp_accounts": ftp_writer,
    "mailing_lists": mailing_list_writer,
    "dns_records": dns_writer,
    "email_autoresponders": autoresponder_writer,
}

SECRET_CATEGORIES = {"ftp_accounts", "mailing_lists", "mysql_users"}

# Attributo settings del flag per-writer, per categoria. L'orchestratore non
# richiede che sia `mock` (è gateato da MOCK_ORCHESTRATOR_MODE + endpoint mock),
# ma rifiuta esplicitamente il valore `real`: un writer reale non è implementato
# e non deve mai essere eseguito, nemmeno passando per l'orchestratore.
WRITER_MODE_ATTR = {
    "domains": "domain_writer_mode",
    "databases": "database_writer_mode",
    "mysql_users": "mysql_user_writer_mode",
    "email_forwarders": "forwarder_writer_mode",
    "cron_jobs": "cron_writer_mode",
    "ftp_accounts": "ftp_writer_mode",
    "mailing_lists": "mailing_list_writer_mode",
    "dns_records": "dns_writer_mode",
    "email_autoresponders": "autoresponder_writer_mode",
}


def _prevalidate(db: Session, run: ExecutionRun) -> dict[str, list[dict]]:
    """Pre-validazione strutturale prima dell'avvio di qualsiasi fase."""
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        raise ConflictError("Target non valido: il writer accetta soltanto la destinazione")
    if destination.auth_type != "mock":
        raise ConflictError("L'orchestratore reale non è implementato né abilitato")

    plan = db.get(MigrationPlan, run.plan_id)
    if plan is None or plan.comparison_report_id != run.comparison_report_id:
        raise ConflictError("Piano incoerente con il run")
    report = db.get(ComparisonReport, run.comparison_report_id)
    if report is None or report.source_snapshot_id != run.source_snapshot_id or report.destination_snapshot_id != run.destination_snapshot_id:
        raise ConflictError("Comparazione o snapshot incoerenti con il run")

    plan_steps = {step["id"]: step for step in plan.steps}
    by_category: dict[str, list[dict]] = {}
    for item in run.preview:
        category = item.get("category")
        if category not in CATEGORY_ORDER:
            raise ConflictError(f"Categoria non orchestrabile: {category}")
        step = plan_steps.get(item["step_id"], {})
        # La categoria autoritativa è quella del piano: la preview non deve
        # instradare un passo verso un writer diverso da quello pianificato.
        if step and step.get("category") != category:
            raise ConflictError(
                f"Categoria incoerente per {item['step_id']}: preview={category}, piano={step.get('category')}"
            )
        mode = step.get("mode")
        if mode in {"manual", "excluded"} or mode is None:
            raise ConflictError(f"Passo non automatizzabile ({mode}): {item['step_id']}")
        if mode == "approval" and run.confirmed_at is None:
            raise ConflictError("I passi approval richiedono l'evidenza della conferma forte")
        by_category.setdefault(category, []).append(item)

    if not by_category:
        raise ConflictError("Il run non contiene passi orchestrabili")

    # Difesa in profondità: un writer configurato in modalità reale (non
    # implementata) non deve essere eseguito nemmeno tramite l'orchestratore.
    for category in by_category:
        if getattr(settings, WRITER_MODE_ATTR[category]) == "real":
            raise ConflictError(f"Writer '{category}' in modalità reale: non implementato né abilitato")

    for category, items in by_category.items():
        if category in SECRET_CATEGORIES:
            for item in items:
                if item["step_id"] not in run.encrypted_secrets:
                    raise ConflictError(f"Manca la password cifrata per {item['step_id']}")

    selected_categories = set(by_category)
    for category, items in by_category.items():
        for item in items:
            for dependency in plan_steps.get(item["step_id"], {}).get("depends_on_categories", []):
                if dependency not in selected_categories:
                    raise ConflictError(
                        f"Dipendenza '{dependency}' non selezionata né verificata per {item['step_id']}"
                    )
    return by_category


def execute(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Execution run non trovato")
    if settings.mock_orchestrator_mode != "mock":
        raise ConflictError("Orchestratore mock non abilitato: è consentita soltanto la modalità mock")
    # Ordine dei guard allineato ai writer standalone (flag → status → dry_run).
    if run.status != "queued":
        raise ConflictError("Il run deve essere confermato e in coda")
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere convertito in una scrittura")

    by_category = _prevalidate(db, run)
    ordered = [category for category in CATEGORY_ORDER if category in by_category]

    run.status = "running"
    run.started_at = datetime.now(timezone.utc)
    run.events.append(ExecutionEvent(
        phase="orchestrator",
        message="Pre-validazione superata: run, coerenza, modalità, segreti e dipendenze verificati.",
        result={"selected_categories": sorted(by_category)},
    ))
    run.events.append(ExecutionEvent(
        phase="orchestrator",
        message="Ordine di esecuzione calcolato.",
        result={"order": ordered},
    ))

    failed_category: str | None = None
    failure_reason: str | None = None
    for index, category in enumerate(ordered):
        module = PHASES[category]
        try:
            ctx = module.validate_phase(db, run)
            outcome = module.apply_phase(db, run, ctx)
        except ConflictError as exc:
            outcome = PhaseOutcome(category, ok=False, reason=str(exc))
        if not outcome.ok:
            failed_category = category
            failure_reason = outcome.reason
            not_executed = ordered[index + 1:]
            run.events.append(ExecutionEvent(
                level="error", phase="orchestrator",
                message=f"Fase '{category}' bloccata o fallita; categorie downstream non eseguite.",
                result={
                    "failed_category": category,
                    "reason": failure_reason,
                    "executed_categories": ordered[:index],
                    "not_executed": not_executed,
                },
            ))
            break

    run.finished_at = datetime.now(timezone.utc)
    if failed_category is not None:
        run.status = "failed"
        run.error = f"Orchestrazione interrotta alla fase '{failed_category}': {failure_reason}"
        db.commit(); db.refresh(run)
        return run

    # Verifica finale: rilettura dello stato mock condiviso dagli eventi immutabili.
    state = MockDestinationState.from_events(run)
    verified = {category: sorted(state.verified_steps(category)) for category in ordered}
    selected_ids = {item["step_id"] for items in by_category.values() for item in items}
    verified_ids = {step_id for ids in verified.values() for step_id in ids}
    unverified = sorted(selected_ids - verified_ids)
    if unverified:
        run.status = "failed"
        run.error = "Verifica finale fallita: passi selezionati non presenti nello stato mock condiviso"
        run.events.append(ExecutionEvent(
            level="error", phase="orchestrator_verify",
            message="Verifica finale fallita rileggendo lo stato mock condiviso.",
            result={"verified": verified, "unverified": unverified},
            verification={"status": "failed", "evidence": "shared_mock_state_reread"},
        ))
        db.commit(); db.refresh(run)
        return run

    run.status = "succeeded"
    run.events.append(ExecutionEvent(
        phase="orchestrator_verify",
        message="Verifica finale superata rileggendo lo stato mock condiviso ricostruito dagli eventi.",
        result={"verified": verified, "categories": ordered},
        verification={"status": "verified", "evidence": "shared_mock_state_reread"},
    ))
    db.commit(); db.refresh(run)
    return run
