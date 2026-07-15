"""R2-c4-LAB-GATEWAY — TEST-ONLY secure loader for the disposable-lab cPanel token.

Investigation result: the repo has NO Vault/secret-manager resolver (only ``credentials.py``'s
Fernet at-rest encryption keyed by ``CREDENTIAL_ENCRYPTION_KEY``, which is application encryption —
NOT a secret-injection mechanism, and must never be reused as a cPanel token). So the live lab token
is read from a strictly-protected local file kept OUTSIDE the repository. The token is read ONLY
after the non-secret gates pass, is held no longer than needed, and never appears in a CLI arg, an
environment variable, a log, an error, a repr, or a report.

Lives under ``app/tests/live`` so no runtime module imports it.
"""
from __future__ import annotations

import hashlib
import os
import secrets as _secrets
import stat as _stat
from collections.abc import Mapping

from adapters.cpanel.schemas import CpanelCredentials

LAB_TOKEN_MAX_BYTES = 4096

# Module-private sentinel: the receipt constructor is unreachable without it, so no caller can
# fabricate a "valid" receipt by hand — a receipt exists ONLY if ``issue_connection_receipt`` minted
# it after every static gate passed.
_RECEIPT_SENTINEL = object()


class LabCredentialError(RuntimeError):
    """Fail-closed credential error. NEVER contains the token value (only the path + reason)."""


def endpoint_fingerprint(endpoint: str) -> str:
    """Stable, non-reversible fingerprint of a normalized endpoint. Used everywhere the raw
    endpoint must NOT appear (receipt repr, write-authorization binding, sanitized report)."""
    return "epfp:" + hashlib.sha256((endpoint or "").strip().lower().encode()).hexdigest()[:16]


class LabConnectionGateReceipt:
    """Opaque proof that EVERY static safety gate passed. Replaces the falsifiable ``authorized``
    boolean. Binds the run to a commit, endpoint (as a fingerprint), disposable domain, TTL and a
    session nonce. Holds NO secret (no token, no raw endpoint beyond the fingerprint), and its repr
    never leaks the raw endpoint, the username, or a credential."""

    __slots__ = ("_commit", "_endpoint_fp", "_domain", "_issued_at", "_expires_at", "_session")

    def __init__(self, sentinel, *, commit: str, endpoint: str, endpoint_fingerprint: str,
                 disposable_domain: str, issued_at: float, expires_at: float, session_nonce: str):
        if sentinel is not _RECEIPT_SENTINEL:
            raise LabCredentialError(
                "connection receipt constructor is private; use issue_connection_receipt")
        if not (commit and endpoint and endpoint_fingerprint and disposable_domain and session_nonce):
            raise LabCredentialError("connection receipt refused: incomplete binding")
        if expires_at <= issued_at:
            raise LabCredentialError("connection receipt refused: non-positive ttl")
        self._commit = commit
        self._endpoint_fp = endpoint_fingerprint
        self._domain = disposable_domain.strip().lower()
        self._issued_at = float(issued_at)
        self._expires_at = float(expires_at)
        self._session = session_nonce

    # non-secret binding, exposed read-only so the gateway/wiring can mint matching contexts
    @property
    def commit(self) -> str:
        return self._commit

    @property
    def endpoint_fingerprint(self) -> str:
        return self._endpoint_fp

    @property
    def disposable_domain(self) -> str:
        return self._domain

    @property
    def session_nonce(self) -> str:
        return self._session

    def valid_at(self, now: float) -> bool:
        return float(now) < self._expires_at

    def _binds(self, *, now: float, commit: str, endpoint: str, domain: str) -> bool:
        return (self.valid_at(now) and self._commit == commit
                and self._endpoint_fp == endpoint_fingerprint(endpoint)
                and self._domain == (domain or "").strip().lower())

    def __repr__(self) -> str:
        return (f"LabConnectionGateReceipt(endpoint={self._endpoint_fp!r}, "
                f"domain={self._domain!r}, session={self._session!r})")


def issue_connection_receipt(*, gates: Mapping[str, bool], commit: str, endpoint: str,
                             disposable_domain: str, issued_at: float, ttl_seconds: float,
                             session_nonce: str | None = None) -> LabConnectionGateReceipt:
    """Mint a receipt ONLY when every static gate is satisfied; otherwise fail closed. The endpoint
    is normalized and reduced to a fingerprint before it is ever stored."""
    missing = sorted(k for k, v in dict(gates).items() if not v)
    if missing:
        raise LabCredentialError(f"connection refused: gate(s) not satisfied: {missing}")
    endpoint = (endpoint or "").strip()
    if not (commit and endpoint and disposable_domain):
        raise LabCredentialError("connection refused: incomplete binding")
    if ttl_seconds <= 0:
        raise LabCredentialError("connection refused: non-positive ttl")
    nonce = session_nonce or _secrets.token_hex(16)
    return LabConnectionGateReceipt(
        _RECEIPT_SENTINEL, commit=commit, endpoint=endpoint,
        endpoint_fingerprint=endpoint_fingerprint(endpoint),
        disposable_domain=disposable_domain, issued_at=float(issued_at),
        expires_at=float(issued_at) + float(ttl_seconds), session_nonce=nonce)


