#!/usr/bin/env python3
"""A stand-in for the Go ``cpanel-self-migration execute`` bridge.

The real executor opens SSH to both endpoints; a unit test cannot. This script
speaks the *same* contract instead — it consumes an execution-spec-v1 document
and emits execution-event-v1 lines plus an execution-result-v1 report — so the
Python bridge engine can be driven end to end without a network, a real binary,
or a sacrificial account.

Its behaviour is chosen by ``BRIDGE_FAKE_MODE`` in the environment the engine
hands it. This is a *test fixture*, not a test: pytest does not collect it (no
``test_`` prefix), and it is executed only as a subprocess.

Every document it writes is a real, valid contract document — the point is to
prove the engine ingests and classifies genuine executor output, not a mock.
"""

from __future__ import annotations

import json
import os
import sys
import time
from datetime import datetime, timezone


def _now() -> str:
    # RFC3339 with an explicit offset, as the contract requires.
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f000+00:00")


def _host(ip: str, user: str) -> dict:
    return {"ip": ip, "user": user}


def _event(run_id: str, event: str, phase: str, level: str, message: str) -> dict:
    return {
        "format_version": 1,
        "run_id": run_id,
        "ts": _now(),
        "level": level,
        "phase": phase,
        "event": event,
        "message": message,
        "source": _host("203.0.113.10", "src"),
        "destination": _host("203.0.113.20", "dst"),
    }


def _report(run_id: str, exit_status: str, phases: list[str], artifacts: dict) -> dict:
    started = _now()
    return {
        "format_version": 1,
        "run_id": run_id,
        "version": "fake-executor/0.0.0-test",
        "mode": "dry-run",
        "scope": {"mail": True, "files": False, "databases": False},
        "source": _host("203.0.113.10", "src"),
        "destination": _host("203.0.113.20", "dst"),
        "started_at": started,
        "finished_at": _now(),
        "exit_status": exit_status,
        "phases_completed": phases,
        "warnings": [],
        "errors": [] if exit_status == "success" else ["dry-run reported a failure"],
        "artifacts": artifacts,
    }


def _parse_args(argv: list[str]) -> dict:
    # argv is what follows the executable path: ["execute", "--spec", P,
    # "--config", C, "--output-dir", D]. Mirror the Go flag surface loosely.
    out: dict[str, str] = {}
    i = 0
    while i < len(argv):
        tok = argv[i]
        if tok in ("--spec", "--config", "--output-dir") and i + 1 < len(argv):
            out[tok.lstrip("-")] = argv[i + 1]
            i += 2
        else:
            i += 1
    return out


def main(argv: list[str]) -> int:
    mode = os.environ.get("BRIDGE_FAKE_MODE", "success")

    # A record of what the executor saw, so a test can assert the engine passed a
    # stripped environment and the expected argv. Written only if asked.
    probe_path = os.environ.get("BRIDGE_FAKE_ENV_PROBE")
    if probe_path:
        with open(probe_path, "w", encoding="utf-8") as fh:
            json.dump({"env_keys": sorted(os.environ), "argv": argv}, fh)

    args = _parse_args(argv)
    spec_path = args.get("spec")
    if not spec_path:
        sys.stderr.write("fake-executor: missing --spec\n")
        return 2
    with open(spec_path, encoding="utf-8") as fh:
        spec = json.load(fh)
    run_id = spec["run_id"]
    if mode == "run_id_mismatch":
        run_id_report = run_id + "-DIFFERENT"
    else:
        run_id_report = run_id

    out_dir = args.get("output-dir") or os.getcwd()
    events_path = os.path.join(out_dir, "events.jsonl")
    report_path = os.path.join(out_dir, "report.json")

    def emit(ev: dict) -> None:
        with open(events_path, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(ev) + "\n")

    emit(_event(run_id, "run_started", "", "info", "dry-run started"))

    if mode == "hang":
        # Never terminates on its own: the engine's timeout must reap it.
        time.sleep(3600)
        return 0

    if mode == "bad_event":
        # A line that is NOT a valid execution-event-v1 (unknown event type).
        with open(events_path, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(_event(run_id, "run_started", "", "info", "ok")
                                 | {"event": "not_a_real_event"}) + "\n")

    emit(_event(run_id, "phase_started", "connect", "info", "connecting"))
    emit(_event(run_id, "phase_completed", "connect", "info", "connected"))

    if mode == "failed":
        emit(_event(run_id, "run_failed", "", "error", "dry-run failed"))
        _write_report(report_path, _report(run_id_report, "failed", ["connect"],
                                            {"events": "events.jsonl", "report": "report.json"}))
        return 1

    emit(_event(run_id, "run_completed", "", "info", "dry-run completed"))

    if mode == "missing_report":
        # Exit cleanly but never write report.json.
        return 0

    if mode == "bad_report":
        # A report that violates the contract (unknown exit_status).
        bad = _report(run_id_report, "success", ["connect"], {})
        bad["exit_status"] = "totally_bogus"
        _write_report(report_path, bad)
        return 0

    _write_report(report_path, _report(run_id_report, "success", ["connect"],
                                        {"events": "events.jsonl", "report": "report.json"}))
    return 0


def _write_report(path: str, report: dict) -> None:
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(report, fh)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
