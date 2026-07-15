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
    CheckConstraint,
    DateTime,
    ForeignKey,
    Integer,
    String,
    Text,
    UniqueConstraint,
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


class SshAuthMethod(str, enum.Enum):
    """How the executor authenticates over SSH to this endpoint's account.

    A capability DISTINCT from ``AuthType`` (the cPanel API token): the ADR
    models ``cpanel_api_access`` and ``ssh_account_access`` as independent axes.
    The engine takes exactly one method per host — password OR private key,
    never both — so this enum is single-valued, not a set.
    """

    NONE = "none"
    PASSWORD = "password"
    PRIVATE_KEY = "private_key"


class SshSecretSource(str, enum.Enum):
    """Where the SSH secret lives.

    ``direct`` — the material itself, encrypted at rest (Fernet), same as the
    cPanel token. ``ref`` — an opaque reference (``env://…``) the worker resolves
    at run time; the platform stores the pointer, never the value. Orthogonal to
    the method: a password or a private key can be either.
    """

    DIRECT = "direct"
    REF = "ref"


class ConnectionStatus(str, enum.Enum):
    UNKNOWN = "unknown"
    TESTING = "testing"
    CONNECTED = "connected"
    FAILED = "failed"


# Database-level guardrails on the SSH columns. Pydantic validates the request,
# but the worker will read these rows as the source of truth — a bad row inserted
# by any other path (a fixture, a manual UPDATE, a future bug) must not reach it.
# These pin the enums, the port range, and that a 'none' method carries nothing.
# The direct/ref secret-coherence rule is deliberately NOT encoded here (it would
# be a long, brittle predicate); the runtime validates each row fail-closed
# before it decrypts or materializes anything.
_SSH_CONSTRAINTS = (
    CheckConstraint(
        "ssh_auth_method IN ('none', 'password', 'private_key')",
        name="ck_endpoints_ssh_auth_method",
    ),
    CheckConstraint(
        "ssh_secret_source IS NULL OR ssh_secret_source IN ('direct', 'ref')",
        name="ck_endpoints_ssh_secret_source",
    ),
    CheckConstraint(
        "ssh_port IS NULL OR (ssh_port BETWEEN 1 AND 65535)",
        name="ck_endpoints_ssh_port_range",
    ),
    CheckConstraint(
        "ssh_auth_method <> 'none' OR ("
        "ssh_secret_source IS NULL AND ssh_username IS NULL AND ssh_port IS NULL "
        "AND ssh_password_enc IS NULL AND ssh_private_key_enc IS NULL "
        "AND ssh_key_passphrase_enc IS NULL AND ssh_password_ref IS NULL "
        "AND ssh_private_key_ref IS NULL AND ssh_key_passphrase_ref IS NULL)",
        name="ck_endpoints_ssh_none_is_empty",
    ),
)


class Endpoint(Base):
    __tablename__ = "endpoints"
    __table_args__ = _SSH_CONSTRAINTS

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

    # --- SSH account access (distinct capability from the cPanel token) -------
    # Persistence only: nothing here connects, resolves a ref or builds a
    # host.yaml. `none` by default, so every pre-existing endpoint is untouched.
    ssh_auth_method: Mapped[str] = mapped_column(
        String(16),
        default=SshAuthMethod.NONE.value,
        server_default=SshAuthMethod.NONE.value,
        nullable=False,
    )
    # direct | ref, or NULL when the method is none.
    ssh_secret_source: Mapped[str | None] = mapped_column(String(8), nullable=True)
    # The SSH login user and port. The port is the SSH port (default 22), NOT
    # `port` above, which is the cPanel UAPI port (2083).
    ssh_username: Mapped[str | None] = mapped_column(String(255), nullable=True)
    ssh_port: Mapped[int | None] = mapped_column(Integer, nullable=True)
    # Fernet ciphertext of a directly-entered secret. Plaintext is never stored
    # and never returned — only the boolean has_* flags are exposed.
    ssh_password_enc: Mapped[str | None] = mapped_column(Text, nullable=True)
    ssh_private_key_enc: Mapped[str | None] = mapped_column(Text, nullable=True)
    ssh_key_passphrase_enc: Mapped[str | None] = mapped_column(Text, nullable=True)
    # Opaque references (env://…) resolved by the worker at run time. A pointer,
    # never the value — mirrors auth_ref.
    ssh_password_ref: Mapped[str | None] = mapped_column(String(255), nullable=True)
    ssh_private_key_ref: Mapped[str | None] = mapped_column(String(255), nullable=True)
    ssh_key_passphrase_ref: Mapped[str | None] = mapped_column(
        String(255), nullable=True
    )
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

    @property
    def has_ssh_password(self) -> bool:
        """Whether an SSH password is configured (direct ciphertext or a ref).

        Exposed instead of the secret/pointer so neither is serialized.
        """
        return self.ssh_password_enc is not None or self.ssh_password_ref is not None

    @property
    def has_ssh_private_key(self) -> bool:
        return (
            self.ssh_private_key_enc is not None
            or self.ssh_private_key_ref is not None
        )

    @property
    def has_ssh_key_passphrase(self) -> bool:
        return (
            self.ssh_key_passphrase_enc is not None
            or self.ssh_key_passphrase_ref is not None
        )


