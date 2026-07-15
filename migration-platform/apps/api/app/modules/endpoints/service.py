"""Persistence + mock connection logic for endpoints."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import delete, select
from sqlalchemy.orm import Session

from adapters.credentials import (
    CredentialError,
    CredentialNotFound,
    CredentialResolverNotImplemented,
    resolve_credential,
)
from adapters.crypto import SecretDecryptError, SecretKeyError, decrypt_secret, encrypt_secret
from adapters.inventory import build_inventory_source
from adapters.ssh_host_keys import (
    InvalidHostKey,
    InvalidPersistedHostKey,
    ParsedHostKey,
    parse_host_key,
    validate_persisted_host_key,
)
from app.core.errors import ConflictError, NotFoundError, UnprocessableError
from app.modules.endpoints.models import (
    AuthType,
    ConnectionStatus,
    Endpoint,
    EndpointSshHostKey,
    SshAuthMethod,
    SshSecretSource,
)
from app.modules.endpoints.schemas import (
    EndpointCreate,
    EndpointUpdate,
    SshCredentialBundle,
)
from app.modules.migrations.service import get_migration


def list_endpoints(db: Session, migration_id: int) -> list[Endpoint]:
    # Validate the parent exists so the caller gets a 404, not an empty list.
    get_migration(db, migration_id)
    return list(
        db.execute(
            select(Endpoint)
            .where(Endpoint.migration_id == migration_id)
            .order_by(Endpoint.id)
        ).scalars()
    )


def create_endpoint(
    db: Session, migration_id: int, payload: EndpointCreate
) -> Endpoint:
    get_migration(db, migration_id)  # 404 if the migration is missing
    endpoint = Endpoint(
        migration_id=migration_id,
        role=payload.role.value,
        label=payload.label,
        host=payload.host,
        port=payload.port,
        username=payload.username,
        auth_type=payload.auth_type.value,
        auth_ref=payload.auth_ref,
        verify_tls=payload.verify_tls,
    )
    # A directly-entered token is encrypted at rest; the plaintext is dropped.
    if payload.auth_type == AuthType.TOKEN and payload.token:
        endpoint.auth_secret_enc = _encrypt_token(payload.token)
    db.add(endpoint)
    db.commit()
    db.refresh(endpoint)
    return endpoint


def _encrypt_token(token: str) -> str:
    try:
        return encrypt_secret(token)
    except SecretKeyError as exc:
        # Misconfiguration (no master key) → 422, not a silent 500. Never echoes
        # the token.
        raise UnprocessableError(str(exc)) from exc


# The SSH secret columns, so a method/source change clears every one that no
# longer applies. Listed once, cleared as a set — a stray leftover ciphertext
# would be a credential nobody can see but the worker would still use.
_SSH_SECRET_COLUMNS = (
    "ssh_password_enc",
    "ssh_private_key_enc",
    "ssh_key_passphrase_enc",
    "ssh_password_ref",
    "ssh_private_key_ref",
    "ssh_key_passphrase_ref",
)


def set_ssh_credentials(
    db: Session, endpoint_id: int, bundle: SshCredentialBundle
) -> Endpoint:
    """Replace an endpoint's SSH credential as a unit.

    Distinct from the cPanel token: this touches only the ssh_* columns and
    leaves auth_type/auth_secret_enc alone. The bundle is validated whole, so the
    method and its one secret are always consistent; here we only encrypt the
    direct secrets and store the refs verbatim. Every ssh_* column not used by
    the chosen method is cleared, so a switch never leaves an orphan ciphertext.

    ``connection_status``/``capabilities``/``last_*`` describe the cPanel TOKEN
    probe, a DISTINCT capability — they are deliberately left untouched. Rotating
    an SSH key must not turn a connected cPanel endpoint into ``unknown`` or drop
    valid capabilities. The SSH connection's own verdict arrives with separate
    fields in the runtime PR.
    """
    endpoint = _lock_endpoint(db, endpoint_id)  # 404 if missing; serialize the pin
    old_ssh_port = endpoint.ssh_port

    # Clear the slate: every SSH secret column, plus source. Coordinates
    # (username/port) are set below only when a method is chosen.
    for column in _SSH_SECRET_COLUMNS:
        setattr(endpoint, column, None)
    endpoint.ssh_auth_method = bundle.auth_method.value
    endpoint.ssh_secret_source = (
        bundle.secret_source.value if bundle.secret_source is not None else None
    )
    endpoint.ssh_username = bundle.username
    endpoint.ssh_port = None

    if bundle.auth_method != SshAuthMethod.NONE:
        endpoint.ssh_port = bundle.port if bundle.port is not None else 22
        if bundle.secret_source == SshSecretSource.DIRECT:
            _apply_direct_ssh_secret(endpoint, bundle)
        else:  # REF
            _apply_ref_ssh_secret(endpoint, bundle)

    # The pin is bound to host + ssh_port. Clearing SSH ('none') or moving the
    # SSH port invalidates it — the key belongs to the old coordinates. Rotating
    # the secret, source, or username while host and port stay the same does NOT:
    # the host key is the server's identity, independent of how we authenticate.
    if bundle.auth_method == SshAuthMethod.NONE or endpoint.ssh_port != old_ssh_port:
        _delete_host_key_for_endpoint(db, endpoint_id)

    db.add(endpoint)
    db.commit()
    db.refresh(endpoint)
    return endpoint


def _apply_direct_ssh_secret(endpoint: Endpoint, bundle: SshCredentialBundle) -> None:
    """Encrypt the direct secret(s) at rest. The plaintext is dropped here — it
    reaches neither the DB nor a log; only the ciphertext is assigned."""
    if bundle.auth_method == SshAuthMethod.PASSWORD:
        endpoint.ssh_password_enc = _encrypt_token(bundle.password or "")
    else:  # PRIVATE_KEY
        endpoint.ssh_private_key_enc = _encrypt_token(bundle.private_key or "")
        if bundle.key_passphrase:
            endpoint.ssh_key_passphrase_enc = _encrypt_token(bundle.key_passphrase)


def _apply_ref_ssh_secret(endpoint: Endpoint, bundle: SshCredentialBundle) -> None:
    """Store the opaque reference(s) verbatim — a pointer, never a value."""
    if bundle.auth_method == SshAuthMethod.PASSWORD:
        endpoint.ssh_password_ref = bundle.password_ref
    else:  # PRIVATE_KEY
        endpoint.ssh_private_key_ref = bundle.private_key_ref
        endpoint.ssh_key_passphrase_ref = bundle.key_passphrase_ref


def get_endpoint(db: Session, endpoint_id: int) -> Endpoint:
    endpoint = db.get(Endpoint, endpoint_id)
    if endpoint is None:
        raise NotFoundError("Endpoint", endpoint_id)
    return endpoint


def _lock_endpoint(db: Session, endpoint_id: int) -> Endpoint:
    """Load the endpoint row with ``FOR UPDATE`` so the coordinate-changing
    operations and the host-key pin serialize on the same row.

    Pinning a host key, changing ``host``, and changing ``ssh_port`` all take
    this lock, so they cannot interleave into a pin bound to coordinates the
    endpoint no longer has (see the concurrency tests). SQLite ignores
    ``FOR UPDATE``; the property is proven on real PostgreSQL.
    """
    endpoint = db.execute(
        select(Endpoint).where(Endpoint.id == endpoint_id).with_for_update()
    ).scalar_one_or_none()
    if endpoint is None:
        raise NotFoundError("Endpoint", endpoint_id)
    return endpoint


def _get_host_key_row(db: Session, endpoint_id: int) -> EndpointSshHostKey | None:
    return db.execute(
        select(EndpointSshHostKey).where(
            EndpointSshHostKey.endpoint_id == endpoint_id
        )
    ).scalar_one_or_none()


def _delete_host_key_for_endpoint(db: Session, endpoint_id: int) -> None:
    """Remove an endpoint's host-key pin, if any, WITHOUT committing.

    Composes into the caller's transaction: the DELETE route commits it on its
    own, and the invalidation hooks (host / ssh_port change) commit it in the
    same transaction as the coordinate change — so a pin is never orphaned
    across two transactions, and never survives a coordinate it no longer
    belongs to. A no-op when no pin exists (idempotent).
    """
    db.execute(
        delete(EndpointSshHostKey).where(
            EndpointSshHostKey.endpoint_id == endpoint_id
        )
    )


def update_endpoint(
    db: Session, endpoint_id: int, payload: EndpointUpdate
) -> Endpoint:
    """Edit an endpoint's coordinates/credentials. Any config change forces a
    re-test by clearing the previous connection status/capabilities."""
    endpoint = _lock_endpoint(db, endpoint_id)  # 404 if missing; serialize the pin
    # Whether the host changed decides if the pinned host key is still valid. The
    # comparison is normalized-to-normalized: payload.host is already cleaned by
    # the schema validator, and endpoint.host was stored cleaned.
    host_changed = endpoint.host != payload.host
    endpoint.label = payload.label
    endpoint.host = payload.host
    endpoint.port = payload.port
    endpoint.username = payload.username
    endpoint.auth_type = payload.auth_type.value
    endpoint.verify_tls = payload.verify_tls

    if payload.auth_type == AuthType.TOKEN:
        endpoint.auth_ref = None
        if payload.token:
            endpoint.auth_secret_enc = _encrypt_token(payload.token)
        elif endpoint.auth_secret_enc is None:
            # Switching to 'token' with no token and none stored is not usable.
            raise UnprocessableError("token is required for auth_type 'token'")
        # else: keep the existing encrypted token.
    elif payload.auth_type == AuthType.TOKEN_REF:
        endpoint.auth_ref = payload.auth_ref
        endpoint.auth_secret_enc = None
    else:  # mock | none | password_ref
        endpoint.auth_ref = payload.auth_ref
        endpoint.auth_secret_enc = None

    endpoint.connection_status = ConnectionStatus.UNKNOWN.value
    endpoint.last_error = None
    endpoint.capabilities = None
    endpoint.last_checked_at = None
    # A changed host means a different server: the pinned host key no longer
    # belongs to it, so invalidate it in the SAME transaction. A changed cPanel
    # port / username / label / token / TLS flag does NOT touch the SSH identity.
    if host_changed:
        _delete_host_key_for_endpoint(db, endpoint_id)
    db.add(endpoint)
    db.commit()
    db.refresh(endpoint)
    return endpoint


def delete_endpoint(db: Session, endpoint_id: int) -> None:
    endpoint = get_endpoint(db, endpoint_id)  # 404 if missing
    db.delete(endpoint)
    db.commit()


def update_endpoint_credentials(
    db: Session, endpoint_id: int, token: str
) -> Endpoint:
    """Replace a directly-entered (time-limited) token. Re-encrypts and forces
    a re-test by clearing the previous connection status."""
    endpoint = get_endpoint(db, endpoint_id)
    if endpoint.auth_type != AuthType.TOKEN.value:
        raise ConflictError(
            "credential update applies only to auth_type 'token' endpoints"
        )
    endpoint.auth_secret_enc = _encrypt_token(token)
    endpoint.connection_status = ConnectionStatus.UNKNOWN.value
    endpoint.last_error = None
    endpoint.last_checked_at = None
    db.add(endpoint)
    db.commit()
    db.refresh(endpoint)
    return endpoint


def _probe_endpoint(endpoint: Endpoint) -> tuple[str, str | None, dict | None]:
    """Build the right inventory source and run a minimal connect/auth probe.

    ``mock`` uses the offline source; ``token_ref`` resolves the credential
    (only ``env://`` in Sprint 2) and calls the real cPanel UAPI. A missing env
    var is a recorded failure; an unimplemented resolver (vault://) is a 422.
    """
    # A direct token is decrypted only here, in memory, just before use.
    token: str | None = None
    if endpoint.auth_type == AuthType.TOKEN.value:
        try:
            token = decrypt_secret(endpoint.auth_secret_enc or "")
        except (SecretDecryptError, SecretKeyError) as exc:
            return ConnectionStatus.FAILED.value, str(exc), None

    try:
        source = build_inventory_source(
            auth_type=endpoint.auth_type,
            host=endpoint.host,
            port=endpoint.port,
            username=endpoint.username,
            auth_ref=endpoint.auth_ref,
            token=token,
            verify_tls=endpoint.verify_tls,
            resolver=resolve_credential,
        )
    except CredentialResolverNotImplemented as exc:
        raise UnprocessableError(str(exc)) from exc
    except CredentialNotFound as exc:
        # Missing env var → failed connection, not a 4xx. Names the var, not
        # the value (the value never existed).
        return ConnectionStatus.FAILED.value, str(exc), None
    except CredentialError as exc:
        raise UnprocessableError(str(exc)) from exc

    try:
        outcome = source.probe()
    finally:
        source.close()  # release the httpx client / socket promptly
    status = (
        ConnectionStatus.CONNECTED.value
        if outcome.connected and outcome.authenticated
        else ConnectionStatus.FAILED.value
    )
    return status, outcome.error, outcome.capabilities.model_dump()


def test_connection(db: Session, endpoint_id: int) -> Endpoint:
    """Probe the endpoint (mock or real cPanel) and persist the outcome."""
    endpoint = get_endpoint(db, endpoint_id)
    status, last_error, capabilities = _probe_endpoint(endpoint)

    endpoint.connection_status = status
    endpoint.last_error = last_error
    endpoint.capabilities = capabilities
    endpoint.last_checked_at = datetime.now(timezone.utc)

    db.add(endpoint)
    db.commit()
    db.refresh(endpoint)
    return endpoint


def has_both_endpoints(db: Session, migration_id: int) -> bool:
    """True when the migration has at least one source and one destination."""
    roles = set(
        db.execute(
            select(Endpoint.role).where(Endpoint.migration_id == migration_id)
        ).scalars()
    )
    return {"source", "destination"} <= roles


# --- SSH host key pin (persistence only) ------------------------------------


def validate_ssh_host_key_pin(
    endpoint: Endpoint, pin: EndpointSshHostKey
) -> ParsedHostKey:
    """The complete fail-closed check for a persisted pin: coordinates AND
    cryptographic integrity.

    Raises :class:`InvalidPersistedHostKey` when the pin's snapshot no longer
    matches the endpoint's SSH coordinates, OR when the stored key/type/
    fingerprint are not internally coherent (an unparsable or non-canonical key,
    a mismatched type, a fingerprint that does not derive from the key — states
    the format-only DB CHECKs allow). Returns the parsed key on success.

    The coordinate comparison is here (it needs the ORM rows); the crypto proof
    is delegated to the pure adapter, which the future SSH runtime will call the
    same way — read endpoint + pin consistently, check host/port, run this, and
    only then materialize a known_hosts.
    """
    if pin.host != endpoint.host or pin.port != endpoint.ssh_port:
        raise InvalidPersistedHostKey(
            "pinned coordinates no longer match the endpoint"
        )
    return validate_persisted_host_key(
        public_key=pin.public_key,
        key_type=pin.key_type,
        fingerprint_sha256=pin.fingerprint_sha256,
    )


def get_ssh_host_key(db: Session, endpoint_id: int) -> EndpointSshHostKey:
    """Return the endpoint's host-key pin, fail-closed on a stale or corrupt one.

    404 when the endpoint or the pin is missing. A pin is also treated as no valid
    identity — uniformly 404, not distinguishing absent/stale/corrupt — when its
    snapshot no longer matches the endpoint's coordinates OR its stored key/type/
    fingerprint are not internally coherent (verifying host and port alone is not
    enough: the DB cannot prove a fingerprint was computed from the key). The
    corrupt row is left untouched — not deleted, rewritten or auto-corrected —
    so it stays available for administrative diagnosis; no material is logged or
    returned. This is an ordinary read: it takes no row lock (the runtime, with
    stronger transactional needs, will lock; see validate_ssh_host_key_pin).
    """
    endpoint = get_endpoint(db, endpoint_id)  # 404 if the endpoint is missing
    pin = _get_host_key_row(db, endpoint_id)
    if pin is None:
        raise NotFoundError("SSH host key", endpoint_id)
    try:
        validate_ssh_host_key_pin(endpoint, pin)
    except InvalidPersistedHostKey:
        # `from None`: the fail-closed verdict is uniform 404; never chain a cause
        # that could carry the stored material into a traceback.
        raise NotFoundError("SSH host key", endpoint_id) from None
    return pin


def set_ssh_host_key(
    db: Session, endpoint_id: int, public_key_material: str
) -> EndpointSshHostKey:
    """Pin (replace) the endpoint's SSH host key from submitted public material.

    Validates and canonicalizes the key BEFORE taking the row lock (no crypto
    under lock), then locks the endpoint row, derives host/port from it (never
    from the client), and upserts the single pin in one transaction. Requires SSH
    to be configured (a method other than 'none' and a port) — 409 otherwise. No
    probe, no connection: persistence only.
    """
    # Parse first: a bad key is a 422 that never touched the database or a lock.
    try:
        parsed = parse_host_key(public_key_material)
    except InvalidHostKey as exc:
        raise UnprocessableError(str(exc)) from exc

    endpoint = _lock_endpoint(db, endpoint_id)  # 404 if missing; serialize the pin
    if endpoint.ssh_auth_method == SshAuthMethod.NONE.value or endpoint.ssh_port is None:
        raise ConflictError(
            "SSH access must be configured (a method and port) before a host "
            "key can be pinned"
        )

    # Host and port are the server's, read under lock from the endpoint row.
    pin = _get_host_key_row(db, endpoint_id)
    if pin is None:
        pin = EndpointSshHostKey(endpoint_id=endpoint_id)
        db.add(pin)
    pin.host = endpoint.host
    pin.port = endpoint.ssh_port
    pin.key_type = parsed.key_type
    pin.public_key = parsed.public_key
    pin.fingerprint_sha256 = parsed.fingerprint_sha256
    db.commit()
    db.refresh(pin)
    return pin


def delete_ssh_host_key(db: Session, endpoint_id: int) -> None:
    """Remove the endpoint's host-key pin (idempotent).

    404 only when the endpoint itself is missing. When the endpoint exists, the
    call succeeds (204) whether or not a pin was present — DELETE expresses the
    desired end state ("no pin"), which is already true if none existed.

    Takes the endpoint row lock like the other pin mutations: without it, a
    concurrent ``set_ssh_host_key`` (which loads the pin ORM row and buffers an
    UPDATE under ``autoflush=False``) could have its row deleted here before it
    flushes, turning its commit into a ``StaleDataError`` / 500. The lock
    serializes the two, so the loser sees a clean state.
    """
    _lock_endpoint(db, endpoint_id)  # 404 if the endpoint is missing; serialize
    _delete_host_key_for_endpoint(db, endpoint_id)
    db.commit()
