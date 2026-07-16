"""Identify the executor binary, ask it what it can do, decide — fail-closed.

The compatibility handshake ADR-001 requires ("verificata prima dell'avvio,
non scoperta a metà run"), in three deliberately separate steps:

1. **Identity** (:func:`identify_executor_binary`) — the platform launches a
   *precise* binary, pinned by SHA-256 over its content, never "whatever is in
   PATH". The path must be a plain regular executable file: a symlink is
   refused, because the digest pins content but the *path* is what gets
   executed, and a path someone can re-point is not a pinned binary.
2. **Handshake** (:func:`run_capabilities_handshake`) — the one subprocess this
   module runs: ``<binary> capabilities``, which prints the
   executor-capabilities-v1 self-description and exits. Bounded on every side:
   argv only (no shell), stdin closed, a **stripped environment** (the worker's
   env legitimately carries ``*_CPANEL_*`` secrets for ``env://`` refs — none
   of it may reach the child), a timeout, and a cap on the answer's size. The
   answer is validated by :func:`domain.execution_contract.parse_capabilities`,
   the same validator the shared corpus proves against the Go emitter.
3. **Decision** (:func:`ensure_compatible`) — pure. The contract versions the
   platform speaks must appear in every list the binary declares, the two
   facts the workspace design depends on (``strict_host_config``,
   ``known_hosts_via_home``) are always required, and the SSH authentication
   facts are required per run. A missing capability refuses the launch; there
   is no degraded mode.

This module opens no network connection and reads no database. Errors name the
administrative path, an exit code or a capability — never file content, never
stderr text (attacker-influenced), never a digest.

The identity is a statement about the moment it was computed. The future
executor increment must identify and handshake in the same sequence that
launches the subprocess, exactly as it must build a fresh workspace — a stale
verification authorizes nothing.
"""

from __future__ import annotations

import hashlib
import hmac
import os
import re
import stat
import subprocess  # noqa: S404 - the handshake IS a bounded subprocess, argv-only
from dataclasses import dataclass
from pathlib import Path

from domain.execution_contract import (
    CURRENT_FORMAT_VERSION,
    ContractError,
    ExecutorCapabilities,
    parse_capabilities,
)

__all__ = [
    "HANDSHAKE_ARGUMENT",
    "MAX_CAPABILITIES_BYTES",
    "ExecutorBinaryIdentity",
    "ExecutorCompatibilityError",
    "ExecutorHandshakeError",
    "ExecutorIdentityError",
    "ensure_compatible",
    "identify_executor_binary",
    "run_capabilities_handshake",
]

#: The executor's handshake subcommand (cmd/cpanel-self-migration/capabilities_cmd.go).
HANDSHAKE_ARGUMENT = "capabilities"

#: Upper bound on the handshake answer. The real document is ~400 bytes; the
#: cap exists so a wrong binary cannot make the worker buffer arbitrary output.
MAX_CAPABILITIES_BYTES = 64 * 1024

_HEX_SHA256 = re.compile(r"^[0-9a-fA-F]{64}$")
_HASH_CHUNK = 1024 * 1024


class ExecutorIdentityError(Exception):
    """The path does not identify the pinned executor binary.

    Names the path (administrative). Never carries the expected or the actual
    digest: the operator re-computes them, the message stays stable, and the
    error teaches nothing about what would have been accepted.
    """


class ExecutorHandshakeError(Exception):
    """The binary did not answer the handshake with a valid document.

    Names the path, an exit code, or the contract violation (which names a
    field, never a value). Never echoes stdout or stderr content: both are
    attacker-influenced, and exception text ends up in CI's JUnit artifact.
    """


class ExecutorCompatibilityError(Exception):
    """The binary answered, and what it declared is not enough for this run."""


@dataclass(frozen=True)
class ExecutorBinaryIdentity:
    """A binary that matched its pinned digest at the moment of the check."""

    path: Path
    sha256: str


