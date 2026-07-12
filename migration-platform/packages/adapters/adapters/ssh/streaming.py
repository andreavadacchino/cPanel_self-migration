"""Backpressured, bounded, cancellable source->destination byte streaming.

The engine copies bytes from a :class:`ByteSource` (a source that only *produces*
bytes) into a :class:`StdinSink` (a destination stdin that only *receives* bytes).
It is deliberately synchronous and holds at most **one chunk** in flight: it writes
a chunk in full before reading the next, so a slow sink naturally throttles the
source (real backpressure via the SSH channel's own flow control) and the maximum
memory is ``chunk_size`` — independent of the total payload. There is no unbounded
queue anywhere.

Cancellation is cooperative and checked before start, before each read, inside a
blocked write, and while awaiting exit. Distinct start/idle/total/close timeouts
run on an injectable monotonic clock. Any timeout, cancel, or interruption returns
a typed **partial** :class:`StreamResult` carrying the transferred-byte count and
the failing side; a started stream is **never** retried. Transferred bytes never
enter a log, error, audit, or progress callback, and secrets are redacted.

Role separation and the real transport live in B2b-ii; this module is pure engine
plus contracts and is tested against the deterministic fake in ``fakes.py``.
"""

from __future__ import annotations

import enum
import threading
import time
from dataclasses import dataclass, field
from typing import Callable, Protocol, runtime_checkable

from adapters.ssh.contract import redact
from adapters.ssh.errors import SshCommandTimeoutError, SshError


# -- contracts -------------------------------------------------------------


@runtime_checkable
class ByteSource(Protocol):
    """A source that only produces bytes (never receives stdin)."""

    def read_chunk(self, max_bytes: int, *, timeout: float | None) -> bytes:
        """Return up to ``max_bytes``; ``b""`` signals EOF. Raises on disconnect."""
        ...

    def exited(self) -> bool: ...  # readiness (a signalled exit has status None)
    def stderr(self) -> tuple[bytes, bool]: ...
    def exit_status(self) -> int | None: ...
    def exit_signal(self) -> str | None: ...
    def close(self) -> None: ...


@runtime_checkable
class StdinSink(Protocol):
    """A destination stdin that only receives bytes."""

    def write_some(self, data: bytes, *, timeout: float | None) -> int:
        """Accept and return ``>=0`` bytes (a short write is allowed). Raises on
        broken pipe/disconnect."""
        ...

    def close_stdin(self) -> None: ...
    def exited(self) -> bool: ...  # readiness (a signalled exit has status None)
    def stderr(self) -> tuple[bytes, bool]: ...
    def exit_status(self) -> int | None: ...
    def exit_signal(self) -> str | None: ...
    def close(self) -> None: ...


class StreamFailureSide(enum.Enum):
    NONE = "none"
    SOURCE = "source"
    DESTINATION = "destination"
    LOCAL = "local"  # cancellation or a local timeout


class StreamOutcome(enum.Enum):
    COMPLETED = "completed"  # the copy finished naturally (exit codes may still be non-zero)
    PARTIAL = "partial"  # interrupted by cancel/timeout/disconnect


@dataclass(frozen=True)
class StreamOptions:
    """Bounded streaming knobs.

    ``chunk_size`` is both the read size and the high-water mark: only one chunk is
    ever held, so memory is bounded and independent of the total size.
    """

    chunk_size: int = 65_536
    max_source_stderr_bytes: int = 262_144
    max_dest_stderr_bytes: int = 262_144
    start_timeout: float | None = 10.0
    idle_timeout: float | None = 30.0
    total_timeout: float | None = None
    close_grace: float | None = 5.0
    progress_interval: float = 1.0


@dataclass(frozen=True)
class StreamProgress:
    """Payload-free progress. Carries counters only, never transferred bytes."""

    bytes_transferred: int
    elapsed_seconds: float


@dataclass
class StreamAudit:
    """Redacted, content-free evidence of a stream."""

    operation: str
    outcome: str = StreamOutcome.PARTIAL.value
    failure_side: str = StreamFailureSide.NONE.value
    bytes_transferred: int = 0
    source_exit_status: int | None = None
    source_exit_signal: str | None = None
    dest_exit_status: int | None = None
    dest_exit_signal: str | None = None
    source_stderr_bytes: int = 0
    dest_stderr_bytes: int = 0
    source_stderr_truncated: bool = False
    dest_stderr_truncated: bool = False
    error_type: str | None = None
    message: str | None = None

    def as_evidence(self) -> dict[str, object]:
        return dict(self.__dict__)


