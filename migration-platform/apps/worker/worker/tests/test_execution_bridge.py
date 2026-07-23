"""The dry-run execution bridge: run the executor, ingest its output, classify.

This is the M3 core (EXECUTION_ROADMAP.md §M3): given a verified executor path
and an SSH workspace built elsewhere, generate the canonical spec, launch the
subprocess in its own process group with a stripped environment and a timeout,
ingest and *validate* events.jsonl and report.json against the execution
contract, and classify the terminal outcome — with `partial` unreachable
because a dry run never writes.

The engine takes plain paths, never a DB session or a Dramatiq message, so it is
driven here by a real contract-speaking fake executor, no network required. The
actor that wires it to leases, the SSH workspace (#118) and the verified
artifact (#119) is a separate increment.
"""

from __future__ import annotations

import hashlib
import json
import os
import stat
import sys
from pathlib import Path

import pytest

from domain.execution_spec import build_execution_spec, canonical_spec_bytes
from worker import execution_bridge as eb

FAKE = Path(__file__).with_name("fake_executor.py")
# The executor is expected to arrive executable (the Go binary is staged 0o500);
# the fixture script ships as source, so mark it runnable for the subprocess.
FAKE.chmod(0o755)


def _spec(run_id: str = "run-abc") -> dict:
    return build_execution_spec(
        run_id=run_id,
        plan_id=1,
        source_snapshot_id=2,
        destination_snapshot_id=3,
        comparison_report_id=4,
        mail=True,
        files=False,
        databases=False,
    )


def _env(mode: str, **extra: str) -> dict[str, str]:
    # The executor shells out to python via its shebang, so PATH must survive;
    # everything else is intentionally absent to prove the engine does not leak
    # the worker's environment into the subprocess.
    return {"PATH": os.environ["PATH"], "BRIDGE_FAKE_MODE": mode, **extra}


@pytest.fixture
def workspace(tmp_path: Path) -> Path:
    # Stand-in for the SSH workspace #118 builds: only host.yaml needs to exist;
    # the engine passes it through to --config and never parses it.
    (tmp_path / "host.yaml").write_text("# fake host config\n", encoding="utf-8")
    return tmp_path


@pytest.fixture
def output_dir(tmp_path: Path) -> Path:
    d = tmp_path / "run-artifacts"
    d.mkdir()
    return d


def _run(workspace: Path, output_dir: Path, mode: str, *, spec: dict | None = None,
         timeout: float = 30.0, **env_extra: str) -> eb.ExecutionOutcome:
    return eb.run_execution(
        executable_path=FAKE,
        host_config_path=workspace / "host.yaml",
        spec=spec or _spec(),
        output_dir=output_dir,
        timeout_seconds=timeout,
        env=_env(mode, **env_extra),
    )


# --- materialize_spec -------------------------------------------------------


def test_materialize_spec_writes_canonical_bytes_private(tmp_path: Path) -> None:
    spec = _spec()
    path = eb.materialize_spec(spec, tmp_path)

    assert path.read_bytes() == canonical_spec_bytes(spec)
    mode = stat.S_IMODE(path.stat().st_mode)
    assert mode == 0o600, f"spec must be private, got {oct(mode)}"


def test_materialize_spec_rejects_an_invalid_spec(tmp_path: Path) -> None:
    from domain.execution_contract import ContractError

    bogus = {"format_version": 1, "run_id": "x", "mode": "dry_run"}  # missing scope/ids
    with pytest.raises(ContractError):
        eb.materialize_spec(bogus, tmp_path)


# --- happy path -------------------------------------------------------------


def test_successful_dry_run_is_terminalized_as_succeeded(workspace, output_dir) -> None:
    outcome = _run(workspace, output_dir, "success")

    assert outcome.terminal_status == "succeeded"
    assert outcome.return_code == 0
    assert outcome.report is not None
    assert outcome.report["exit_status"] == "success"
    # Every event was ingested and validated; the stream is ordered as emitted.
    types = [e["event"] for e in outcome.events]
    assert types[0] == "run_started"
    assert types[-1] == "run_completed"


def test_artifacts_manifest_records_digests(workspace, output_dir) -> None:
    outcome = _run(workspace, output_dir, "success")

    names = {a.name for a in outcome.artifacts}
    assert names == {"events.jsonl", "report.json"}
    for art in outcome.artifacts:
        raw = art.path.read_bytes()
        assert art.sha256 == hashlib.sha256(raw).hexdigest()
        assert art.size_bytes == len(raw)


