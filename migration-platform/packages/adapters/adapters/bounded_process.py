"""Run a child process bounded while it runs, not judged after it finishes.

``subprocess.run(capture_output=True, timeout=…)`` reads to EOF and hands back
everything the child wrote; a size check afterwards is a verdict on a buffer
that already exists. This module reads incrementally instead and stops the
child the moment it goes past the limit, so the worker's memory is bounded by
``max_stdout_bytes`` whatever the child does.

What it deliberately is not: a job runner. No retries, no streaming callbacks,
no output files, no partial results. One child, one bounded answer, or an
error — the shape the capabilities handshake needs and the future ``execute``
can reuse.

**Process-group discipline.** The child gets its own session
(``start_new_session=True``), so stopping it means signalling the *group*: a
child that spawned a helper cannot leave the helper holding the pipe. The
sequence is SIGTERM, a short grace period, then SIGKILL, and the child is
always reaped before any error leaves this module — an exception with a zombie
behind it is a leak, not a failure.

Errors name the executable path (administrative) and nothing else: never the
child's output, never the environment. Exception text reaches CI's JUnit
artifact.

POSIX only, which is what the platform runs on.
"""

from __future__ import annotations

import os
import selectors
import signal
import subprocess  # noqa: S404 - running a bounded child IS this module's job
import time
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path

__all__ = [
    "BoundedProcessResult",
    "ProcessError",
    "ProcessOutputLimitError",
    "ProcessStartError",
    "ProcessTerminationError",
    "ProcessTimeoutError",
    "run_bounded_process",
]

#: How much is read per syscall. Independent of the limit: the buffer never
#: exceeds ``max_stdout_bytes + 1``, because reads are clamped to what is still
#: allowed plus the single byte that proves the child went over.
_READ_CHUNK = 64 * 1024

#: How long a SIGTERM has to work before SIGKILL. Short: the child is being
#: stopped because it already misbehaved.
_GRACE_SECONDS = 0.5


class ProcessError(Exception):
    """Base class, so a caller can catch the family by type, never by message."""


class ProcessStartError(ProcessError):
    """The child could not be started (missing, not executable, bad argv)."""


class ProcessTimeoutError(ProcessError):
    """The child outlived its deadline and was stopped."""


class ProcessOutputLimitError(ProcessError):
    """The child wrote past ``max_stdout_bytes`` and was stopped."""


class ProcessTerminationError(ProcessError):
    """The child could not be stopped. Never raised while it is still running."""


@dataclass(frozen=True)
class BoundedProcessResult:
    """A child that finished on its own, within its bounds.

    ``returncode`` follows the POSIX convention ``subprocess`` uses: negative
    means killed by that signal. Carries no stderr — the handshake sends it to
    /dev/null, and a field nobody reads is a field that leaks.
    """

    returncode: int
    stdout: bytes


