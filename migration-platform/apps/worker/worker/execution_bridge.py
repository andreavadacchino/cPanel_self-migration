"""Run one dry-run execution and ingest its output — the M3 core.

Given a *verified* executor path and an SSH workspace built elsewhere (the
`host.yaml` #118 generates, the verified artifact #119 stages), this module:

1. materializes the canonical `execution-spec-v1` into the run's output
   directory, with private permissions, from an already-anchored spec;
2. launches ``<executor> execute --spec … --config host.yaml --output-dir …``
   in its **own process group**, with a **stripped environment** the caller
   controls and a **timeout** whose expiry reaps the whole group
   (``SIGTERM`` → grace → ``SIGKILL``);
3. ingests and **validates** ``events.jsonl`` line by line and ``report.json``
   against the execution contract (:mod:`domain.execution_contract`);
4. classifies the terminal outcome. Because a dry run writes nothing, the
   outcome is **never** ``partial``: it is ``succeeded``, ``failed`` or
   ``interrupted``.

It takes plain paths — never a database session, never a Dramatiq message — so
it can be driven end to end by a contract-speaking fake, and so the actor that
wires it to leases, freshness and the real workspace stays a separate concern.

No secret is read, logged, or returned here: the spec carries references only,
credentials live in ``host.yaml`` (passed through, never parsed), and the
contract validators reject a document that leaked one. POSIX only — process
groups and ``killpg`` have no portable Windows equivalent, and the executor
runs in a Linux worker.
"""

from __future__ import annotations

import hashlib
import json
import os
import signal
import subprocess
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path

from domain.execution_contract import (
    ContractError,
    validate_event_json,
    validate_result_json,
    validate_spec_json,
)
from domain.execution_spec import canonical_spec_bytes

__all__ = [
    "SPEC_FILENAME",
    "EVENTS_FILENAME",
    "REPORT_FILENAME",
    "EXECUTE_SUBCOMMAND",
    "TERMINAL_SUCCEEDED",
    "TERMINAL_FAILED",
    "TERMINAL_INTERRUPTED",
    "ExecutionBridgeError",
    "ExecutorLaunchError",
    "EventIngestionError",
    "ResultIngestionError",
    "ArtifactRecord",
    "ExecutionOutcome",
    "materialize_spec",
    "run_execution",
]

SPEC_FILENAME = "execution-spec.json"
EVENTS_FILENAME = "events.jsonl"
REPORT_FILENAME = "report.json"
EXECUTE_SUBCOMMAND = "execute"

#: Terminal states this engine can assign. `partial` is deliberately absent: a
#: dry run never writes, so no interruption can leave the destination half-done.
TERMINAL_SUCCEEDED = "succeeded"
TERMINAL_FAILED = "failed"
TERMINAL_INTERRUPTED = "interrupted"

#: The executor's exit_status vocabulary (execution-result-v1) → terminal state.
_EXIT_STATUS_TO_TERMINAL = {
    "success": TERMINAL_SUCCEEDED,
    "failed": TERMINAL_FAILED,
    "interrupted": TERMINAL_INTERRUPTED,
}

#: The output artifacts the executor produces. One execution = one set.
_ARTIFACT_NAMES = (EVENTS_FILENAME, REPORT_FILENAME)

_DEFAULT_TERM_GRACE_SECONDS = 5.0


class ExecutionBridgeError(Exception):
    """A dry-run execution could not be run or ingested. Never carries a secret."""


class ExecutorLaunchError(ExecutionBridgeError):
    """A launch precondition failed, or the subprocess could not be started."""


class EventIngestionError(ExecutionBridgeError):
    """events.jsonl held a line that is not a valid execution-event-v1."""


class ResultIngestionError(ExecutionBridgeError):
    """report.json is absent, invalid, or describes a different run."""


@dataclass(frozen=True)
class ArtifactRecord:
    """One durable artifact and its digest — the manifest an audit needs."""

    name: str
    path: Path
    sha256: str
    size_bytes: int


@dataclass(frozen=True)
class ExecutionOutcome:
    """The result of running and ingesting one dry-run execution."""

    terminal_status: str
    return_code: int
    events: tuple[dict, ...]
    report: dict | None
    artifacts: tuple[ArtifactRecord, ...]


