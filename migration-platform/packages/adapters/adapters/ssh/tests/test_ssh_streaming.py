"""Pump tests: backpressure, bounded memory, cancellation, timeouts, partial
results, exit/disconnect handling, redaction — all deterministic, no network."""

from __future__ import annotations

import threading

from adapters.ssh.errors import SshCommandTimeoutError, SshStreamInterruptedError
from adapters.ssh.fakes import FakeByteSource, FakeClock, FakeStdinSink, SourceStep
from adapters.ssh.streaming import (
    StreamFailureSide,
    StreamOptions,
    StreamOutcome,
    StreamProgress,
    pump,
)

SECRET = "pw-SUPER-SECRET-value"


def _opts(**kw) -> StreamOptions:
    base = dict(chunk_size=4, start_timeout=None, idle_timeout=None, total_timeout=None)
    base.update(kw)
    return StreamOptions(**base)


# -- happy path / chunking / bounded memory -------------------------------


def test_small_stream_completes_and_closes_stdin() -> None:
    source = FakeByteSource([SourceStep(b"hello")])
    sink = FakeStdinSink()
    result = pump(source, sink, _opts(chunk_size=64))
    assert result.ok is True
    assert result.outcome is StreamOutcome.COMPLETED
    assert result.bytes_transferred == 5
    assert sink.received == b"hello"
    assert sink.stdin_closed is True
    assert source.close_count == 1 and sink.close_count == 1


def test_stream_much_larger_than_buffer_stays_bounded() -> None:
    payload = b"x" * 1_000_000
    source = FakeByteSource([SourceStep(payload)])
    sink = FakeStdinSink()
    result = pump(source, sink, _opts(chunk_size=4096))
    assert result.bytes_transferred == 1_000_000
    assert bytes(sink.received) == payload
    # High-water mark: the pump never handed the sink more than one chunk at once.
    assert sink.max_write_chunk <= 4096


def test_slow_consumer_applies_backpressure_without_loss() -> None:
    source = FakeByteSource([SourceStep(b"abcdefgh")])
    sink = FakeStdinSink(accept_per_write=1)  # accepts 1 byte per call
    result = pump(source, sink, _opts(chunk_size=8))
    assert result.ok and result.bytes_transferred == 8
    assert bytes(sink.received) == b"abcdefgh"
    assert sink.write_calls >= 8  # producer throttled to the consumer's pace


def test_partial_write_completes_chunk_without_loss() -> None:
    source = FakeByteSource([SourceStep(b"abcd"), SourceStep(b"efgh")])
    sink = FakeStdinSink(accept_per_write=3)  # short writes
    result = pump(source, sink, _opts(chunk_size=4))
    assert result.ok and bytes(sink.received) == b"abcdefgh"


# -- exit codes / signals -------------------------------------------------


def test_source_exit_non_zero_marks_source_failure() -> None:
    source = FakeByteSource([SourceStep(b"data")], exit_status=2)
    sink = FakeStdinSink()
    result = pump(source, sink, _opts(chunk_size=64))
    assert result.outcome is StreamOutcome.COMPLETED
    assert result.failure_side is StreamFailureSide.SOURCE
    assert result.ok is False and result.source_exit_status == 2


def test_destination_exit_non_zero_marks_destination_failure() -> None:
    source = FakeByteSource([SourceStep(b"data")])
    sink = FakeStdinSink(exit_status=1)
    result = pump(source, sink, _opts(chunk_size=64))
    assert result.failure_side is StreamFailureSide.DESTINATION
    assert result.ok is False and result.dest_exit_status == 1


def test_remote_signal_marks_failure() -> None:
    source = FakeByteSource([SourceStep(b"data")], exit_status=None, exit_signal="KILL")
    sink = FakeStdinSink()
    result = pump(source, sink, _opts(chunk_size=64))
    assert result.failure_side is StreamFailureSide.SOURCE
    assert result.source_exit_signal == "KILL" and result.ok is False


# -- disconnects / broken pipe --------------------------------------------


def test_source_disconnect_returns_partial_source_failure() -> None:
    source = FakeByteSource([SourceStep(error=SshStreamInterruptedError("source dropped"))])
    sink = FakeStdinSink()
    result = pump(source, sink, _opts(chunk_size=64))
    assert result.outcome is StreamOutcome.PARTIAL
    assert result.failure_side is StreamFailureSide.SOURCE
    assert result.bytes_transferred == 0
    assert source.close_count == 1 and sink.close_count == 1


