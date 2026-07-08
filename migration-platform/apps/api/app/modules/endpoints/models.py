"""SQLAlchemy model for an Endpoint.

An endpoint is a source or destination cPanel host attached to a migration.

Security rule (Sprint 1): this table never stores a real secret. ``auth_ref``
is an opaque *reference* (e.g. a vault path) resolved elsewhere, never the
credential itself.
"""

from __future__ import annotations

import enum
from datetime import datetime

from sqlalchemy import (
    JSON,
    Boolean,
    DateTime,
    ForeignKey,
    Integer,
    String,
    Text,
    func,
    true,
)
from sqlalchemy.orm import Mapped, mapped_column

from app.db.base import Base


class EndpointRole(str, enum.Enum):
    SOURCE = "source"
    DESTINATION = "destination"


class AuthType(str, enum.Enum):
    NONE = "none"
    TOKEN = "token"  # direct cPanel API token, encrypted at rest
    TOKEN_REF = "token_ref"  # opaque reference (env://VAR) resolved elsewhere
    PASSWORD_REF = "password_ref"
    MOCK = "mock"


class ConnectionStatus(str, enum.Enum):
    UNKNOWN = "unknown"
    TESTING = "testing"
    CONNECTED = "connected"
    FAILED = "failed"


class Endpoint(Base):
    __tablename__ = "endpoints"

    id: Mapped[int] = mapped_column(primary_key=True)
    migration_id: Mapped[int] = mapped_column(
        ForeignKey("migrations.id", ondelete="CASCADE"), nullable=False, index=True
    )
    role: Mapped[str] = mapped_column(String(16), nullable=False)
    label: Mapped[str | None] = mapped_column(String(255), nullable=True)
    host: Mapped[str] = mapped_column(String(255), nullable=False)
    port: Mapped[int] = mapped_column(
        Integer, default=2083, server_default="2083", nullable=False
    )
    username: Mapped[str] = mapped_column(String(255), nullable=False)
    auth_type: Mapped[str] = mapped_column(
        String(16),
        default=AuthType.MOCK.value,
        server_default=AuthType.MOCK.value,
        nullable=False,
    )
    # Opaque reference only — never a real password/token.
    auth_ref: Mapped[str | None] = mapped_column(String(255), nullable=True)
    # Fernet ciphertext of a directly-entered token (auth_type "token"). The
    # plaintext is never stored here and never returned by the API.
    auth_secret_enc: Mapped[str | None] = mapped_column(Text, nullable=True)
    # When False, skip TLS certificate verification (self-signed / hostname-
    # mismatched cPanel certs). Opt-in and insecure; default is to verify.
    verify_tls: Mapped[bool] = mapped_column(
        Boolean, default=True, server_default=true(), nullable=False
    )
    connection_status: Mapped[str] = mapped_column(
        String(16),
        default=ConnectionStatus.UNKNOWN.value,
        server_default=ConnectionStatus.UNKNOWN.value,
        nullable=False,
    )
    last_checked_at: Mapped[datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    last_error: Mapped[str | None] = mapped_column(Text, nullable=True)
    capabilities: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), nullable=False
    )
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True),
        server_default=func.now(),
        onupdate=func.now(),
        nullable=False,
    )

    @property
    def has_auth_ref(self) -> bool:
        """Whether an opaque credential reference is configured.

        Exposed to the API instead of ``auth_ref`` so the reference itself is
        never serialized to the UI.
        """
        return self.auth_ref is not None

    @property
    def has_auth_secret(self) -> bool:
        """Whether a directly-entered token is stored (encrypted).

        Exposed instead of the ciphertext so neither the token nor its
        ciphertext is ever serialized to the UI.
        """
        return self.auth_secret_enc is not None