def materialize_spec(spec: dict, dest_dir: Path | str) -> Path:
    """Write the canonical spec bytes into *dest_dir*, privately.

    The exact bytes are validated first, so an invalid spec is never written and
    never reaches the executor. Raises :class:`domain.execution_contract.ContractError`
    on an invalid spec (the contract is the authority, not this module).
    """
    raw = canonical_spec_bytes(spec)
    validate_spec_json(raw)  # ContractError propagates untouched.
    path = Path(dest_dir) / SPEC_FILENAME
    _write_private(path, raw)
    return path


def run_execution(
    *,
    executable_path: Path | str,
    host_config_path: Path | str,
    spec: dict,
    output_dir: Path | str,
    timeout_seconds: float,
    env: Mapping[str, str] | None = None,
    term_grace_seconds: float = _DEFAULT_TERM_GRACE_SECONDS,
) -> ExecutionOutcome:
    """Run one dry-run execution to a terminal outcome.

    *env* is the **complete** environment handed to the subprocess — the engine
    never merges the worker's own environment, so a secret the worker holds
    cannot leak into the executor. The caller supplies exactly what the executor
    needs (``PATH``; ``HOME`` pointing at the workspace so the executor finds its
    ``known_hosts``). Absent means an empty environment.
    """
    executable_path = Path(executable_path)
    host_config_path = Path(host_config_path)
    output_dir = Path(output_dir)

    _check_launch_preconditions(executable_path, host_config_path, output_dir)

    try:
        spec_path = materialize_spec(spec, output_dir)
    except OSError as exc:
        raise ExecutorLaunchError(f"could not write the execution spec: {exc}") from exc
    run_id = spec["run_id"]

    argv = [
        str(executable_path),
        EXECUTE_SUBCOMMAND,
        "--spec",
        str(spec_path),
        "--config",
        str(host_config_path),
        "--output-dir",
        str(output_dir),
    ]
    return_code, interrupted = _launch(
        argv, dict(env or {}), output_dir, timeout_seconds, term_grace_seconds
    )

    events = _ingest_events(
        output_dir / EVENTS_FILENAME, run_id, tolerate_truncation=interrupted
    )
    report = (
        None if interrupted else _ingest_report(output_dir / REPORT_FILENAME, run_id)
    )
    artifacts = _manifest(output_dir)
    terminal = _classify(report, interrupted)

    return ExecutionOutcome(
        terminal_status=terminal,
        return_code=return_code,
        events=events,
        report=report,
        artifacts=artifacts,
    )


# --- launch -----------------------------------------------------------------


def _check_launch_preconditions(
    executable_path: Path, host_config_path: Path, output_dir: Path
) -> None:
    if not executable_path.is_file() or not os.access(executable_path, os.X_OK):
        raise ExecutorLaunchError(
            f"executor is not an executable file: {executable_path}"
        )
    if not host_config_path.is_file():
        raise ExecutorLaunchError(f"host config not found: {host_config_path}")
    if not output_dir.is_dir():
        raise ExecutorLaunchError(f"output directory does not exist: {output_dir}")
    # One execution = one artifact set. A directory already holding a previous
    # run's output is evidence; refuse it rather than interleave two runs under
    # one run_id (the same defence the Go bridge applies before it starts).
    for name in _ARTIFACT_NAMES:
        if (output_dir / name).exists():
            raise ExecutorLaunchError(
                f"output directory already holds {name} from a previous execution: "
                "the bridge needs a private workspace per run"
            )


