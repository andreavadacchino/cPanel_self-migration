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

import os
import stat as _stat

from adapters.cpanel.schemas import CpanelCredentials

LAB_TOKEN_MAX_BYTES = 4096


class LabCredentialError(RuntimeError):
    """Fail-closed credential error. NEVER contains the token value (only the path + reason)."""


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


def resolve_lab_token_after_gates(*, authorized: bool, token_file: str, repo_root: str,
                                  current_uid: int | None = None, loader=load_lab_token) -> str:
    """Gate-ordering guard: the token is read ONLY when the non-secret gates already passed. If
    ``authorized`` is False the ``loader`` is never invoked (fail closed before any file access)."""
    if not authorized:
        raise LabCredentialError("credential read refused: non-secret gates not satisfied")
    return loader(token_file, repo_root=repo_root, current_uid=current_uid)


def build_lab_credentials(env, *, authorized: bool, repo_root: str,
                          current_uid: int | None = None) -> CpanelCredentials:
    """Assemble ``CpanelCredentials`` for the lab from non-secret env (username/host/port) plus the
    file-loaded token. ``api_token`` carries ``repr=False`` upstream, so the token never renders in a
    repr. Fails closed on any missing field. TLS verification is always on."""
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
    token = resolve_lab_token_after_gates(authorized=authorized, token_file=token_file,
                                          repo_root=repo_root, current_uid=current_uid)
    return CpanelCredentials(host=host, username=username, api_token=token, port=port,
                             verify_tls=True)


__all__ = ["LAB_TOKEN_MAX_BYTES", "LabCredentialError", "load_lab_token",
           "resolve_lab_token_after_gates", "build_lab_credentials"]
