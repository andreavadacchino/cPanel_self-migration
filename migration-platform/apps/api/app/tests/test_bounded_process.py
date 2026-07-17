"""A subprocess bounded while it runs, not judged after it finishes.

The contract: memory proportional to ``max_stdout_bytes`` whatever the child
does, a timeout that actually stops it, no descendant left alive, no zombie,
and no child output in any error — an exception's text reaches CI's JUnit
artifact.

The children here are small POSIX shell scripts. Determinism comes from the
observable facts (a pid file that appears, a process that stops existing), not
from sleeping and hoping.
"""

from __future__ import annotations

import os
import signal
import subprocess
import sys
import time
from pathlib import Path

import pytest
from adapters.bounded_process import (
    ProcessOutputLimitError,
    ProcessStartError,
    ProcessTimeoutError,
    run_bounded_process,
)


def _script(path: Path, body: str) -> Path:
    path.write_text("#!/bin/sh\n" + body + "\n")
    path.chmod(0o700)
    return path


def _alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:  # pragma: no cover - alive but not ours
        return True
    return True


def _wait_gone(pid: int, deadline_seconds: float = 5.0) -> bool:
    """Poll until the pid is gone. A deadline, never a bare sleep."""
    end = time.monotonic() + deadline_seconds
    while time.monotonic() < end:
        if not _alive(pid):
            return True
        time.sleep(0.01)
    return False


def test_a_small_answer_comes_back_whole(tmp_path: Path) -> None:
    script = _script(tmp_path / "ok", "printf 'hello'")

    result = run_bounded_process([str(script)], timeout_seconds=5, max_stdout_bytes=1024)

    assert result.returncode == 0
    assert result.stdout == b"hello"


def test_a_nonzero_exit_is_reported_not_raised(tmp_path: Path) -> None:
    script = _script(tmp_path / "fail", "printf 'partial'; exit 3")

    result = run_bounded_process([str(script)], timeout_seconds=5, max_stdout_bytes=1024)

    assert result.returncode == 3
    assert result.stdout == b"partial"


def test_output_is_bounded_while_it_is_read(tmp_path: Path) -> None:
    """A child that floods gigabytes must not be buffered before the verdict.

    The limit is small and the child's output is effectively unbounded: if the
    implementation read to EOF first, this test would hang or die on memory.
    """
    script = _script(tmp_path / "flood", "yes AAAAAAAAAAAAAAAAAAAAAAAA")

    with pytest.raises(ProcessOutputLimitError):
        run_bounded_process([str(script)], timeout_seconds=30, max_stdout_bytes=4096)


def test_an_overflowing_child_is_killed_and_reaped(tmp_path: Path) -> None:
    script = _script(
        tmp_path / "flood",
        f"echo $$ > {tmp_path / 'pid'}\nyes AAAAAAAAAAAAAAAAAAAAAAAA",
    )

    with pytest.raises(ProcessOutputLimitError):
        run_bounded_process([str(script)], timeout_seconds=30, max_stdout_bytes=4096)

    pid = int((tmp_path / "pid").read_text().strip())
    assert _wait_gone(pid), "the flooding child survived the output limit"


def test_a_silent_child_hits_the_timeout_and_is_killed(tmp_path: Path) -> None:
    script = _script(
        tmp_path / "sleeper", f"echo $$ > {tmp_path / 'pid'}\nsleep 60"
    )

    with pytest.raises(ProcessTimeoutError):
        run_bounded_process([str(script)], timeout_seconds=0.5, max_stdout_bytes=1024)

    pid = int((tmp_path / "pid").read_text().strip())
    assert _wait_gone(pid), "the sleeping child survived the timeout"


def test_a_stderr_flood_does_not_deadlock(tmp_path: Path) -> None:
    """stderr is DEVNULL: an unread pipe would block the child forever once its
    buffer filled, and the timeout would be the only way out."""
    script = _script(
        tmp_path / "noisy",
        "yes BBBBBBBBBBBBBBBBBBBBBBBBBBBB | head -c 2000000 >&2\nprintf 'done'",
    )

    result = run_bounded_process([str(script)], timeout_seconds=20, max_stdout_bytes=1024)

    assert result.stdout == b"done"
    assert result.returncode == 0


def test_a_descendant_is_killed_with_the_process_group(tmp_path: Path) -> None:
    """The child's own child must not outlive the run: a runner that kills only
    the direct pid leaves the grandchild holding the pipe."""
    pid_file = tmp_path / "grandchild-pid"
    script = _script(
        tmp_path / "spawner",
        f"( echo $$ > {pid_file}; sleep 60 ) &\nsleep 60",
    )

    with pytest.raises(ProcessTimeoutError):
        run_bounded_process([str(script)], timeout_seconds=1.0, max_stdout_bytes=1024)

    # The grandchild wrote its pid before sleeping; the file exists by the time
    # the timeout fires.
    grandchild = int(pid_file.read_text().strip())
    assert _wait_gone(grandchild), "a descendant survived the process-group kill"