# Named, database-level guardrails on the host-key pin. The runtime will read
# this row as an identity to trust, so a bad row from any non-API path (a
# fixture, a manual INSERT) must not reach it: one pin per endpoint, a valid
# port, non-blank text, and a fingerprint that carries the SHA256: prefix. The
# host/port coherence with the endpoint's *current* SSH coordinates is validated
# fail-closed in the service and re-checked by the runtime — deliberately NOT a
# composite FK, which would be brittle against the endpoint's mutable coordinates.
_HOST_KEY_CONSTRAINTS = (
    UniqueConstraint("endpoint_id", name="uq_endpoint_ssh_host_key_endpoint"),
    CheckConstraint(
        "port BETWEEN 1 AND 65535", name="ck_endpoint_ssh_host_key_port_range"
    ),
    CheckConstraint("length(host) > 0", name="ck_endpoint_ssh_host_key_host_nonblank"),
    CheckConstraint(
        "length(key_type) > 0", name="ck_endpoint_ssh_host_key_key_type_nonblank"
    ),
    CheckConstraint(
        "length(public_key) > 0", name="ck_endpoint_ssh_host_key_public_key_nonblank"
    ),
    CheckConstraint(
        "fingerprint_sha256 LIKE 'SHA256:_%'",
        name="ck_endpoint_ssh_host_key_fingerprint_format",
    ),
)


class EndpointSshHostKey(Base):
    """The pinned SSH host key for one endpoint's current SSH coordinates.

    Persistence only: nothing here connects, runs ssh-keyscan, applies TOFU or
    writes a known_hosts. ``host`` and ``port`` are a SNAPSHOT of the endpoint's
    SSH coordinates taken server-side at pin time — never client-supplied — so a
    later runtime can refuse a pin that no longer matches the endpoint it locked.
    One active pin per endpoint (unique ``endpoint_id``); it is removed by FK
    CASCADE with the endpoint, and invalidated by the service when the endpoint's
    ``host`` or ``ssh_port`` change.

    ``public_key`` is the canonical ``algorithm base64`` form and
    ``fingerprint_sha256`` the standard OpenSSH ``SHA256:…`` fingerprint, both
    computed server-side from the submitted key. Host key material is public:
    there is no secret column here and nothing to redact.
    """

    __tablename__ = "endpoint_ssh_host_keys"
    __table_args__ = _HOST_KEY_CONSTRAINTS

    id: Mapped[int] = mapped_column(primary_key=True)
    endpoint_id: Mapped[int] = mapped_column(
        ForeignKey("endpoints.id", ondelete="CASCADE"), nullable=False
    )
    # A snapshot of the endpoint's SSH coordinates at pin time (server-derived).
    host: Mapped[str] = mapped_column(String(255), nullable=False)
    port: Mapped[int] = mapped_column(Integer, nullable=False)
    key_type: Mapped[str] = mapped_column(String(32), nullable=False)
    public_key: Mapped[str] = mapped_column(Text, nullable=False)
    fingerprint_sha256: Mapped[str] = mapped_column(String(80), nullable=False)
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), nullable=False
    )
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True),
        server_default=func.now(),
        onupdate=func.now(),
        nullable=False,
    )
