"""Read an endpoint and its host-key pin coherently, and prove the pin.

The pin is deliberately not bound to the endpoint's coordinates by a foreign key
— they are mutable, and a composite FK would be brittle against them. So "this
pin belongs to this host and port" is a fact that is only ever true *at the
moment of a coherent read*. This module establishes that moment: it takes the
endpoint row lock — the same ``FOR UPDATE`` the API's ``set_ssh_host_key``,
``update_endpoint`` and ``set_ssh_credentials`` take — and reads both rows inside
it, so a concurrent coordinate change either lands before the read (and the pin
is already gone) or after it (and our snapshot was true when taken).

It then re-validates the pin cryptographically. The DB CHECKs are format-only:
a row written outside the API can carry a well-formed fingerprint that was never
derived from its key. ``validate_persisted_host_key`` is the single authority on
that relationship and is reused here rather than re-implemented — the coordinate
half is the caller's, exactly as the adapter's docstring says, and
SSH_HOST_IDENTITY I11 requires the runtime to do both.

Boundary: worker → adapters. The API's ``validate_ssh_host_key_pin`` performs the
same two steps but takes ORM objects, and the worker never imports the FastAPI
app; the semantics are mirrored, the crypto authority is shared.

Under the lock: two SELECTs, the coordinate comparison and the pin's crypto
proof. Nothing else — no network, no filesystem, no subprocess, and deliberately
**not** the credential resolution. Resolving a private key runs bcrypt_pbkdf,
whose cost is chosen by whoever generated the key (``ssh-keygen -a 1000`` is
seconds, not milliseconds), and holding the endpoint row for that long would
block the API's own ``set_ssh_host_key``/``update_endpoint``/
``set_ssh_credentials`` on the same row. The secret columns are copied out under
the lock and resolved after it: the values are a snapshot either way, and the
coherence that matters — host/port against the pin — was established while the
row was held. The workspace is built later still, for the same reason.

This module authorizes nothing. A snapshot records that the pin was coherent when
read; the executor that will one day start a subprocess must re-read and
re-validate immediately before launching, and refuse a snapshot that has drifted.
"""

from __future__ import annotations

from collections.abc import Mapping

from adapters.ssh_host_keys import InvalidPersistedHostKey, validate_persisted_host_key
from adapters.ssh_runtime import (
    SshRuntimeConfigurationError,
    SshRuntimeSnapshot,
    resolve_ssh_credentials,
)
from sqlalchemy import select
from sqlalchemy.engine import Engine

from worker import db

__all__ = [
    "EndpointNotFound",
    "SshHostIdentityError",
    "load_ssh_runtime_snapshot",
]

_MIN_PORT = 1
_MAX_PORT = 65535


class EndpointNotFound(Exception):
    """No endpoint with that id."""


class SshHostIdentityError(Exception):
    """No trustworthy host identity for this endpoint.

    Raised uniformly whether the pin is absent, sits on stale coordinates, or is
    cryptographically incoherent. The distinction is deliberately not surfaced:
    the answer in every case is the same (an operator must re-pin the key), and a
    finer verdict would describe the stored row to the caller. Names no stored
    value.
    """


def load_ssh_runtime_snapshot(
    engine: Engine,
    endpoint_id: int,
    *,
    environ: Mapping[str, str] | None = None,
) -> SshRuntimeSnapshot:
    """Load one endpoint's SSH runtime identity, fail-closed, under a row lock.

    Raises :class:`EndpointNotFound`, :class:`SshHostIdentityError`,
    :class:`~adapters.ssh_runtime.SshRuntimeConfigurationError` (SSH not
    configured, or an unusable/incoherent row) or
    :class:`~adapters.ssh_runtime.SshSecretResolutionError` (a declared secret
    will not resolve). Never writes: a corrupt pin is refused, not repaired or
    deleted.
    """
    with engine.begin() as conn:
        # SQLite ignores FOR UPDATE; the serialization is proven on real
        # PostgreSQL in test_ssh_runtime_pg.py.
        endpoint = conn.execute(
            select(db.endpoints)
            .where(db.endpoints.c.id == endpoint_id)
            .with_for_update()
        ).one_or_none()
        if endpoint is None:
            raise EndpointNotFound(f"endpoint {endpoint_id} does not exist")

        if endpoint.ssh_auth_method == "none":
            raise SshRuntimeConfigurationError(
                "the endpoint has no SSH configured (ssh_auth_method is 'none')"
            )
        if not (endpoint.host or "").strip():
            raise SshRuntimeConfigurationError("the endpoint has no host")
        if not (endpoint.ssh_username or "").strip():
            # Only the write-path Pydantic bundle requires this; a row written
            # around it can have a method and no user.
            raise SshRuntimeConfigurationError("ssh_username is required")
        port = endpoint.ssh_port
        if port is None or not (_MIN_PORT <= port <= _MAX_PORT):
            raise SshRuntimeConfigurationError("ssh_port is missing or out of range")

        pin = conn.execute(
            select(db.endpoint_ssh_host_keys).where(
                db.endpoint_ssh_host_keys.c.endpoint_id == endpoint_id
            )
        ).one_or_none()
        if pin is None:
            raise SshHostIdentityError(
                f"endpoint {endpoint_id} has no pinned SSH host key"
            )
        # Coordinates first (ours), then the crypto proof (the adapter's).
        if pin.host != endpoint.host or pin.port != port:
            raise SshHostIdentityError(
                "the pinned SSH host key no longer matches the endpoint's "
                "coordinates"
            )
        try:
            host_key = validate_persisted_host_key(
                public_key=pin.public_key,
                key_type=pin.key_type,
                fingerprint_sha256=pin.fingerprint_sha256,
            )
        except InvalidPersistedHostKey:
            # from None: one generic verdict, and the adapter's message already
            # describes the stored row more than a caller needs.
            raise SshHostIdentityError(
                "the pinned SSH host key is not internally coherent"
            ) from None

        # Copy the row out; resolve after the lock (see the module docstring).
        secret_columns = {
            "auth_method": endpoint.ssh_auth_method,
            "secret_source": endpoint.ssh_secret_source,
            "password_enc": endpoint.ssh_password_enc,
            "password_ref": endpoint.ssh_password_ref,
            "private_key_enc": endpoint.ssh_private_key_enc,
            "private_key_ref": endpoint.ssh_private_key_ref,
            "key_passphrase_enc": endpoint.ssh_key_passphrase_enc,
            "key_passphrase_ref": endpoint.ssh_key_passphrase_ref,
        }
        identity = (endpoint.id, endpoint.host, port, endpoint.ssh_username)

    credentials = resolve_ssh_credentials(**secret_columns, environ=environ)

    endpoint_id_, host_, port_, username_ = identity
    return SshRuntimeSnapshot(
        endpoint_id=endpoint_id_,
        host=host_,
        port=port_,
        username=username_,
        host_key=host_key,
        credentials=credentials,
    )
