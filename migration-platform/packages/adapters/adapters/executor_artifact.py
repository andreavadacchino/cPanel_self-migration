"""Stage the executor as a private artifact whose bytes are the verified bytes.

The defect this exists to remove: hashing a file and then handing its *path* to
``subprocess`` makes the kernel resolve that path a second time. Between the
verdict and the exec, the deployment directory can hand back a different file —
so the bytes that were verified and the bytes that run are not the same
artifact. Refusing a symlink at verification time does not help; the swap
happens after.

So the deployment path is treated as **input, not identity**: the source is
opened once, copied into a private directory while being hashed, and only that
copy is ever executed. Whatever happens to the source afterwards — replaced,
rewritten, deleted — is irrelevant, because nothing reads it again.

Why a copy rather than exec-by-descriptor: ``fexecve``/``/proc/self/fd`` is
Linux-only (the platform is developed on macOS too), does not work for a
script's shebang, and would push descriptor lifetime into every future caller.
A private copy is portable, needs no ``pass_fds`` in the future actor, and the
artifact is a real path the executor can be handed like any other.

The copy is hashed **as it is written**, not before or after: the source may
change mid-copy, and that is fine — the digest describes exactly the bytes that
landed in the artifact, which are exactly the bytes that will run. A mismatch
removes everything.

This module runs no subprocess, opens no socket, reads no database. Errors name
the administrative path and never a digest, never file content.
"""

from __future__ import annotations

import contextlib
import hashlib
import hmac
import os
import re
import shutil
import stat
import tempfile
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path

__all__ = [
    "ARTIFACT_NAME",
    "ExecutorArtifactCleanupError",
    "ExecutorArtifactError",
    "ExecutorDeployment",
    "VerifiedExecutorArtifact",
    "prepare_verified_executor",
]

#: Constant, never derived from the source path, the digest or a row: a name is
#: a place an attacker-controlled string could otherwise steer a write.
ARTIFACT_NAME = "executor"

_WORKSPACE_PREFIX = "executor-"
_HEX_SHA256 = re.compile(r"^[0-9a-fA-F]{64}$")
_COPY_CHUNK = 1024 * 1024

#: Read + execute for the owner, nothing else. The artifact is finished the
#: moment it is verified; a writable artifact is one that can stop being the
#: thing that was verified.
_ARTIFACT_MODE = 0o500
_ROOT_MODE = 0o700


class ExecutorArtifactError(Exception):
    """The executor could not be staged as a verified artifact.

    Names the administrative path. Never the expected or actual digest — the
    operator re-computes those — and never file content.
    """


class ExecutorArtifactCleanupError(Exception):
    """The artifact exists but could not be removed.

    Distinct from "already gone", which is idempotent success: a real failure
    leaves an executable copy on disk and must not be silent.
    """


@dataclass(frozen=True)
class ExecutorDeployment:
    """The three inputs a launch needs, stated explicitly.

    Pure data, no environment lookup: this package must not decide where the
    binary lives. The worker will bind these when it grows a real consumer;
    until then, making the inputs explicit is the whole point.
    """

    source_path: Path
    expected_sha256: str
    runtime_root: Path


@dataclass(frozen=True)
class VerifiedExecutorArtifact:
    """A private copy whose bytes produced ``sha256``. The only thing to run.

    ``executable_path`` is the artifact. ``source_path`` is kept for audit and
    error messages only — nothing in this codebase may execute it, which is the
    entire reason the type exists. ``repr`` is safe: paths and a digest, no
    secret.
    """

    root: Path
    executable_path: Path
    source_path: Path
    sha256: str


def _check_runtime_root(root: Path) -> None:
    """Refuse a root that cannot hold a private artifact, before creating one."""
    if root.is_symlink():
        raise ExecutorArtifactError(f"executor runtime root is a symlink: {root}")
    try:
        st = os.stat(root, follow_symlinks=False)
    except OSError:
        raise ExecutorArtifactError(
            f"executor runtime root is unusable: {root}"
        ) from None
    if not stat.S_ISDIR(st.st_mode):
        raise ExecutorArtifactError(f"executor runtime root is not a directory: {root}")
    mode = stat.S_IMODE(st.st_mode)
    # Writable beyond the owner is acceptable only with the sticky bit — that is
    # /tmp's own 1777, where another user cannot rename our directory away.
    if mode & (stat.S_IWOTH | stat.S_IWGRP) and not mode & stat.S_ISVTX:
        raise ExecutorArtifactError(
            f"executor runtime root is writable beyond its owner without the "
            f"sticky bit: {root}"
        )
    if not os.access(root, os.W_OK | os.X_OK):
        raise ExecutorArtifactError(f"executor runtime root is not writable: {root}")


def _remove_artifact_root(root: Path) -> None:
    """Remove the artifact root, or say loudly that it is still there.

    Absence is idempotent success. Everything else is
    :class:`ExecutorArtifactCleanupError`: a root swapped for a symlink is
    refused without following it, a filesystem refusal is reported, and an
    rmtree that returns while the root still exists is a failure. Never
    ``ignore_errors``.
    """
    try:
        st = os.lstat(root)
    except FileNotFoundError:
        return
    except OSError:
        raise ExecutorArtifactCleanupError(
            f"executor artifact root could not be inspected: {root}"
        ) from None
    if stat.S_ISLNK(st.st_mode):
        raise ExecutorArtifactCleanupError(
            f"executor artifact root is now a symlink and was not followed: {root}"
        )
    try:
        shutil.rmtree(root)
    except FileNotFoundError:
        pass  # a concurrent removal won the race; the check below decides
    except OSError:
        raise ExecutorArtifactCleanupError(
            f"executor artifact root could not be removed: {root}"
        ) from None
    if os.path.lexists(root):
        raise ExecutorArtifactCleanupError(
            f"executor artifact root still exists after removal: {root}"
        )


