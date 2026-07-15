"""Pydantic schemas for the endpoints module.

The read schema is deliberately narrow: it exposes ``auth_type`` and the opaque
``auth_ref`` but never a secret value (there is no secret column to begin with).

``auth_ref`` is enforced at the API boundary to be an *opaque reference* only
(e.g. ``vault://…``), never a raw credential. This makes the "no secret in the
DB / no secret in responses" rule a code-level invariant, not just a convention.
"""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from adapters.ssh_host_keys import MAX_HOST_KEY
from adapters.ssh_keys import InvalidPrivateKey, load_private_key_or_raise
from app.modules.endpoints.models import (
    AuthType,
    EndpointRole,
    SshAuthMethod,
    SshSecretSource,
)


def _normalize_host(raw: str) -> str:
    """Reduce a pasted value (URL, host:port, user@host, path) to a bare host.

    Operators often paste ``https://server.host.com:2083/cpanel``; the client
    builds ``https://{host}:{port}`` so a scheme/port/path in ``host`` yields a
    malformed URL and an opaque connection error. Strip them to the hostname.
    """
    h = (raw or "").strip()
    if "://" in h:
        h = h.split("://", 1)[1]
    for sep in ("/", "?", "#"):
        h = h.split(sep, 1)[0]
    if "@" in h:  # drop any userinfo
        h = h.rsplit("@", 1)[1]
    # Drop a :port suffix (host:port). IPv6 literals have multiple colons and
    # are left untouched.
    if h.count(":") == 1:
        host_part, _, port_part = h.partition(":")
        if port_part.isdigit():
            h = host_part
    return h.strip()


def _clean_host(value: str) -> str:
    host = _normalize_host(value)
    if not host:
        raise ValueError("host must be a hostname (e.g. server.host.com)")
    return host

# Reference schemes accepted for ``auth_ref`` — a pointer to a secret held
# elsewhere, resolved by a future adapter. A bare value (a raw password/token)
# is rejected so it can never be persisted or echoed back.
ALLOWED_AUTH_REF_SCHEMES: tuple[str, ...] = (
    "vault://",
    "secretsmanager://",
    "env://",
    "ref://",
)


def _validate_auth_combo(
    auth_type: AuthType,
    auth_ref: str | None,
    token: str | None,
    *,
    require_token: bool,
) -> None:
    """Shared auth/credential rules for create and update.

    ``require_token`` is True on create (a 'token' endpoint must carry a token)
    and False on update (an existing token may be kept, so the field is optional).
    """
    if auth_type == AuthType.TOKEN:
        if require_token and not token:
            raise ValueError("token is required for auth_type 'token'")
        if auth_ref is not None:
            raise ValueError("auth_ref must be null for auth_type 'token'")
        return

    # No other auth_type accepts a raw token.
    if token is not None:
        raise ValueError("token is only allowed for auth_type 'token'")

    if auth_type in (AuthType.NONE, AuthType.MOCK):
        if auth_ref is not None:
            raise ValueError("auth_ref must be null for auth_type 'none'/'mock'")
    else:  # token_ref | password_ref
        if not auth_ref:
            raise ValueError(
                "auth_ref is required for auth_type 'token_ref'/'password_ref'"
            )
        if not auth_ref.startswith(ALLOWED_AUTH_REF_SCHEMES):
            raise ValueError(
                "auth_ref must be an opaque reference "
                "(e.g. vault://…), never a raw secret"
            )


class EndpointCreate(BaseModel):
    role: EndpointRole
    label: str | None = Field(default=None, max_length=255)
    host: str = Field(min_length=1, max_length=255)
    port: int = Field(default=2083, ge=1, le=65535)
    username: str = Field(min_length=1, max_length=255)
    auth_type: AuthType = AuthType.MOCK
    auth_ref: str | None = Field(default=None, max_length=255)
    # Write-only: the plaintext token for auth_type 'token'. It is encrypted on
    # create and never read back (EndpointRead exposes only ``has_auth_secret``).
    token: str | None = Field(default=None, max_length=4096, repr=False)
    # False skips TLS certificate verification (self-signed / mismatched certs).
    verify_tls: bool = True

    _normalize_host = field_validator("host")(_clean_host)

    @model_validator(mode="after")
    def _enforce_credentials(self) -> "EndpointCreate":
        _validate_auth_combo(
            self.auth_type, self.auth_ref, self.token, require_token=True
        )
        return self