def _within(path: str, root: str) -> bool:
    try:
        common = os.path.commonpath([os.path.realpath(path), os.path.realpath(root)])
    except ValueError:
        return False
    return common == os.path.realpath(root)


def load_lab_token(token_file: str, *, repo_root: str, current_uid: int | None = None) -> str:
    """Read the lab token from ``token_file`` enforcing every protection, or fail closed.

    Requirements: absolute path; OUTSIDE the repo; a real regular file (no symlink, no dir); mode
    ``0600`` or stricter (no group/other bits); owned by the current process user; non-empty; not
    larger than ``LAB_TOKEN_MAX_BYTES``. The token is never echoed in an error."""
    if not isinstance(token_file, str) or not token_file:
        raise LabCredentialError("token file path is missing")
    if not os.path.isabs(token_file):
        raise LabCredentialError("token file path must be absolute")
    if _within(token_file, repo_root):
        raise LabCredentialError("token file must live OUTSIDE the repository")
    try:
        lst = os.lstat(token_file)  # do NOT follow symlinks
    except OSError:
        raise LabCredentialError("token file does not exist or is unreadable") from None
    if _stat.S_ISLNK(lst.st_mode):
        raise LabCredentialError("token file must not be a symlink")
    if not _stat.S_ISREG(lst.st_mode):
        raise LabCredentialError("token file must be a regular file")
    if _stat.S_IMODE(lst.st_mode) & 0o077:
        raise LabCredentialError("token file permissions too open; require 0600 or stricter")
    uid = os.getuid() if current_uid is None else current_uid
    if lst.st_uid != uid:
        raise LabCredentialError("token file owner does not match the current process user")
    if lst.st_size == 0:
        raise LabCredentialError("token file is empty")
    if lst.st_size > LAB_TOKEN_MAX_BYTES:
        raise LabCredentialError("token file is larger than the allowed maximum")
    try:
        with open(token_file, "r", encoding="utf-8") as fh:
            raw = fh.read(LAB_TOKEN_MAX_BYTES + 1)
    except OSError:
        raise LabCredentialError("token file could not be read") from None
    token = raw.strip()
    if not token:
        raise LabCredentialError("token file contains no token after trimming")
    return token


def resolve_lab_token(receipt, token_file_path: str, *, repo_root: str, now: float,
                      expected_commit: str, expected_endpoint: str, expected_domain: str,
                      current_uid: int | None = None, loader=load_lab_token) -> str:
    """Read the lab token ONLY when a genuine, unexpired receipt binds the exact commit + endpoint +
    domain of this run. A missing/forged/expired/mismatched receipt fails closed BEFORE the loader
    runs, so the token file is never opened, no client is built, and no network occurs."""
    if not isinstance(receipt, LabConnectionGateReceipt):
        raise LabCredentialError("credential read refused: a valid connection receipt is required")
    if not receipt._binds(now=now, commit=expected_commit, endpoint=expected_endpoint,
                          domain=expected_domain):
        raise LabCredentialError(
            "credential read refused: receipt does not bind this run or is expired")
    return loader(token_file_path, repo_root=repo_root, current_uid=current_uid)


def build_lab_credentials(env, receipt, *, repo_root: str, now: float, expected_commit: str,
                          expected_endpoint: str, expected_domain: str,
                          current_uid: int | None = None) -> CpanelCredentials:
    """Assemble ``CpanelCredentials`` for the lab from non-secret env (username/host/port) plus the
    receipt-gated file-loaded token. ``api_token`` carries ``repr=False`` upstream, so the token
    never renders in a repr. Fails closed on any missing field. TLS verification is always on."""
    username = (env.get("CPANEL_TEST_USERNAME") or "").strip()
    host = (env.get("CPANEL_TEST_API_HOST") or "").strip()
    token_file = (env.get("CPANEL_TEST_TOKEN_FILE") or "").strip()
    if not username:
        raise LabCredentialError("CPANEL_TEST_USERNAME is required")
    if not host:
        raise LabCredentialError("CPANEL_TEST_API_HOST is required")
    try:
        port = int(env.get("CPANEL_TEST_API_PORT") or 2083)
    except (TypeError, ValueError):
        raise LabCredentialError("CPANEL_TEST_API_PORT must be an integer") from None
    token = resolve_lab_token(receipt, token_file, repo_root=repo_root, now=now,
                              expected_commit=expected_commit, expected_endpoint=expected_endpoint,
                              expected_domain=expected_domain, current_uid=current_uid)
    return CpanelCredentials(host=host, username=username, api_token=token, port=port,
                             verify_tls=True)


__all__ = ["LAB_TOKEN_MAX_BYTES", "LabCredentialError", "endpoint_fingerprint",
           "LabConnectionGateReceipt", "issue_connection_receipt", "load_lab_token",
           "resolve_lab_token", "build_lab_credentials"]
