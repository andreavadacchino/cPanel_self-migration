"""Application configuration via environment variables (pydantic-settings)."""

from __future__ import annotations

from functools import lru_cache

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    app_name: str = "Migration Platform API"
    # Default to a local SQLite file so the app is runnable without Postgres.
    # In Docker this is overridden by DATABASE_URL (postgresql+psycopg://...).
    database_url: str = "sqlite+pysqlite:///./dev.db"
    redis_url: str = "redis://localhost:6379/0"
    cors_origins: str = "http://localhost:5173"
    credential_encryption_key: str | None = None
    preflight_inline: bool = False
    # Hard safety switch. Only "mock" is implemented; "real" is rejected.
    domain_writer_mode: str = "disabled"
    database_writer_mode: str = "disabled"
    mysql_user_writer_mode: str = "disabled"
    forwarder_writer_mode: str = "disabled"
    cron_writer_mode: str = "disabled"
    ftp_writer_mode: str = "disabled"
    mailing_list_writer_mode: str = "disabled"
    dns_writer_mode: str = "disabled"
    autoresponder_writer_mode: str = "disabled"
    # Orchestratore mock end-to-end: coordina i writer mock in un solo run.
    mock_orchestrator_mode: str = "disabled"

    @property
    def cors_origins_list(self) -> list[str]:
        return [o.strip() for o in self.cors_origins.split(",") if o.strip()]


@lru_cache
def get_settings() -> Settings:
    return Settings()


settings = get_settings()