class EndpointUpdate(BaseModel):
    """Edit an existing endpoint's coordinates/credentials.

    ``role`` is immutable (the card is per-role). ``token`` is optional: when
    ``auth_type`` stays 'token' and no new token is given, the existing encrypted
    token is kept.
    """

    label: str | None = Field(default=None, max_length=255)
    host: str = Field(min_length=1, max_length=255)
    port: int = Field(default=2083, ge=1, le=65535)
    username: str = Field(min_length=1, max_length=255)
    auth_type: AuthType = AuthType.MOCK
    auth_ref: str | None = Field(default=None, max_length=255)
    token: str | None = Field(default=None, max_length=4096, repr=False)
    verify_tls: bool = True

    _normalize_host = field_validator("host")(_clean_host)

    @model_validator(mode="after")
    def _enforce_credentials(self) -> "EndpointUpdate":
        _validate_auth_combo(
            self.auth_type, self.auth_ref, self.token, require_token=False
        )
        return self


class EndpointCredentialUpdate(BaseModel):
    """Refresh a directly-entered (time-limited) token on an existing endpoint."""

    token: str = Field(min_length=1, max_length=4096, repr=False)


# References accepted for an SSH secret held elsewhere (env:// only is resolvable
# today; the rest are reserved and rejected at resolve time, not here). A raw
# value is refused so a secret can never be persisted in a ref column.
ALLOWED_SSH_REF_SCHEMES: tuple[str, ...] = ALLOWED_AUTH_REF_SCHEMES

#: The maximum SSH port; a private key rarely exceeds a few KB, but a bounded
#: field keeps a fat-fingered paste out of the database.
MAX_SSH_PRIVATE_KEY = 32768


def _looks_like_private_key(material: str) -> bool:
    """A PEM private key, not a file path or a public key.

    The engine reads the key from a path on disk; the platform stores the
    MATERIAL and the worker writes it out. A path (``/home/op/.ssh/id_ed25519``)
    is both unreadable from the container and not key material — this is what
    separates the two.
    """
    text = material.strip()
    # Must BEGIN with the PEM header, not merely contain it: a file path with a
    # marker smuggled in after a newline ("/tmp/x\n-----BEGIN … PRIVATE KEY-----")
    # would otherwise pass while still being, at its head, a path.
    return (
        text.startswith("-----BEGIN ")
        and "PRIVATE KEY-----" in text
        and text.rstrip().endswith("-----")
        and "-----END " in text
    )


