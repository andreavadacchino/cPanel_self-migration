"""Resolve an endpoint's SSH credentials into memory, fail-closed.

The database cannot express the relationship between ``ssh_auth_method``,
``ssh_secret_source`` and the six secret columns — the CHECKs cover the enum, the
port range and "``none`` is empty", nothing more (see ``endpoints.__table_args__``
and CURRENT_STATE's "la coerenza direct/ref del segreto SSH NON è un vincolo DB").
The Pydantic bundle that enforces coherence runs on *write*, never on a read. So a
row written outside the API can claim ``password``/``direct`` and carry no
ciphertext, carry a private key instead, or carry a ciphertext *and* a ref. This
module refuses every such row **before** anything is decrypted or materialized.

Boundary: pure. No ORM, no database, no filesystem, no network, no subprocess.
The caller reads the row (that is the loader's job, under a lock) and hands the
fields here; the workspace builder receives only what this returns.

Secrets live in :class:`SshCredentials`, excluded from ``repr`` and never placed
in an error. Errors are generic and cut their cause with ``from None``: CI uploads
the JUnit XML, and pytest serializes failure text into it, so material in an
exception is material in an artifact.

Python strings cannot be reliably zeroized — the mitigation is to keep secrets
short-lived, uncopied, out of reprs and off disk except in the private workspace.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field

from adapters.credentials import CredentialError, resolve_credential
from adapters.crypto import SecretDecryptError, SecretKeyError, decrypt_secret
from adapters.ssh_host_keys import ParsedHostKey
from adapters.ssh_keys import InvalidPrivateKey, load_private_key_or_raise

__all__ = [
    "AUTH_METHOD_PASSWORD",
    "AUTH_METHOD_PRIVATE_KEY",
    "SOURCE_DIRECT",
    "SOURCE_REF",
    "SshCredentials",
    "SshRuntimeConfigurationError",
    "SshRuntimeSnapshot",
    "SshSecretResolutionError",
    "resolve_ssh_credentials",
]

# Mirrors app.modules.endpoints.models.SshAuthMethod / SshSecretSource. Duplicated
# as plain strings on purpose: this package must not import the FastAPI app (the
# worker imports adapters, never app). The DB CHECK is the shared authority on the
# allowed values, and a value outside these is refused rather than guessed.
AUTH_METHOD_NONE = "none"
AUTH_METHOD_PASSWORD = "password"
AUTH_METHOD_PRIVATE_KEY = "private_key"
SOURCE_DIRECT = "direct"
SOURCE_REF = "ref"


class SshRuntimeConfigurationError(Exception):
    """The stored SSH configuration is unusable or internally incoherent.

    Names the offending *field*, never a value: a field name is configuration the
    operator already knows; a value could be a secret.
    """


class SshSecretResolutionError(Exception):
    """A declared secret could not be resolved into a usable value.

    Covers a failed decrypt, an unsupported/missing reference and a value that
    resolves to nothing usable. Deliberately one verdict: distinguishing "wrong
    key" from "corrupt ciphertext" tells a caller about the material.
    """


@dataclass(frozen=True)
class SshCredentials:
    """Resolved SSH credentials, in memory only.

    ``auth_method`` is safe to show; the three secrets are excluded from ``repr``
    so a stray log line, an f-string or a pytest assertion cannot print them. The
    class exposes no serializer for the same reason — there is deliberately no
    ``to_dict``/``model_dump`` that a caller could hand to a logger.
    """

    auth_method: str
    password: str | None = field(default=None, repr=False)
    private_key: str | None = field(default=None, repr=False)
    passphrase: str | None = field(default=None, repr=False)


@dataclass(frozen=True)
class SshRuntimeSnapshot:
    """One endpoint's SSH runtime identity, coherent at the moment it was read.

    Assembled by the loader inside a single locked read, so ``host``/``port`` and
    ``host_key`` cannot disagree: a concurrent coordinate change either lands
    before the read (and the pin is already gone) or after it.

    ``host_key`` is a :class:`~adapters.ssh_host_keys.ParsedHostKey` — the object
    ``validate_persisted_host_key`` returns. Carrying the *proof* rather than the
    three raw columns is what makes an unvalidated pin unrepresentable here, so
    the workspace builder can write a ``known_hosts`` without re-deciding trust.

    Not persisted, and no timestamp anchor: ``host``, ``port`` and the key's
    fingerprint *are* the anchor. The executor that will one day start a
    subprocess must re-read endpoint + pin and re-run the same validation
    immediately before launching, and refuse a snapshot that has drifted. This
    object records a past truth; it authorizes nothing.
    """

    endpoint_id: int
    host: str
    port: int
    username: str
    host_key: ParsedHostKey
    credentials: SshCredentials

    @property
    def auth_method(self) -> str:
        return self.credentials.auth_method


def _require_absent(name: str, value: str | None) -> None:
    """A field belonging to a different method/source must be empty."""
    if value is not None and value != "":
        raise SshRuntimeConfigurationError(
            f"{name} is set but does not belong to this SSH method/source"
        )


def _resolve_one(
    field_name: str,
    *,
    source: str,
    ciphertext: str | None,
    reference: str | None,
    environ: Mapping[str, str] | None,
) -> str:
    """Resolve exactly one declared secret for ``source``, or refuse.

    Coherence is decided *before* resolution: exactly the column matching the
    declared source must be populated, and the other must not. A row carrying both
    is ambiguous — it is refused rather than resolved by precedence.
    """
    if source == SOURCE_DIRECT:
        _require_absent(f"{field_name}_ref", reference)
        if not ciphertext:
            raise SshRuntimeConfigurationError(
                f"{field_name}_enc is required when the secret source is 'direct'"
            )
        try:
            value = decrypt_secret(ciphertext)
        except (SecretDecryptError, SecretKeyError):
            # from None: the cause carries the ciphertext in some code paths, and
            # this verdict is a single generic fact either way.
            raise SshSecretResolutionError(
                f"{field_name} could not be decrypted"
            ) from None
    else:
        _require_absent(f"{field_name}_enc", ciphertext)
        if not reference:
            raise SshRuntimeConfigurationError(
                f"{field_name}_ref is required when the secret source is 'ref'"
            )
        if not reference.strip():
            raise SshRuntimeConfigurationError(f"{field_name}_ref is blank")
        try:
            # The one supported provider set, reused as-is from Sprint 2: env://
            # with the uppercase-identifier-containing-CPANEL allowlist. This PR
            # adds no provider and widens no allowlist.
            value = resolve_credential(reference, environ=environ)
        except CredentialError:
            # Covers CredentialNotFound and CredentialResolverNotImplemented too.
            # from None: never chain a message built from caller-supplied text.
            raise SshSecretResolutionError(
                f"{field_name}_ref could not be resolved"
            ) from None

    if not value.strip():
        raise SshSecretResolutionError(f"{field_name} resolved to an empty value")
    return value


def resolve_ssh_credentials(
    *,
    auth_method: str,
    secret_source: str | None,
    password_enc: str | None = None,
    password_ref: str | None = None,
    private_key_enc: str | None = None,
    private_key_ref: str | None = None,
    key_passphrase_enc: str | None = None,
    key_passphrase_ref: str | None = None,
    environ: Mapping[str, str] | None = None,
) -> SshCredentials:
    """Validate a stored SSH credential row and resolve it, fail-closed.

    Raises :class:`SshRuntimeConfigurationError` when the row cannot be used as
    declared (``none``, unknown method/source, a missing or a foreign field), and
    :class:`SshSecretResolutionError` when a declared secret will not resolve.
    Nothing is written anywhere; the values exist only in the returned object.

    A passphrase is optional and must use the *same* source as the key: a row
    mixing sources is incoherent, not a convenience. Its presence is not taken as
    proof that the key is encrypted — that is the key parser's verdict, reached
    here through the shared ``load_private_key_or_raise`` and, ultimately, the Go
    engine's at dial time.
    """
    if auth_method == AUTH_METHOD_NONE:
        raise SshRuntimeConfigurationError(
            "the endpoint has no SSH configured (ssh_auth_method is 'none')"
        )
    if auth_method not in (AUTH_METHOD_PASSWORD, AUTH_METHOD_PRIVATE_KEY):
        raise SshRuntimeConfigurationError("ssh_auth_method is not a known method")
    if secret_source not in (SOURCE_DIRECT, SOURCE_REF):
        raise SshRuntimeConfigurationError("ssh_secret_source is not a known source")

    if auth_method == AUTH_METHOD_PASSWORD:
        # Every private-key field is foreign to a password row, whatever its source.
        _require_absent("ssh_private_key_enc", private_key_enc)
        _require_absent("ssh_private_key_ref", private_key_ref)
        _require_absent("ssh_key_passphrase_enc", key_passphrase_enc)
        _require_absent("ssh_key_passphrase_ref", key_passphrase_ref)
        password = _resolve_one(
            "ssh_password",
            source=secret_source,
            ciphertext=password_enc,
            reference=password_ref,
            environ=environ,
        )
        return SshCredentials(auth_method=auth_method, password=password)

    _require_absent("ssh_password_enc", password_enc)
    _require_absent("ssh_password_ref", password_ref)
    private_key = _resolve_one(
        "ssh_private_key",
        source=secret_source,
        ciphertext=private_key_enc,
        reference=private_key_ref,
        environ=environ,
    )

    passphrase: str | None = None
    declared = key_passphrase_enc if secret_source == SOURCE_DIRECT else key_passphrase_ref
    foreign = key_passphrase_ref if secret_source == SOURCE_DIRECT else key_passphrase_enc
    # A passphrase from the other source is an incoherent row, not a fallback.
    _require_absent(
        "ssh_key_passphrase_ref" if secret_source == SOURCE_DIRECT else "ssh_key_passphrase_enc",
        foreign,
    )
    if declared:
        passphrase = _resolve_one(
            "ssh_key_passphrase",
            source=secret_source,
            ciphertext=key_passphrase_enc,
            reference=key_passphrase_ref,
            environ=environ,
        )

    # Prove the material is a usable key here, where the error is generic, rather
    # than discovering it in the engine's stderr after a subprocess has started.
    try:
        load_private_key_or_raise(private_key, passphrase)
    except InvalidPrivateKey:
        raise SshSecretResolutionError(
            "the stored private key could not be parsed, or the passphrase does "
            "not match it"
        ) from None

    return SshCredentials(
        auth_method=auth_method, private_key=private_key, passphrase=passphrase
    )