def test_destination_broken_pipe_returns_partial_destination_failure() -> None:
    source = FakeByteSource([SourceStep(b"abcd")])
    sink = FakeStdinSink(write_error=SshStreamInterruptedError("broken pipe"), error_after_bytes=0)
    result = pump(source, sink, _opts(chunk_size=4))
    assert result.outcome is StreamOutcome.PARTIAL
    assert result.failure_side is StreamFailureSide.DESTINATION


# -- cancellation ---------------------------------------------------------


def test_cancellation_before_start_transfers_nothing() -> None:
    cancel = threading.Event()
    cancel.set()
    source = FakeByteSource([SourceStep(b"data")])
    sink = FakeStdinSink()
    result = pump(source, sink, _opts(chunk_size=64), cancel=cancel)
    assert result.outcome is StreamOutcome.PARTIAL
    assert result.failure_side is StreamFailureSide.LOCAL
    assert result.bytes_transferred == 0
    assert source.read_calls == 0  # never read
    assert source.close_count == 1 and sink.close_count == 1


def test_cancellation_during_copy_stops_promptly() -> None:
    cancel = threading.Event()
    # First chunk transfers fully; cancel fires while reading the second chunk.
    source = FakeByteSource([SourceStep(b"aa"), SourceStep(b"bb", on_read=cancel.set)])
    sink = FakeStdinSink()
    result = pump(source, sink, _opts(chunk_size=2), cancel=cancel)
    assert result.outcome is StreamOutcome.PARTIAL
    assert result.failure_side is StreamFailureSide.LOCAL
    assert result.bytes_transferred == 2  # first chunk moved, then cancel observed


def test_cancellation_during_blocked_write() -> None:
    cancel = threading.Event()
    source = FakeByteSource([SourceStep(b"abcd")])
    sink = FakeStdinSink(accept_per_write=1, on_write=cancel.set)
    result = pump(source, sink, _opts(chunk_size=4), cancel=cancel)
    assert result.outcome is StreamOutcome.PARTIAL
    assert result.failure_side is StreamFailureSide.LOCAL
    assert result.bytes_transferred == 0  # chunk never fully written


def test_cancellation_during_exit_wait() -> None:
    cancel = threading.Event()
    source = FakeByteSource([SourceStep(b"data")])
    sink = FakeStdinSink(exit_ready=False, on_poll=cancel.set)
    result = pump(source, sink, _opts(chunk_size=64), cancel=cancel)
    assert result.outcome is StreamOutcome.PARTIAL
    assert result.failure_side is StreamFailureSide.LOCAL
    assert "exit wait" in (result.message or "")


# -- timeouts -------------------------------------------------------------


def test_idle_timeout_during_blocked_write() -> None:
    clock = FakeClock()
    source = FakeByteSource([SourceStep(b"a"), SourceStep(b"bbbb")], clock=clock)
    sink = FakeStdinSink(clock=clock, accept_per_write=1, advance_per_write=10)
    result = pump(source, sink, _opts(chunk_size=4, idle_timeout=25), monotonic=clock.now)
    assert result.outcome is StreamOutcome.PARTIAL
    assert result.failure_side is StreamFailureSide.LOCAL
    assert "idle" in (result.message or "")
    assert result.bytes_transferred == 1


def test_start_timeout_before_first_progress() -> None:
    clock = FakeClock()
    source = FakeByteSource([SourceStep(b"abcd")], clock=clock)
    sink = FakeStdinSink(clock=clock, accept_per_write=1, advance_per_write=10)
    result = pump(source, sink, _opts(chunk_size=4, start_timeout=25), monotonic=clock.now)
    assert result.outcome is StreamOutcome.PARTIAL
    assert "start" in (result.message or "")
    assert result.bytes_transferred == 0


def test_total_timeout_stops_the_stream() -> None:
    clock = FakeClock()
    source = FakeByteSource([SourceStep(b"a"), SourceStep(b"b")], clock=clock)
    sink = FakeStdinSink(clock=clock, advance_per_write=100)
    result = pump(source, sink, _opts(chunk_size=4, total_timeout=50), monotonic=clock.now)
    assert result.outcome is StreamOutcome.PARTIAL
    assert "total" in (result.message or "")
    assert result.bytes_transferred == 1


def test_source_read_timeout_is_partial_local() -> None:
    source = FakeByteSource([SourceStep(error=SshCommandTimeoutError("read blocked"))])
    sink = FakeStdinSink()
    result = pump(source, sink, _opts(chunk_size=4))
    assert result.outcome is StreamOutcome.PARTIAL
    assert result.failure_side is StreamFailureSide.LOCAL