class SshCredentialBundle(BaseModel):
    """The full SSH credential for one endpoint, set as a unit.

    A typed bundle rather than loose fields: four consecutive optional secrets
    are exactly the shape a caller half-fills by mistake. It REPLACES the
    endpoint's SSH credential wholesale — there is no partial merge — so the
    method and its one secret are always internally consistent.

    ``auth_method`` and ``secret_source`` are orthogonal: the method is password
    or private key; the source is the material itself (``direct``, encrypted at
    rest) or an opaque ``ref`` the worker resolves. ``none`` clears everything.
    """

    model_config = ConfigDict(extra="forbid")

    auth_method: SshAuthMethod
    secret_source: SshSecretSource | None = None
    username: str | None = Field(default=None, max_length=255)
    port: int | None = Field(default=None, ge=1, le=65535)

    # Direct secrets — write-only, never read back.
    password: str | None = Field(default=None, max_length=1024, repr=False)
    private_key: str | None = Field(default=None, max_length=MAX_SSH_PRIVATE_KEY, repr=False)
    key_passphrase: str | None = Field(default=None, max_length=1024, repr=False)
    # Opaque references — a pointer, never a value.
    password_ref: str | None = Field(default=None, max_length=255)
    private_key_ref: str | None = Field(default=None, max_length=255)
    key_passphrase_ref: str | None = Field(default=None, max_length=255)

    @model_validator(mode="after")
    def _validate(self) -> "SshCredentialBundle":
        method = self.auth_method
        direct = (self.password, self.private_key, self.key_passphrase)
        refs = (self.password_ref, self.private_key_ref, self.key_passphrase_ref)

        if method == SshAuthMethod.NONE:
            if (
                self.secret_source is not None
                or any(direct)
                or any(refs)
                or self.username
                or self.port is not None
            ):
                raise ValueError(
                    "auth_method 'none' takes no username, port, source or secret"
                )
            return self

        if not self.username:
            raise ValueError("username is required when an SSH method is set")
        if self.secret_source is None:
            raise ValueError("secret_source is required when an SSH method is set")

        # A passphrase belongs only to a key.
        if method != SshAuthMethod.PRIVATE_KEY and (
            self.key_passphrase or self.key_passphrase_ref
        ):
            raise ValueError("a key passphrase applies only to auth_method 'private_key'")

        if self.secret_source == SshSecretSource.DIRECT:
            if any(refs):
                raise ValueError("secret_source 'direct' takes no *_ref fields")
            self._validate_direct(method)
        else:  # REF
            if any(direct):
                raise ValueError("secret_source 'ref' takes no direct secret fields")
            self._validate_ref(method)
        return self

    def _validate_direct(self, method: SshAuthMethod) -> None:
        if method == SshAuthMethod.PASSWORD:
            if not self.password:
                raise ValueError("password is required for a direct password method")
            if self.private_key:
                raise ValueError("a password method takes no private_key")
        else:  # PRIVATE_KEY
            if not self.private_key:
                raise ValueError("private_key is required for a direct private_key method")
            if self.password:
                raise ValueError("a private_key method takes no password")
            if not _looks_like_private_key(self.private_key):
                raise ValueError(
                    "private_key must be PEM private key material, not a file path "
                    "or a public key"
                )
            # Prove it is a usable key, and that the passphrase (if any) matches,
            # before it is ever encrypted and stored. Turns "accepted here,
            # rejected by the engine at launch" into an input-time 422. The error
            # is generic — it never echoes the key or the passphrase.
            try:
                load_private_key_or_raise(self.private_key, self.key_passphrase)
            except InvalidPrivateKey as exc:
                raise ValueError(str(exc)) from exc

    def _validate_ref(self, method: SshAuthMethod) -> None:
        wanted = self.password_ref if method == SshAuthMethod.PASSWORD else self.private_key_ref
        other = self.private_key_ref if method == SshAuthMethod.PASSWORD else self.password_ref
        if other:
            raise ValueError("the ref does not match auth_method")
        for name, value in (
            (("password_ref" if method == SshAuthMethod.PASSWORD else "private_key_ref"), wanted),
            ("key_passphrase_ref", self.key_passphrase_ref),
        ):
            if value and not value.startswith(ALLOWED_SSH_REF_SCHEMES):
                raise ValueError(
                    f"{name} must be an opaque reference (e.g. env://…), never a raw secret"
                )
        if not wanted:
            raise ValueError("a ref source requires the matching *_ref for the method")


class SshHostKeyUpsert(BaseModel):
    """Pin (replace) an endpoint's SSH host key.

    The client sends only the public key. Host, port and fingerprint are the
    server's to decide — there is deliberately no field for them here, so the
    client cannot bind a pin to coordinates or a fingerprint of its choosing.
    ``extra='forbid'`` refuses a smuggled ``host``/``port``/``fingerprint``.
    """

    model_config = ConfigDict(extra="forbid")

    public_key: str = Field(min_length=1, max_length=MAX_HOST_KEY)


class SshHostKeyRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    endpoint_id: int
    host: str
    port: int
    key_type: str
    public_key: str
    fingerprint_sha256: str
    created_at: datetime
    updated_at: datetime


class EndpointRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    migration_id: int
    role: str
    label: str | None
    host: str
    port: int
    username: str
    auth_type: str
    # The opaque auth_ref and the encrypted token are NEVER returned. Only these
    # boolean flags tell the UI whether a credential is configured.
    has_auth_ref: bool
    has_auth_secret: bool
    verify_tls: bool
    connection_status: str
    last_checked_at: datetime | None
    last_error: str | None
    capabilities: dict | None
    created_at: datetime
    updated_at: datetime
    # SSH: the fact of a credential, never the credential. No *_enc, no material,
    # no ref value — only the method/source metadata and the has_* flags.
    ssh_auth_method: str
    ssh_secret_source: str | None
    ssh_username: str | None
    ssh_port: int | None
    has_ssh_password: bool
    has_ssh_private_key: bool
    has_ssh_key_passphrase: bool