def run_bounded_process(
    argv: Sequence[str],
    *,
    timeout_seconds: float,
    max_stdout_bytes: int,
    env: Mapping[str, str] | None = None,
    cwd: Path | None = None,
) -> BoundedProcessResult:
    """Run ``argv``, returning at most ``max_stdout_bytes`` of stdout.

    ``argv[0]`` must be an absolute path: this module never resolves a name
    through ``PATH``, because the whole point of the caller's digest pin is
    that the file is chosen, not looked up. ``env`` defaults to an empty
    environment — a caller that wants the parent's must say so, which is the
    right way round for a worker whose env carries secrets.

    stderr goes to ``/dev/null``. An unread pipe would block the child forever
    once the kernel buffer filled, making the timeout the only exit; capturing
    it would mean a second bounded reader for output nobody reads.

    Raises :class:`ProcessStartError`, :class:`ProcessTimeoutError`,
    :class:`ProcessOutputLimitError` or :class:`ProcessTerminationError`. A
    non-zero exit is *not* an error: it is a result the caller judges.
    """
    if not argv:
        raise ValueError("argv must not be empty")
    if not os.path.isabs(argv[0]):
        raise ValueError("argv[0] must be an absolute path, never a PATH lookup")
    if max_stdout_bytes < 0:
        raise ValueError("max_stdout_bytes must not be negative")
    if timeout_seconds <= 0:
        raise ValueError("timeout_seconds must be positive")

    try:
        process = subprocess.Popen(  # noqa: S603 - absolute argv, no shell, empty env
            list(argv),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            env=dict(env) if env is not None else {},
            cwd=str(cwd) if cwd is not None else None,
            shell=False,
            close_fds=True,
            # Its own session: stopping the child means stopping its group, so a
            # helper it spawned cannot outlive the run holding the pipe.
            start_new_session=True,
        )
    except OSError:
        raise ProcessStartError(f"child process could not be started: {argv[0]}") from None

    try:
        stdout, overflowed = _read_bounded(process, timeout_seconds, max_stdout_bytes)
    except _Timeout:
        _stop(process, argv[0])
        raise ProcessTimeoutError(
            f"child process exceeded {timeout_seconds}s and was stopped: {argv[0]}"
        ) from None
    except BaseException:
        # Any other exit from the read loop (including KeyboardInterrupt) must
        # not leave the child running.
        _stop(process, argv[0])
        raise

    if overflowed:
        _stop(process, argv[0])
        raise ProcessOutputLimitError(
            f"child process wrote more than {max_stdout_bytes} bytes to stdout "
            f"and was stopped: {argv[0]}"
        )

    # EOF on stdout does not prove the child is gone (it could have closed the
    # descriptor and lived on), so the deadline still applies to the wait.
    try:
        returncode = process.wait(timeout=_GRACE_SECONDS)
    except subprocess.TimeoutExpired:
        _stop(process, argv[0])
        raise ProcessTimeoutError(
            f"child process closed stdout but did not exit: {argv[0]}"
        ) from None
    return BoundedProcessResult(returncode=returncode, stdout=stdout)


class _Timeout(Exception):
    """Internal: the read loop hit the deadline."""


def _read_bounded(
    process: subprocess.Popen[bytes], timeout_seconds: float, limit: int
) -> tuple[bytes, bool]:
    """Read stdout until EOF, the limit, or the deadline.

    Returns the bytes and whether the child went over. The buffer never holds
    more than ``limit + 1`` bytes: each read asks for exactly what is still
    allowed plus one byte, and that extra byte is the proof of overflow — it is
    never returned to the caller.
    """
    assert process.stdout is not None  # stdout=PIPE, by construction
    chunks: list[bytes] = []
    total = 0
    deadline = time.monotonic() + timeout_seconds

    with selectors.DefaultSelector() as selector:
        selector.register(process.stdout, selectors.EVENT_READ)
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise _Timeout
            if not selector.select(timeout=remaining):
                raise _Timeout  # nothing readable before the deadline
            # One byte past the limit is enough to know, and is all we keep.
            want = min(_READ_CHUNK, limit - total + 1)
            try:
                chunk = os.read(process.stdout.fileno(), want)
            except OSError:
                break  # the pipe broke; the child's status is the real answer
            if not chunk:
                break  # EOF
            chunks.append(chunk)
            total += len(chunk)
            if total > limit:
                return b"".join(chunks)[:limit], True
    return b"".join(chunks), False


def _stop(process: subprocess.Popen[bytes], name: str) -> None:
    """Stop the child's whole group and reap it. Never leaves a zombie.

    A child that already exited is not an error: the race between it finishing
    and us signalling is normal, and ``ProcessLookupError`` is how it looks.
    """
    try:
        pgid = os.getpgid(process.pid)
    except ProcessLookupError:
        pgid = None  # already gone; still needs reaping below

    if pgid is not None:
        _signal_group(pgid, signal.SIGTERM)
        try:
            process.wait(timeout=_GRACE_SECONDS)
        except subprocess.TimeoutExpired:
            _signal_group(pgid, signal.SIGKILL)

    try:
        process.wait(timeout=_GRACE_SECONDS)
    except subprocess.TimeoutExpired:
        raise ProcessTerminationError(
            f"child process could not be stopped: {name}"
        ) from None
    finally:
        # The pipe is ours to close whatever happened; Popen only closes it in
        # communicate(), which this module deliberately never calls.
        if process.stdout is not None and not process.stdout.closed:
            process.stdout.close()


def _signal_group(pgid: int, sig: signal.Signals) -> None:
    """Signal a process group, tolerating a group that is already gone."""
    try:
        os.killpg(pgid, sig)
    except (ProcessLookupError, PermissionError):
        # Gone, or never ours. Either way there is nothing left to stop; the
        # wait below decides whether that is true.
        pass
