"""Ask the verified executor what it can do, and decide — fail-closed.

The compatibility handshake ADR-001 requires ("verificata prima dell'avvio,
non scoperta a metà run"), in two steps that follow the staging done by
:mod:`adapters.executor_artifact`:

1. **Handshake** (:func:`run_capabilities_handshake`) — the one subprocess this
   module runs: ``<artifact> capabilities``, which prints the
   executor-capabilities-v1 self-description and exits. It runs the *verified
   private artifact*, never a deployment path: a path handed to ``subprocess``
   is resolved again by the kernel, so what was verified and what runs would be
   two different questions. The type says so — this function takes a
   :class:`~adapters.executor_artifact.VerifiedExecutorArtifact` and nothing
   else, so re-opening that hole needs a deliberate change, not a slip.
   Bounded on every side by :func:`adapters.bounded_process.run_bounded_process`:
   argv only (no shell, no PATH), stdin closed, a **stripped environment** (the
   worker's env legitimately carries ``*_CPANEL_*`` secrets for ``env://`` refs
   — none may reach the child), a timeout, and stdout limited **while it is
   read**. The answer is validated by
   :func:`domain.execution_contract.parse_capabilities`, the same validator the
   shared corpus proves against the Go emitter.
2. **Decision** (:func:`ensure_compatible`) — pure. The contract versions the
   platform speaks must appear in every list the binary declares, the two facts
   the workspace design depends on (``strict_host_config``,
   ``known_hosts_via_home``) are always required, and the SSH authentication
   facts are required per run. A missing capability refuses the launch; there
   is no degraded mode.

**The invariant the future executor inherits**: the artifact that passes the
handshake is the artifact ``execute`` must run. Staging once and executing the
source path, or re-staging a second copy after the handshake, both re-open the
hole this design closed::

    with prepare_verified_executor(deployment) as artifact:
        capabilities = run_capabilities_handshake(artifact)
        ensure_compatible(capabilities, ...)
        # fresh snapshot, fresh SSH workspace, then:
        # run_execute(artifact, ...)   <- the SAME artifact, never the source

This module opens no network connection and reads no database. Errors name the
administrative path, an exit code, a capability or a *field* — never a field's
value, never file content, never the child's stdout/stderr, never a digest.
(One precise exception, shared with the Go validator: an *unknown* field is
reported by name, and that name comes from the document. It is a field name,
not a value, and the document comes from a verified artifact.)
"""

from __future__ import annotations

from adapters.bounded_process import (
    ProcessError,
    ProcessOutputLimitError,
    ProcessStartError,
    ProcessTimeoutError,
    run_bounded_process,
)
from adapters.executor_artifact import VerifiedExecutorArtifact
from domain.execution_contract import (
    CURRENT_FORMAT_VERSION,
    ContractError,
    ExecutorCapabilities,
    parse_capabilities,
)

__all__ = [
    "HANDSHAKE_ARGUMENT",
    "MAX_CAPABILITIES_BYTES",
    "ExecutorCompatibilityError",
    "ExecutorHandshakeError",
    "ensure_compatible",
    "run_capabilities_handshake",
]

#: The executor's handshake subcommand (cmd/cpanel-self-migration/capabilities_cmd.go).
HANDSHAKE_ARGUMENT = "capabilities"

#: Upper bound on the handshake answer (the real document is ~400 bytes).
#: Enforced *while stdout is read* by the bounded runner, so it is a memory
#: bound and not a verdict on a buffer that already exists.
MAX_CAPABILITIES_BYTES = 64 * 1024


class ExecutorHandshakeError(Exception):
    """The executor did not answer the handshake with a valid document.

    Names the artifact path, an exit code, or the contract violation (which
    names a field, never a value). Never echoes stdout or stderr: both are the
    child's own output, and exception text ends up in CI's JUnit artifact.
    """


class ExecutorCompatibilityError(Exception):
    """The executor answered, and what it declared is not enough for this run."""


def run_capabilities_handshake(
    artifact: VerifiedExecutorArtifact, *, timeout_seconds: float = 10.0
) -> ExecutorCapabilities:
    """Ask the verified artifact for its self-description, bounded on every side.

    Takes an artifact, never a path: the bytes that answer here are the bytes
    whose digest was verified, because they are the same file — nothing
    re-resolves the deployment path, so replacing, rewriting or deleting the
    source after staging changes nothing.
    """
    try:
        result = run_bounded_process(
            [str(artifact.executable_path), HANDSHAKE_ARGUMENT],
            timeout_seconds=timeout_seconds,
            max_stdout_bytes=MAX_CAPABILITIES_BYTES,
            env={},  # the worker env carries resolvable secrets; the child gets none
        )
    except ProcessStartError:
        raise ExecutorHandshakeError(
            f"executor could not be started for the handshake: "
            f"{artifact.executable_path}"
        ) from None
    except ProcessTimeoutError:
        raise ExecutorHandshakeError(
            f"executor did not answer the capabilities handshake within "
            f"{timeout_seconds}s: {artifact.executable_path}"
        ) from None
    except ProcessOutputLimitError:
        raise ExecutorHandshakeError(
            f"executor emitted more than {MAX_CAPABILITIES_BYTES} bytes during "
            f"the capabilities handshake: {artifact.executable_path}"
        ) from None
    except ProcessError:
        # Termination failures and anything else the runner classifies: a
        # handshake that cannot be bounded is a handshake that failed.
        raise ExecutorHandshakeError(
            f"the capabilities handshake could not be completed: "
            f"{artifact.executable_path}"
        ) from None

    if result.returncode != 0:
        # Never the stderr text: it is the child's own output.
        raise ExecutorHandshakeError(
            f"executor exited with status {result.returncode} during the "
            f"capabilities handshake: {artifact.executable_path}"
        )

    try:
        return parse_capabilities(result.stdout)
    except ContractError as exc:
        # ContractError messages name a field, never a value — safe to carry.
        raise ExecutorHandshakeError(
            f"executor emitted an invalid capabilities document ({exc}): "
            f"{artifact.executable_path}"
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
