"""Application configuration via environment variables (pydantic-settings)."""

from __future__ import annotations

from functools import lru_cache

from pydantic import field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict

# Recognised values of ``DOMAIN_WRITER_MODE``. ``disabled`` (default) does
# nothing, ``mock`` drives the simulated writer path, and ``enabled`` is the
# real destination writer's own switch (still inert without the master
# ``REAL_EXECUTION_MODE`` gate). Any other value is a misconfiguration of a
# write-enabling safety flag and is rejected fail-closed at load time.
_DOMAIN_WRITER_MODES = frozenset({"disabled", "mock", "enabled"})


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    app_name: str = "Migration Platform API"
    # Default to a local SQLite file so the app is runnable without Postgres.
    # In Docker this is overridden by DATABASE_URL (postgresql+psycopg://...).
    database_url: str = "sqlite+pysqlite:///./dev.db"
    redis_url: str = "redis://localhost:6379/0"
    cors_origins: str = "http://localhost:5173"
    credential_encryption_key: str | None = None
    # Dedicated key for the durable email pre-write backup store (task B4e-iii-a). It is
    # deliberately SEPARATE from the credential key (no silent fallback) and is required only
    # when a backup is persisted or loaded; its absence fails closed before any write. Losing
    # this key makes the encrypted backups — and therefore rollback — unrecoverable.
    email_backup_encryption_key: str | None = None
    preflight_inline: bool = False
    # Hard safety switch. Only "mock" is implemented; "real" is rejected.
    domain_writer_mode: str = "disabled"
    database_writer_mode: str = "disabled"
    mysql_user_writer_mode: str = "disabled"
    forwarder_writer_mode: str = "disabled"
    default_address_writer_mode: str = "disabled"
    routing_writer_mode: str = "disabled"
    filter_writer_mode: str = "disabled"
    cron_writer_mode: str = "disabled"
    ftp_writer_mode: str = "disabled"
    mailing_list_writer_mode: str = "disabled"
    dns_writer_mode: str = "disabled"
    autoresponder_writer_mode: str = "disabled"
    # Orchestratore mock end-to-end: coordina i writer mock in un solo run.
    mock_orchestrator_mode: str = "disabled"
    # Domain crash-recovery (B4e-iii-c-iii-b R2-b2). Explicit opt-in, disabled by
    # default: the recovery service is invocable in tests, but no scheduler/worker
    # sweep acts on a live run unless this and the master switch are both enabled.
    domain_recovery_mode: str = "disabled"
    # Email crash-recovery (B4e-iii-c R2-c2). Same fail-closed opt-in as the domain
    # recovery: the pure service is unit/PostgreSQL-tested, but no runtime sweep acts.
    email_recovery_mode: str = "disabled"
    # Master switch for the real (non-dry-run) execution contract. Only
    # "disabled" and "enabled" are accepted; it defaults to disabled so no real
    # attempt, lease, or destination mutation can be opened without an explicit,
    # audited opt-in for an authorized environment.
    real_execution_mode: str = "disabled"
    # Time-to-live of a destination-account execution lease. A holder must renew
    # (heartbeat) within this window or the lease becomes eligible for a fenced
    # takeover by another worker.
    execution_lease_ttl_seconds: int = 300
    # Maximum age of a strong confirmation before a real write phase must be
    # re-confirmed. The safety gate rejects a confirmation older than this so a
    # long-stale authorization can never drive a mutation.
    real_confirmation_ttl_seconds: int = 900

    @property
    def cors_origins_list(self) -> list[str]:
        return [o.strip() for o in self.cors_origins.split(",") if o.strip()]

    @field_validator("domain_writer_mode")
    @classmethod
    def _validate_domain_writer_mode(cls, value: str) -> str:
        # Fail closed: refuse to boot with an unknown value for a flag that can
        # authorise a real destination mutation, rather than silently treating a
        # typo (e.g. "enabledd", "real") as disabled and hiding the misconfig.
        if value not in _DOMAIN_WRITER_MODES:
            raise ValueError(
                f"DOMAIN_WRITER_MODE non valido: {value!r} "
                f"(ammessi: {', '.join(sorted(_DOMAIN_WRITER_MODES))})"
            )
        return value

    @field_validator("forwarder_writer_mode")
    @classmethod
    def _validate_forwarder_writer_mode(cls, value: str) -> str:
        # Same fail-closed rule as the domain flag: an unknown value for a flag that
        # can authorise a real email mutation is rejected at load time (B4a).
        if value not in _DOMAIN_WRITER_MODES:
            raise ValueError(
                f"FORWARDER_WRITER_MODE non valido: {value!r} "
                f"(ammessi: {', '.join(sorted(_DOMAIN_WRITER_MODES))})"
            )
        return value

    @field_validator("default_address_writer_mode")
    @classmethod
    def _validate_default_address_writer_mode(cls, value: str) -> str:
        # Same fail-closed rule (B4b-i): an unknown value for a flag that can
        # authorise a real catch-all overwrite is rejected at load time.
        if value not in _DOMAIN_WRITER_MODES:
            raise ValueError(
                f"DEFAULT_ADDRESS_WRITER_MODE non valido: {value!r} "
                f"(ammessi: {', '.join(sorted(_DOMAIN_WRITER_MODES))})"
            )
        return value

    @field_validator("routing_writer_mode")
    @classmethod
    def _validate_routing_writer_mode(cls, value: str) -> str:
        # Fail-closed (B4c-i): an unknown value for a flag that can authorise a real
        # mail-route overwrite is rejected at load time.
        if value not in _DOMAIN_WRITER_MODES:
            raise ValueError(
                f"ROUTING_WRITER_MODE non valido: {value!r} "
                f"(ammessi: {', '.join(sorted(_DOMAIN_WRITER_MODES))})"
            )
        return value

    @field_validator("filter_writer_mode")
    @classmethod
    def _validate_filter_writer_mode(cls, value: str) -> str:
        # Fail-closed (B4d-i): an unknown value for a flag that can authorise a real
        # filter store_filter (an UPSERT) is rejected at load time.
        if value not in _DOMAIN_WRITER_MODES:
            raise ValueError(
                f"FILTER_WRITER_MODE non valido: {value!r} "
                f"(ammessi: {', '.join(sorted(_DOMAIN_WRITER_MODES))})"
            )
        return value

    @field_validator("autoresponder_writer_mode")
    @classmethod
    def _validate_autoresponder_writer_mode(cls, value: str) -> str:
        # Fail-closed (B4e-i): an unknown value for a flag that can authorise a real
        # add_auto_responder (an UPSERT) is rejected at load time.
        if value not in _DOMAIN_WRITER_MODES:
            raise ValueError(
                f"AUTORESPONDER_WRITER_MODE non valido: {value!r} "
                f"(ammessi: {', '.join(sorted(_DOMAIN_WRITER_MODES))})"
            )
        return value

    @property
    def real_execution_enabled(self) -> bool:
        return self.real_execution_mode == "enabled"

    @property
    def domain_real_writer_enabled(self) -> bool:
        # Double gate: a real destination domain create is reachable only when
        # BOTH the master real switch and the domain-writer switch are enabled.
        # Exact-match on each value keeps every other combination fail-closed.
        return self.real_execution_enabled and self.domain_writer_mode == "enabled"

    @property
    def forwarder_real_writer_enabled(self) -> bool:
        # Double gate for the real additive forwarder writer (B4a): reachable only
        # when both the master real switch and FORWARDER_WRITER_MODE are "enabled".
        return self.real_execution_enabled and self.forwarder_writer_mode == "enabled"

    @property
    def default_address_real_writer_enabled(self) -> bool:
        # Double gate for the compensable default-address writer (B4b): reachable
        # only when both the master real switch and DEFAULT_ADDRESS_WRITER_MODE are
        # "enabled". The B4b-i rules perform no write; this gate is the seam the
        # B4b-ii engine (and B4e dispatch) require before any real catch-all set.
        return self.real_execution_enabled and self.default_address_writer_mode == "enabled"

    @property
    def routing_real_writer_enabled(self) -> bool:
        # Double gate for the compensable routing writer (B4c): reachable only when
        # both the master real switch and ROUTING_WRITER_MODE are "enabled". The B4c-i
        # rules perform no write; the B4c-ii engine (and B4e) require this gate.
        return self.real_execution_enabled and self.routing_writer_mode == "enabled"

    @property
    def filter_real_writer_enabled(self) -> bool:
        # Double gate for the additive-only filter writer (B4d): reachable only when
        # both the master real switch and FILTER_WRITER_MODE are "enabled". The B4d-i
        # rules perform no write; the B4d-ii engine (and B4e) require this gate before
        # any real store_filter (an UPSERT reached only on a live-absent name).
        return self.real_execution_enabled and self.filter_writer_mode == "enabled"

    @property
    def autoresponder_real_writer_enabled(self) -> bool:
        # Double gate for the additive-only autoresponder writer (B4e): reachable only
        # when both the master real switch and AUTORESPONDER_WRITER_MODE are "enabled".
        # The B4e-i rules perform no write; the B4e-ii engine (and B4e-iii dispatch)
        # require this gate before any real add_auto_responder (an UPSERT reached only
        # on a live-absent address). The value "mock" drives the separate mock writer.
        return self.real_execution_enabled and self.autoresponder_writer_mode == "enabled"

    @property
    def domain_recovery_enabled(self) -> bool:
        # Double gate for the R2-b2 recovery sweep: an automatic recovery worker may
        # act on a live run only when both the master real switch and
        # DOMAIN_RECOVERY_MODE are "enabled". Off by default — the pure service stays
        # explicitly invocable (and unit-tested) without this gate.
        return self.real_execution_enabled and self.domain_recovery_mode == "enabled"

    @property
    def email_recovery_enabled(self) -> bool:
        # Double gate for the R2-c2 email recovery sweep; off by default. The pure
        # service stays explicitly invocable (and tested) without this gate.
        return self.real_execution_enabled and self.email_recovery_mode == "enabled"


@lru_cache
def get_settings() -> Settings:
    return Settings()


settings = get_settings()