def _write_all(fd: int, data: bytes) -> None:
    """Write every byte. ``os.write`` may write fewer than asked, and a short
    write silently truncating the artifact would mean hashing bytes that are not
    the bytes on disk."""
    offset = 0
    while offset < len(data):
        offset += os.write(fd, data[offset:])


def _copy_and_hash(source_fd: int, dest_fd: int) -> str:
    """Copy source to dest, hashing exactly what is written. Returns the digest.

    One pass: the digest cannot describe anything other than the artifact's
    content, so a source that changes mid-copy produces a digest that will not
    match the pin — and the artifact is removed — rather than a digest of bytes
    nobody kept.
    """
    digest = hashlib.sha256()
    while True:
        chunk = os.read(source_fd, _COPY_CHUNK)
        if not chunk:
            break
        digest.update(chunk)
        _write_all(dest_fd, chunk)
    return digest.hexdigest()


@contextlib.contextmanager
def prepare_verified_executor(
    deployment: ExecutorDeployment,
) -> Iterator[VerifiedExecutorArtifact]:
    """Stage the pinned executor privately and yield the artifact to run.

    A context manager on purpose: the artifact is an executable copy, so
    "forgot to clean up" must not be reachable through ordinary use. Every exit
    path removes the whole root.

        with prepare_verified_executor(deployment) as artifact:
            caps = run_capabilities_handshake(artifact)
            ensure_compatible(caps, ...)
            # the future execute runs THIS artifact — never the source again

    Raises :class:`ExecutorArtifactError` when the source cannot be staged or
    does not match its pin (leaving the runtime root untouched), and
    :class:`ExecutorArtifactCleanupError` when the artifact cannot be removed.
    """
    if not _HEX_SHA256.match(deployment.expected_sha256):
        raise ExecutorArtifactError(
            "the expected executor digest is not a 64-character hex SHA-256"
        )
    expected = deployment.expected_sha256.lower()

    runtime_root = Path(deployment.runtime_root)
    _check_runtime_root(runtime_root)

    source = Path(deployment.source_path)
    # One atomic open: O_NOFOLLOW refuses a symlinked source, and every question
    # afterwards is answered from THIS descriptor. The path is never resolved
    # again — not here, and not at exec, because the artifact is what runs.
    try:
        source_fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW)
    except OSError:
        raise ExecutorArtifactError(
            f"executor source does not exist, is a symlink, or cannot be opened: {source}"
        ) from None

    root: Path | None = None
    try:
        st = os.fstat(source_fd)
        if not stat.S_ISREG(st.st_mode):
            raise ExecutorArtifactError(
                f"executor source is not a regular file: {source}"
            )
        # The mode from this descriptor, never os.access on the path: access()
        # would re-resolve the path and describe a file we are not copying.
        if not st.st_mode & 0o111:
            raise ExecutorArtifactError(f"executor source is not executable: {source}")

        root = Path(tempfile.mkdtemp(prefix=_WORKSPACE_PREFIX, dir=runtime_root))
        os.chmod(root, _ROOT_MODE)  # mkdtemp is already 0700; make it explicit
        artifact_path = root / ARTIFACT_NAME

        # O_EXCL: we create it, never open something already there. O_NOFOLLOW:
        # a planted symlink cannot redirect the copy. 0600 while writing; the
        # execute bit arrives only once the digest matches.
        dest_fd = os.open(
            artifact_path,
            os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW,
            0o600,
        )
        try:
            actual = _copy_and_hash(source_fd, dest_fd)
            os.fsync(dest_fd)
            if not hmac.compare_digest(actual, expected):
                raise ExecutorArtifactError(
                    f"executor source does not match the pinned digest: {source}"
                )
            # Read + execute, and not writable: from here the artifact is what
            # it was verified to be, and nothing can quietly make it otherwise.
            os.fchmod(dest_fd, _ARTIFACT_MODE)
        finally:
            os.close(dest_fd)

        artifact = VerifiedExecutorArtifact(
            root=root,
            executable_path=artifact_path,
            source_path=source,
            sha256=actual,
        )
    except BaseException as primary:
        os.close(source_fd)
        if root is None:
            raise
        # A half-staged artifact is still an executable copy on disk. Removing
        # it must neither be skipped nor silently replace the reason we are
        # here: if the removal fails too, both stay observable.
        try:
            _remove_artifact_root(root)
        except ExecutorArtifactCleanupError as cleanup_failure:
            raise BaseExceptionGroup(
                "staging the executor artifact failed and its removal failed too",
                [primary, cleanup_failure],
            ) from None
        raise
    os.close(source_fd)

    try:
        yield artifact
    except BaseException as primary:
        try:
            _remove_artifact_root(artifact.root)
        except ExecutorArtifactCleanupError as cleanup_failure:
            raise BaseExceptionGroup(
                "the executor artifact failed and its removal failed too",
                [primary, cleanup_failure],
            ) from None
        raise
    _remove_artifact_root(artifact.root)