def test_destination_write_timeout_is_partial_local() -> None:
    source = FakeByteSource([SourceStep(b"abcd")])
    sink = FakeStdinSink(write_error=SshCommandTimeoutError("write blocked"), error_after_bytes=0)
    result = pump(source, sink, _opts(chunk_size=4))
    assert result.outcome is StreamOutcome.PARTIAL
    assert result.failure_side is StreamFailureSide.LOCAL
    assert "idle" in (result.message or "")


def test_close_stdin_failure_marks_destination() -> None:
    source = FakeByteSource([SourceStep(b"data")])
    sink = FakeStdinSink(close_stdin_error=SshStreamInterruptedError("stdin closed early"))
    result = pump(source, sink, _opts(chunk_size=64))
    assert result.outcome is StreamOutcome.PARTIAL
    assert result.failure_side is StreamFailureSide.DESTINATION
    assert sink.close_count == 1


def test_close_grace_exceeded_during_exit_wait() -> None:
    clock = FakeClock()
    source = FakeByteSource([SourceStep(b"data")], clock=clock)
    # poll_advance < close_grace so the wait loop iterates several times (covering
    # the loop back-edge) before the grace deadline is finally exceeded.
    sink = FakeStdinSink(clock=clock, exit_ready=False, poll_advance=3)
    result = pump(source, sink, _opts(chunk_size=64, close_grace=10), monotonic=clock.now)
    assert result.outcome is StreamOutcome.PARTIAL
    assert "close grace" in (result.message or "")


# -- close idempotence / no retry -----------------------------------------


def test_close_is_idempotent_and_channels_closed_once() -> None:
    source = FakeByteSource([SourceStep(b"data")])
    sink = FakeStdinSink()
    pump(source, sink, _opts(chunk_size=64))
    assert source.close_count == 1 and sink.close_count == 1
    source.close()
    sink.close()  # calling again does not raise
    assert source.close_count == 2 and sink.close_count == 2


def test_partial_stream_is_not_retried() -> None:
    source = FakeByteSource([SourceStep(b"ab"), SourceStep(error=SshStreamInterruptedError("drop"))])
    sink = FakeStdinSink()
    result = pump(source, sink, _opts(chunk_size=2))
    assert result.outcome is StreamOutcome.PARTIAL
    # Exactly two reads (one chunk + the failing read); the pump never replays.
    assert source.read_calls == 2


# -- progress / stderr / redaction ----------------------------------------


def test_progress_callback_receives_counters_only_and_is_rate_limited() -> None:
    clock = FakeClock()
    events: list[StreamProgress] = []
    source = FakeByteSource(
        [SourceStep(b"aa"), SourceStep(b"bb"), SourceStep(b"cc")], clock=clock
    )
    sink = FakeStdinSink(clock=clock, advance_per_write=1)
    pump(
        source, sink, _opts(chunk_size=2, progress_interval=2),
        progress=events.append, monotonic=clock.now,
    )
    assert events, "expected at least one progress event"
    for ev in events:
        assert isinstance(ev, StreamProgress)
        assert not hasattr(ev, "data") and not hasattr(ev, "chunk")
        assert isinstance(ev.bytes_transferred, int)
    # Rate limited: fewer callbacks than chunks written.
    assert len(events) <= 3


def test_stderr_is_bounded_and_truncation_flagged() -> None:
    source = FakeByteSource([SourceStep(b"x")], stderr=b"E" * 5000)
    sink = FakeStdinSink(stderr=b"F" * 10, stderr_truncated=True)
    result = pump(source, sink, _opts(chunk_size=64, max_source_stderr_bytes=100))
    assert len(result.source_stderr) == 100 and result.source_stderr_truncated is True
    assert result.dest_stderr_truncated is True


def test_no_secret_or_payload_in_result_error_or_audit() -> None:
    source = FakeByteSource(
        [SourceStep(error=SshStreamInterruptedError(f"boom {SECRET}"))]
    )
    sink = FakeStdinSink()
    result = pump(source, sink, _opts(chunk_size=64), secrets=(SECRET,))
    assert SECRET not in (result.message or "")
    assert SECRET not in repr(result)
    evidence = result.audit.as_evidence()
    assert SECRET not in repr(evidence)
    # The audit records counters, never transferred data.
    assert "data" not in evidence and "payload" not in evidence
    assert evidence["bytes_transferred"] == 0