def _launch(
    argv: list[str],
    env: dict[str, str],
    cwd: Path,
    timeout_seconds: float,
    term_grace_seconds: float,
) -> tuple[int, bool]:
    """Start the executor in its own process group; return (return_code, interrupted).

    stdin/stdout/stderr are closed to ``/dev/null``: the contract travels through
    files, never the pipes, and an unread pipe that fills would deadlock the run.
    """
    try:
        proc = subprocess.Popen(  # noqa: S603 - argv is engine-built, not shell
            argv,
            env=env,
            cwd=str(cwd),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
    except OSError as exc:
        raise ExecutorLaunchError(f"could not start the executor: {exc}") from exc

    interrupted = False
    try:
        proc.wait(timeout=timeout_seconds)
    except subprocess.TimeoutExpired:
        interrupted = True
        _terminate_group(proc, term_grace_seconds)
    return_code = proc.returncode if proc.returncode is not None else -1
    return return_code, interrupted


def _terminate_group(proc: subprocess.Popen, grace_seconds: float) -> None:
    """Reap the whole process group: SIGTERM, a grace window, then SIGKILL.

    ``start_new_session=True`` made the child a group leader (pgid == pid), so
    signalling the group also reaps anything the executor itself spawned (an
    ``ssh`` child), which a bare ``proc.kill()`` would orphan.
    """
    try:
        pgid = os.getpgid(proc.pid)
    except ProcessLookupError:
        return
    _killpg(pgid, signal.SIGTERM)
    try:
        proc.wait(timeout=grace_seconds)
        return
    except subprocess.TimeoutExpired:
        pass
    _killpg(pgid, signal.SIGKILL)
    try:
        proc.wait(timeout=grace_seconds)
    except subprocess.TimeoutExpired:  # pragma: no cover - SIGKILL is not refusable
        pass


def _killpg(pgid: int, sig: int) -> None:
    try:
        os.killpg(pgid, sig)
    except ProcessLookupError:  # pragma: no cover - already gone
        pass


# --- ingestion --------------------------------------------------------------


def _ingest_events(
    events_path: Path, run_id: str, *, tolerate_truncation: bool
) -> tuple[dict, ...]:
    """Validate every complete line of events.jsonl and confirm its run_id.

    On a clean run every line must be a valid execution-event-v1. On an
    interrupted run the stream may be cut mid-line: a final line with no trailing
    newline is a truncation, not a contract breach, and is dropped rather than
    rejected.
    """
    if not events_path.exists():
        return ()
    try:
        text = events_path.read_bytes().decode("utf-8")
    except UnicodeDecodeError as exc:
        raise EventIngestionError("events.jsonl is not valid UTF-8") from exc

    ends_with_newline = text.endswith("\n")
    segments = text.split("\n")
    if segments and segments[-1] == "":
        segments = segments[:-1]  # the empty tail after a final newline
    elif segments and not ends_with_newline and tolerate_truncation:
        segments = segments[:-1]  # a partial last line the kill cut off

    events: list[dict] = []
    for lineno, line in enumerate(segments, start=1):
        if line == "":
            continue
        try:
            validate_event_json(line)
        except ContractError as exc:
            raise EventIngestionError(f"events.jsonl line {lineno}: {exc}") from exc
        doc = json.loads(line)
        if doc.get("run_id") != run_id:
            raise EventIngestionError(
                f"events.jsonl line {lineno}: run_id does not match the execution"
            )
        events.append(doc)
    return tuple(events)


def _ingest_report(report_path: Path, run_id: str) -> dict:
    """Validate report.json and confirm it describes this run.

    A clean exit without a report is a contract breach: the executor always
    writes one when it reaches the end (execute_cmd.go), so its absence means the
    executor is broken, not that the run merely failed.
    """
    if not report_path.exists():
        raise ResultIngestionError(
            "the executor exited without writing report.json (contract breach)"
        )
    raw = report_path.read_bytes()
    try:
        validate_result_json(raw)
    except ContractError as exc:
        raise ResultIngestionError(f"report.json: {exc}") from exc
    doc = json.loads(raw)
    if doc.get("run_id") != run_id:
        raise ResultIngestionError(
            "report.json run_id does not match the execution spec"
        )
    return doc


def _manifest(output_dir: Path) -> tuple[ArtifactRecord, ...]:
    records: list[ArtifactRecord] = []
    for name in _ARTIFACT_NAMES:
        path = output_dir / name
        if not path.exists():
            continue
        raw = path.read_bytes()
        records.append(
            ArtifactRecord(
                name=name,
                path=path,
                sha256=hashlib.sha256(raw).hexdigest(),
                size_bytes=len(raw),
            )
        )
    return tuple(records)


def _classify(report: dict | None, interrupted: bool) -> str:
    if interrupted:
        return TERMINAL_INTERRUPTED
    # The validator has already restricted exit_status to the three known values.
    return _EXIT_STATUS_TO_TERMINAL[report["exit_status"]]  # type: ignore[index]


# --- private files ----------------------------------------------------------


def _write_private(path: Path, data: bytes) -> None:
    """Create *path* exclusively with 0600 and write *data*.

    ``O_EXCL`` refuses to clobber an existing file (a reused workspace); the mode
    is re-applied with ``fchmod`` so the umask cannot widen it.
    """
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    try:
        os.fchmod(fd, 0o600)
        os.write(fd, data)
    finally:
        os.close(fd)