def identify_executor_binary(
    path: Path | str, *, expected_sha256: str
) -> ExecutorBinaryIdentity:
    """Prove ``path`` is the pinned executor binary, or refuse.

    ``expected_sha256`` is the deployment's pin (64 hex chars, case-insensitive)
    and is required: without a pin there is no "precise binary", only PATH
    lookup with extra steps. The comparison is constant-time out of habit, not
    necessity — a binary digest is not a secret, but ``compare_digest`` costs
    nothing and removes the question.
    """
    if not _HEX_SHA256.match(expected_sha256):
        raise ExecutorIdentityError(
            "the expected executor digest is not a 64-character hex SHA-256"
        )
    expected = expected_sha256.lower()

    binary = Path(path)
    try:
        st = os.lstat(binary)
    except OSError:
        raise ExecutorIdentityError(
            f"executor binary does not exist or cannot be inspected: {binary}"
        ) from None
    if stat.S_ISLNK(st.st_mode):
        raise ExecutorIdentityError(
            f"executor binary path is a symlink and cannot be pinned: {binary}"
        )
    if not stat.S_ISREG(st.st_mode):
        raise ExecutorIdentityError(
            f"executor binary path is not a regular file: {binary}"
        )
    if not os.access(binary, os.X_OK):
        raise ExecutorIdentityError(f"executor binary is not executable: {binary}")

    digest = hashlib.sha256()
    try:
        with open(binary, "rb") as handle:
            while chunk := handle.read(_HASH_CHUNK):
                digest.update(chunk)
    except OSError:
        raise ExecutorIdentityError(
            f"executor binary could not be read: {binary}"
        ) from None

    actual = digest.hexdigest()
    if not hmac.compare_digest(actual, expected):
        raise ExecutorIdentityError(
            f"executor binary does not match the pinned digest: {binary}"
        )
    return ExecutorBinaryIdentity(path=binary, sha256=actual)


def run_capabilities_handshake(
    identity: ExecutorBinaryIdentity, *, timeout_seconds: float = 10.0
) -> ExecutorCapabilities:
    """Ask the identified binary for its self-description, bounded on every side.

    Call this immediately after :func:`identify_executor_binary`: the identity
    pins the content at the moment it was computed, and the gap between the two
    calls is the window an operator must keep closed (a read-only deployment
    directory, not a re-check loop).
    """
    try:
        completed = subprocess.run(  # noqa: S603 - argv from a digest-pinned identity
            [str(identity.path), HANDSHAKE_ARGUMENT],
            stdin=subprocess.DEVNULL,
            capture_output=True,
            timeout=timeout_seconds,
            env={},  # the worker env carries resolvable secrets; the child gets none
            shell=False,
            check=False,
        )
    except subprocess.TimeoutExpired:
        raise ExecutorHandshakeError(
            f"executor did not answer the capabilities handshake within "
            f"{timeout_seconds}s: {identity.path}"
        ) from None
    except OSError:
        raise ExecutorHandshakeError(
            f"executor could not be started for the handshake: {identity.path}"
        ) from None

    if completed.returncode != 0:
        # Never the stderr text: it is attacker-influenced output.
        raise ExecutorHandshakeError(
            f"executor exited with status {completed.returncode} during the "
            f"capabilities handshake: {identity.path}"
        )
    if len(completed.stdout) > MAX_CAPABILITIES_BYTES:
        raise ExecutorHandshakeError(
            f"executor emitted more than {MAX_CAPABILITIES_BYTES} bytes during "
            f"the capabilities handshake: {identity.path}"
        )

    try:
        return parse_capabilities(completed.stdout)
    except ContractError as exc:
        # ContractError messages name a field, never a value — safe to carry.
        raise ExecutorHandshakeError(
            f"executor emitted an invalid capabilities document ({exc}): {identity.path}"
        ) from None


def ensure_compatible(
    capabilities: ExecutorCapabilities,
    *,
    require_password: bool = False,
    require_private_key: bool = False,
    require_encrypted_private_key: bool = False,
) -> None:
    """Refuse the launch unless the declared capabilities cover this run.

    Always required, because the workspace design depends on them: the strict
    ``host.yaml`` parser (an unknown field must be a hard error, not a silently
    ignored operator belief) and ``known_hosts`` derived from ``HOME`` (pinned
    trust exists only because the engine reads the file the workspace wrote).

    The three SSH authentication facts are per-run: the caller states what the
    resolved credentials actually need. Nothing here defaults to "probably
    fine" — a missing capability is :class:`ExecutorCompatibilityError`.
    """
    for kind, declared in (
        ("spec", capabilities.contract.spec),
        ("event", capabilities.contract.event),
        ("result", capabilities.contract.result),
    ):
        if CURRENT_FORMAT_VERSION not in declared:
            raise ExecutorCompatibilityError(
                f"the executor does not speak execution-{kind} format_version "
                f"{CURRENT_FORMAT_VERSION}"
            )

    ssh = capabilities.ssh
    always = (
        ("strict_host_config", ssh.strict_host_config),
        ("known_hosts_via_home", ssh.known_hosts_via_home),
    )
    for name, present in always:
        if not present:
            raise ExecutorCompatibilityError(
                f"the executor lacks a capability the workspace design requires: {name}"
            )

    needed = (
        ("password", require_password, ssh.password),
        ("private_key", require_private_key, ssh.private_key),
        ("encrypted_private_key", require_encrypted_private_key, ssh.encrypted_private_key),
    )
    for name, required, present in needed:
        if required and not present:
            raise ExecutorCompatibilityError(
                f"this run requires SSH capability {name}, which the executor lacks"
            )