@dataclass
class StreamResult:
    """A finished or partial stream, bound to its redacted audit."""

    outcome: StreamOutcome
    failure_side: StreamFailureSide
    bytes_transferred: int
    source_exit_status: int | None
    source_exit_signal: str | None
    dest_exit_status: int | None
    dest_exit_signal: str | None
    source_stderr: bytes = field(repr=False, default=b"")
    dest_stderr: bytes = field(repr=False, default=b"")
    source_stderr_truncated: bool = False
    dest_stderr_truncated: bool = False
    error_type: str | None = None
    message: str | None = None
    audit: StreamAudit | None = field(repr=False, default=None)

    @property
    def ok(self) -> bool:
        return (
            self.outcome is StreamOutcome.COMPLETED
            and self.failure_side is StreamFailureSide.NONE
            and self.source_exit_status == 0
            and self.dest_exit_status == 0
            and self.source_exit_signal is None
            and self.dest_exit_signal is None
        )


# -- interruption sentinel -------------------------------------------------


@dataclass(frozen=True)
class _Interrupt:
    side: StreamFailureSide
    error_type: str
    message: str


class _PumpRun:
    """One synchronous copy run. Holds all state so helpers stay small."""

    def __init__(
        self,
        source: ByteSource,
        sink: StdinSink,
        options: StreamOptions,
        *,
        cancel: threading.Event | None,
        progress: Callable[[StreamProgress], None] | None,
        monotonic: Callable[[], float],
        secrets: tuple[str, ...],
        operation: str,
    ) -> None:
        self._source = source
        self._sink = sink
        self._opt = options
        self._cancel = cancel
        self._progress = progress
        self._now = monotonic
        self._secrets = secrets
        self._operation = operation
        self._start = monotonic()
        self._last_progress = self._start
        self._last_emit = self._start
        self._bytes = 0
        self._interrupt: _Interrupt | None = None
        self._closed = False

    # -- lifecycle --------------------------------------------------------

    def run(self) -> StreamResult:
        try:
            self._copy_loop()
            if self._interrupt is None:
                self._close_stdin()
            if self._interrupt is None:
                self._await_exits()
        finally:
            # The destination is always closed/drained, even when the source
            # failed, and both channels are closed exactly once.
            self._close_once()
        return self._build_result()

    def _close_once(self) -> None:
        if self._closed:  # pragma: no cover - defensive idempotency guard
            return
        self._closed = True
        # Close both sides once; never let a close fault mask the real outcome.
        for side in (self._sink, self._source):
            try:
                side.close()
            except Exception:  # pragma: no cover - defensive
                pass

    # -- copy loop --------------------------------------------------------

    def _copy_loop(self) -> None:
        while True:
            if self._deadline_or_cancel_hit():
                return
            try:
                chunk = self._source.read_chunk(self._opt.chunk_size, timeout=self._budget())
            except SshCommandTimeoutError:
                self._interrupt = _Interrupt(StreamFailureSide.LOCAL, "SshCommandTimeoutError", "idle timeout")
                return
            except SshError as exc:
                self._interrupt = _Interrupt(StreamFailureSide.SOURCE, type(exc).__name__, self._clean(exc))
                return
            if not chunk:
                return  # EOF
            if not self._write_full(chunk):
                return
            self._bytes += len(chunk)
            self._last_progress = self._now()
            self._emit_progress()

    def _write_full(self, chunk: bytes) -> bool:
        """Write the whole chunk before returning; a slow/short sink throttles us."""
        offset = 0
        view = memoryview(chunk)
        while offset < len(chunk):
            if self._deadline_or_cancel_hit():
                return False
            try:
                written = self._sink.write_some(bytes(view[offset:]), timeout=self._budget())
            except SshCommandTimeoutError:
                self._interrupt = _Interrupt(StreamFailureSide.LOCAL, "SshCommandTimeoutError", "idle timeout")
                return False
            except SshError as exc:
                self._interrupt = _Interrupt(StreamFailureSide.DESTINATION, type(exc).__name__, self._clean(exc))
                return False
            offset += max(0, written)
        return True

    def _close_stdin(self) -> None:
        try:
            self._sink.close_stdin()
        except SshError as exc:
            self._interrupt = _Interrupt(StreamFailureSide.DESTINATION, type(exc).__name__, self._clean(exc))

    # -- exit wait --------------------------------------------------------

    def _await_exits(self) -> None:
        deadline = None if self._opt.close_grace is None else self._now() + self._opt.close_grace
        while not self._source.exited() or not self._sink.exited():
            if self._cancel is not None and self._cancel.is_set():
                self._interrupt = _Interrupt(StreamFailureSide.LOCAL, "SshCancelledError", "cancelled during exit wait")
                return
            if deadline is not None and self._now() >= deadline:
                self._interrupt = _Interrupt(StreamFailureSide.LOCAL, "SshCommandTimeoutError", "close grace exceeded")
                return

    # -- deadlines / cancellation ----------------------------------------

    def _deadline_or_cancel_hit(self) -> bool:
        if self._cancel is not None and self._cancel.is_set():
            self._interrupt = _Interrupt(StreamFailureSide.LOCAL, "SshCancelledError", "cancelled")
            return True
        now = self._now()
        if self._opt.total_timeout is not None and now - self._start > self._opt.total_timeout:
            self._interrupt = _Interrupt(StreamFailureSide.LOCAL, "SshCommandTimeoutError", "total timeout")
            return True
        if self._bytes == 0 and self._opt.start_timeout is not None and now - self._start > self._opt.start_timeout:
            self._interrupt = _Interrupt(StreamFailureSide.LOCAL, "SshCommandTimeoutError", "start timeout")
            return True
        if self._bytes > 0 and self._opt.idle_timeout is not None and now - self._last_progress > self._opt.idle_timeout:
            self._interrupt = _Interrupt(StreamFailureSide.LOCAL, "SshCommandTimeoutError", "idle timeout")
            return True
        return False

    def _budget(self) -> float | None:
        """The smallest remaining timeout budget to pass to a blocking call."""
        budgets = []
        now = self._now()
        if self._opt.idle_timeout is not None:
            budgets.append(max(0.0, self._last_progress + self._opt.idle_timeout - now))
        if self._opt.total_timeout is not None:
            budgets.append(max(0.0, self._start + self._opt.total_timeout - now))
        return min(budgets) if budgets else None

    # -- progress / redaction --------------------------------------------

    def _emit_progress(self) -> None:
        if self._progress is None:
            return
        now = self._now()
        if now - self._last_emit < self._opt.progress_interval:
            return
        self._last_emit = now
        # Counters only — never the transferred bytes themselves.
        self._progress(StreamProgress(bytes_transferred=self._bytes, elapsed_seconds=now - self._start))

    def _clean(self, text: object) -> str:
        return redact(text, self._secrets)

    # -- result -----------------------------------------------------------

    def _build_result(self) -> StreamResult:
        src_err, src_trunc = self._safe_stderr(self._source, self._opt.max_source_stderr_bytes)
        dst_err, dst_trunc = self._safe_stderr(self._sink, self._opt.max_dest_stderr_bytes)
        src_status, src_signal = self._safe_exit(self._source)
        dst_status, dst_signal = self._safe_exit(self._sink)
        outcome, side, err_type, message = self._classify(src_status, src_signal, dst_status, dst_signal)
        audit = StreamAudit(
            operation=self._operation, outcome=outcome.value, failure_side=side.value,
            bytes_transferred=self._bytes, source_exit_status=src_status, source_exit_signal=src_signal,
            dest_exit_status=dst_status, dest_exit_signal=dst_signal,
            source_stderr_bytes=len(src_err), dest_stderr_bytes=len(dst_err),
            source_stderr_truncated=src_trunc, dest_stderr_truncated=dst_trunc,
            error_type=err_type, message=message,
        )
        return StreamResult(
            outcome=outcome, failure_side=side, bytes_transferred=self._bytes,
            source_exit_status=src_status, source_exit_signal=src_signal,
            dest_exit_status=dst_status, dest_exit_signal=dst_signal,
            source_stderr=src_err, dest_stderr=dst_err,
            source_stderr_truncated=src_trunc, dest_stderr_truncated=dst_trunc,
            error_type=err_type, message=message, audit=audit,
        )

    def _classify(self, src_status, src_signal, dst_status, dst_signal):
        if self._interrupt is not None:
            return StreamOutcome.PARTIAL, self._interrupt.side, self._interrupt.error_type, self._interrupt.message
        if src_status not in (0, None) or src_signal is not None:
            return StreamOutcome.COMPLETED, StreamFailureSide.SOURCE, "SshNonZeroExit", "source exited non-zero"
        if dst_status not in (0, None) or dst_signal is not None:
            return StreamOutcome.COMPLETED, StreamFailureSide.DESTINATION, "SshNonZeroExit", "destination exited non-zero"
        return StreamOutcome.COMPLETED, StreamFailureSide.NONE, None, None

    def _safe_stderr(self, side, cap: int) -> tuple[bytes, bool]:
        try:
            data, trunc = side.stderr()
        except SshError:  # pragma: no cover - defensive
            return b"", False
        return (data[:cap], trunc or len(data) > cap)

    def _safe_exit(self, side) -> tuple[int | None, str | None]:
        try:
            return side.exit_status(), side.exit_signal()
        except SshError:  # pragma: no cover - defensive
            return None, None


def pump(
    source: ByteSource,
    sink: StdinSink,
    options: StreamOptions | None = None,
    *,
    cancel: threading.Event | None = None,
    progress: Callable[[StreamProgress], None] | None = None,
    monotonic: Callable[[], float] = time.monotonic,
    secrets: tuple[str, ...] = (),
    operation: str = "stream",
) -> StreamResult:
    """Copy ``source`` -> ``sink`` with backpressure; return a typed result.

    Never raises for cancel/timeout/disconnect/non-zero exit — those are reported
    in the returned :class:`StreamResult`. A started stream is never retried.
    """
    return _PumpRun(
        source, sink, options or StreamOptions(),
        cancel=cancel, progress=progress, monotonic=monotonic,
        secrets=secrets, operation=operation,
    ).run()


__all__ = [
    "ByteSource",
    "StdinSink",
    "StreamFailureSide",
    "StreamOutcome",
    "StreamOptions",
    "StreamProgress",
    "StreamAudit",
    "StreamResult",
    "pump",
]