def test_no_zombie_is_left_behind(tmp_path: Path) -> None:
    script = _script(tmp_path / "quick", "printf 'x'")

    result = run_bounded_process([str(script)], timeout_seconds=5, max_stdout_bytes=1024)

    assert result.returncode == 0
    # Every child this process spawned has been reaped: waitpid(-1) must find
    # nothing left to collect.
    with pytest.raises(ChildProcessError):
        os.waitpid(-1, os.WNOHANG)


def test_the_environment_is_exactly_what_is_passed(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("SOURCE_CPANEL_SSH_PASSWORD", "env-sentinel-0xBEEF")
    script = _script(tmp_path / "env", 'printf "%s" "${SOURCE_CPANEL_SSH_PASSWORD:-clean}"')

    result = run_bounded_process(
        [str(script)], timeout_seconds=5, max_stdout_bytes=1024, env={}
    )

    assert result.stdout == b"clean"


def test_a_missing_executable_is_a_start_error(tmp_path: Path) -> None:
    with pytest.raises(ProcessStartError):
        run_bounded_process(
            [str(tmp_path / "absent")], timeout_seconds=5, max_stdout_bytes=1024
        )


def test_errors_carry_no_child_output(tmp_path: Path) -> None:
    script = _script(tmp_path / "loud", "yes SECRET-SENTINEL-0xF00D")

    with pytest.raises(ProcessOutputLimitError) as excinfo:
        run_bounded_process([str(script)], timeout_seconds=30, max_stdout_bytes=4096)

    assert "SECRET-SENTINEL-0xF00D" not in str(excinfo.value)


def test_output_exactly_at_the_limit_is_accepted(tmp_path: Path) -> None:
    script = _script(tmp_path / "exact", "head -c 1024 /dev/zero | tr '\\0' 'a'")

    result = run_bounded_process([str(script)], timeout_seconds=10, max_stdout_bytes=1024)

    assert len(result.stdout) == 1024


def test_one_byte_over_the_limit_is_refused(tmp_path: Path) -> None:
    script = _script(tmp_path / "over", "head -c 1025 /dev/zero | tr '\\0' 'a'")

    with pytest.raises(ProcessOutputLimitError):
        run_bounded_process([str(script)], timeout_seconds=10, max_stdout_bytes=1024)


def test_a_child_that_exits_before_being_read_is_still_collected(
    tmp_path: Path,
) -> None:
    """EOF can arrive with the process already gone; the runner must still
    return its output and its status rather than race."""
    script = _script(tmp_path / "fast", "printf 'instant'; exit 0")
    time.sleep(0)  # no synchronisation needed: the assertion is on the result

    result = run_bounded_process([str(script)], timeout_seconds=5, max_stdout_bytes=1024)

    assert result.stdout == b"instant"
    assert result.returncode == 0


def test_the_runner_does_not_use_a_shell_or_path_lookup(tmp_path: Path) -> None:
    """`sh` exists on PATH; a bare name must still fail, because argv[0] is a
    path to an artifact, never a name resolved at launch."""
    with pytest.raises(ProcessStartError):
        run_bounded_process(["sh"], timeout_seconds=5, max_stdout_bytes=1024)


@pytest.mark.skipif(sys.platform == "win32", reason="POSIX signals")
def test_a_child_killed_by_a_signal_reports_a_negative_returncode(
    tmp_path: Path,
) -> None:
    script = _script(tmp_path / "selfkill", "kill -TERM $$")

    result = run_bounded_process([str(script)], timeout_seconds=5, max_stdout_bytes=1024)

    assert result.returncode == -signal.SIGTERM


def test_stdin_is_closed_for_the_child(tmp_path: Path) -> None:
    """A child that reads stdin must see EOF, not the worker's terminal."""
    script = _script(tmp_path / "reader", "cat; printf 'eof'")

    result = run_bounded_process([str(script)], timeout_seconds=5, max_stdout_bytes=1024)

    assert result.stdout == b"eof"


def test_the_runner_rejects_an_empty_argv() -> None:
    with pytest.raises(ValueError):
        run_bounded_process([], timeout_seconds=5, max_stdout_bytes=1024)


def test_a_relative_argv0_is_refused(tmp_path: Path) -> None:
    """argv[0] must be an absolute path to a verified artifact: a relative path
    is resolved against a cwd this module does not own."""
    with pytest.raises(ValueError):
        run_bounded_process(["./executor"], timeout_seconds=5, max_stdout_bytes=1024)


def test_subprocess_is_not_used_with_capture_output() -> None:
    """Guard the property this module exists for: reading to EOF and judging
    afterwards is exactly the defect the bounded runner replaces."""
    source = Path(subprocess.__file__)  # sanity: stdlib import is the real one
    assert source.exists()

    from adapters import bounded_process

    text = Path(bounded_process.__file__).read_text()
    assert "capture_output" not in text
    assert ".communicate(" not in text
