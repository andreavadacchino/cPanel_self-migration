"""Contratto di fase condiviso fra i writer mock e l'orchestratore end-to-end.

Ogni writer mock espone lo stesso contratto:

* ``validate_phase(db, run) -> dict`` — guardrail di sicurezza (dry-run, endpoint
  destinazione mock, snapshot, passi, segreti, dipendenze, approvazione, solo
  ``missing_on_destination``). NON verifica il flag per-writer né ``run.status``:
  quelli gateano soltanto il percorso standalone ``execute``. Solleva
  ``ConflictError`` prima di qualsiasi mutazione.
* ``apply_phase(db, run, ctx) -> PhaseOutcome`` — esegue i passi della categoria,
  registra gli eventi di audit e verifica rileggendo lo stato mock. NON imposta
  lo stato terminale del run né effettua commit: lo stato terminale appartiene al
  chiamante (``execute`` standalone oppure l'orchestratore).

L'orchestratore riusa lo stesso contratto senza forzare ripetutamente
``run.status=queued`` e senza aggirare i guardrail dei writer.
"""

from __future__ import annotations

from dataclasses import dataclass

from app.modules.executions.models import ExecutionRun

# Ordine deterministico di esecuzione delle categorie.
CATEGORY_ORDER: list[str] = [
    "domains",
    "databases",
    "mysql_users",
    "email_forwarders",
    "cron_jobs",
    "ftp_accounts",
    "mailing_lists",
    "dns_records",
    "email_autoresponders",
]

# Nome della fase di audit "write" per categoria: usato per ricostruire lo stato
# mock condiviso dagli eventi immutabili del run.
WRITE_PHASE: dict[str, str] = {
    "domains": "domain_write",
    "databases": "database_write",
    "mysql_users": "mysql_user_write",
    "email_forwarders": "forwarder_write",
    "cron_jobs": "cron_write",
    "ftp_accounts": "ftp_write",
    "mailing_lists": "mailing_list_write",
    "dns_records": "dns_write",
    "email_autoresponders": "autoresponder_write",
}

# Stati di risultato considerati "presente e verificato" sul target mock.
_PRESENT_STATUSES = {
    "created",
    "already_present",
    "already_completed",
    "created_and_granted",
    "already_present_and_granted",
}


@dataclass
class PhaseOutcome:
    """Esito di una singola fase, senza stato terminale del run."""

    category: str
    ok: bool
    reason: str | None = None


class MockDestinationState:
    """Stato mock condiviso della destinazione, ricostruito ESCLUSIVAMENTE dagli
    eventi immutabili del run e non dai risultati restituiti dai writer.

    Il percorso reale futuro sostituirà questa rilettura con un nuovo preflight e
    una nuova comparazione della destinazione.
    """

    def __init__(self) -> None:
        self.verified: dict[str, set[str]] = {}

    @classmethod
    def from_events(cls, run: ExecutionRun) -> "MockDestinationState":
        state = cls()
        phase_to_category = {phase: category for category, phase in WRITE_PHASE.items()}
        for event in run.events:
            category = phase_to_category.get(event.phase)
            if category is None or not event.step_id:
                continue
            result_status = (event.result or {}).get("status")
            verified = (event.verification or {}).get("status") == "verified"
            if verified and result_status in _PRESENT_STATUSES:
                state.verified.setdefault(category, set()).add(event.step_id)
        return state

    def is_verified(self, category: str, step_id: str) -> bool:
        return step_id in self.verified.get(category, set())

    def verified_steps(self, category: str) -> set[str]:
        return set(self.verified.get(category, set()))