# --- failure classification -------------------------------------------------


def test_reported_failure_is_terminalized_as_failed(workspace, output_dir) -> None:
    outcome = _run(workspace, output_dir, "failed")

    assert outcome.terminal_status == "failed"
    assert outcome.return_code == 1
    assert outcome.report["exit_status"] == "failed"


def test_a_dry_run_never_terminalizes_as_partial(workspace, output_dir) -> None:
    # A dry run writes nothing, so no failure mode may ever be `partial`.
    for mode in ("success", "failed"):
        d = output_dir.parent / f"art-{mode}"
        d.mkdir()
        outcome = _run(workspace, d, mode)
        assert outcome.terminal_status != "partial"


# --- timeout / process group ------------------------------------------------


def test_a_hanging_executor_is_reaped_and_interrupted(workspace, output_dir) -> None:
    import time

    start = time.monotonic()
    outcome = _run(workspace, output_dir, "hang", timeout=1.0)
    elapsed = time.monotonic() - start

    assert outcome.terminal_status == "interrupted"
    assert elapsed < 20.0, "the engine must reap the child, not wait for its sleep"
    assert outcome.report is None


# --- contract enforcement on ingestion --------------------------------------


def test_an_invalid_event_line_fails_ingestion(workspace, output_dir) -> None:
    with pytest.raises(eb.EventIngestionError):
        _run(workspace, output_dir, "bad_event")


def test_a_clean_exit_without_a_report_is_a_contract_breach(workspace, output_dir) -> None:
    with pytest.raises(eb.ResultIngestionError):
        _run(workspace, output_dir, "missing_report")


def test_an_invalid_report_fails_ingestion(workspace, output_dir) -> None:
    with pytest.raises(eb.ResultIngestionError):
        _run(workspace, output_dir, "bad_report")


def test_a_report_for_a_different_run_is_rejected(workspace, output_dir) -> None:
    with pytest.raises(eb.ResultIngestionError):
        _run(workspace, output_dir, "run_id_mismatch")


# --- launch preconditions ---------------------------------------------------


def test_a_reused_workspace_is_refused_before_launch(workspace, output_dir) -> None:
    # One execution = one artifact set. A directory already holding a previous
    # run's report is evidence; the engine must refuse it, not append.
    (output_dir / "report.json").write_text("{}", encoding="utf-8")
    with pytest.raises(eb.ExecutorLaunchError):
        _run(workspace, output_dir, "success")


def test_a_missing_executable_is_refused(workspace, output_dir) -> None:
    with pytest.raises(eb.ExecutorLaunchError):
        eb.run_execution(
            executable_path=workspace / "does-not-exist",
            host_config_path=workspace / "host.yaml",
            spec=_spec(),
            output_dir=output_dir,
            timeout_seconds=5.0,
            env=_env("success"),
        )


def test_a_missing_host_config_is_refused(workspace, output_dir) -> None:
    with pytest.raises(eb.ExecutorLaunchError):
        eb.run_execution(
            executable_path=FAKE,
            host_config_path=workspace / "absent.yaml",
            spec=_spec(),
            output_dir=output_dir,
            timeout_seconds=5.0,
            env=_env("success"),
        )


# --- environment isolation --------------------------------------------------


def test_the_subprocess_environment_is_exactly_what_the_engine_is_given(
    workspace, output_dir, monkeypatch
) -> None:
    # A secret in the worker's own environment must not reach the executor.
    monkeypatch.setenv("BRIDGE_LEAKED_SECRET", "do-not-forward")
    probe = output_dir.parent / "env-probe.json"

    _run(workspace, output_dir, "success", BRIDGE_FAKE_ENV_PROBE=str(probe))

    seen = json.loads(probe.read_text())
    keys = set(seen["env_keys"])
    # The engine hands the executor exactly the environment it is given at
    # execve; the interpreter/OS may then add its own vars (LC_CTYPE,
    # __CF_USER_TEXT_ENCODING), but NONE of the worker's environment is inherited.
    assert {"PATH", "BRIDGE_FAKE_MODE", "BRIDGE_FAKE_ENV_PROBE"} <= keys
    for inherited in ("BRIDGE_LEAKED_SECRET", "HOME", "USER", "PWD"):
        assert inherited not in keys, f"{inherited} leaked into the executor"
    # The engine invokes the executor's `execute` subcommand with our flags.
    assert seen["argv"][0] == "execute"
    assert "--spec" in seen["argv"] and "--config" in seen["argv"]
