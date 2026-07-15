"""Tests for the fail-closed pytest-report gate (check_pytest_report.py).

These exercise the guard's decisions directly, including the base-aware
enforcement of the PostgreSQL module. The four base/head scenarios are realised
here as the (require flag, module present?) combinations the CI workflow derives:

    base absent + head absent   -> require off, module absent  -> pass
    base absent + head present  -> require on,  module present -> pass
    base present + head present -> require on,  module present -> pass
    base present + head absent  -> require on,  module absent  -> FAIL
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

SCRIPT = Path(__file__).with_name("check_pytest_report.py")
PG_FILE = "app/tests/test_execution_attempts_pg.py"


def _write_report(
    path: Path,
    *,
    plain: int = 0,
    pg: int = 0,
    pg_skipped: int = 0,
    plain_failed: int = 0,
) -> Path:
    """Write a minimal JUnit XML with the requested testcase mix."""
    cases: list[str] = []
    for i in range(plain):
        body = "<failure/>" if i < plain_failed else ""
        cases.append(
            f'<testcase classname="app.tests.test_plain" '
            f'file="app/tests/test_plain.py" name="t{i}">{body}</testcase>'
        )
    for i in range(pg):
        body = "<skipped/>" if i < pg_skipped else ""
        cases.append(
            f'<testcase classname="app.tests.test_execution_attempts_pg" '
            f'file="{PG_FILE}" name="pg{i}">{body}</testcase>'
        )
    total = plain + pg
    xml = (
        f'<?xml version="1.0" encoding="utf-8"?>'
        f'<testsuites><testsuite name="pytest" tests="{total}">'
        f'{"".join(cases)}</testsuite></testsuites>'
    )
    path.write_text(xml, encoding="utf-8")
    return path


def _run(report: Path, *extra: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(SCRIPT), str(report), *extra],
        capture_output=True,
        text=True,
    )


# --- base/head matrix -------------------------------------------------------


def test_base_absent_head_absent_not_required_passes(tmp_path: Path) -> None:
    report = _write_report(tmp_path / "r.xml", plain=5)
    result = _run(report, "--min-total", "5", "--pg-min", "9")
    assert result.returncode == 0, result.stdout
    assert "GATE OK" in result.stdout


def test_base_present_head_absent_required_fails(tmp_path: Path) -> None:
    # Module removed/renamed: 0 pg tests but required -> must fail.
    report = _write_report(tmp_path / "r.xml", plain=5)
    result = _run(report, "--min-total", "5", "--pg-min", "9", "--require-pg-module")
    assert result.returncode == 1, result.stdout
    assert "required but ran 0 tests" in result.stdout


def test_base_absent_head_present_required_passes(tmp_path: Path) -> None:
    report = _write_report(tmp_path / "r.xml", plain=5, pg=9)
    result = _run(report, "--min-total", "5", "--pg-min", "9", "--require-pg-module")
    assert result.returncode == 0, result.stdout
    assert "pg_module=9" in result.stdout


def test_base_present_head_present_required_passes(tmp_path: Path) -> None:
    report = _write_report(tmp_path / "r.xml", plain=5, pg=12)
    result = _run(report, "--min-total", "5", "--pg-min", "9", "--require-pg-module")
    assert result.returncode == 0, result.stdout


# --- floor / skip enforcement while the module is present -------------------


def test_module_present_below_floor_fails(tmp_path: Path) -> None:
    report = _write_report(tmp_path / "r.xml", plain=5, pg=8)
    result = _run(report, "--min-total", "5", "--pg-min", "9", "--require-pg-module")
    assert result.returncode == 1, result.stdout
    assert "expected >= 9" in result.stdout


def test_module_present_with_skip_fails(tmp_path: Path) -> None:
    report = _write_report(tmp_path / "r.xml", plain=5, pg=9, pg_skipped=1)
    result = _run(report, "--min-total", "5", "--pg-min", "9", "--require-pg-module")
    assert result.returncode == 1, result.stdout
    # A skip trips both the global no-skip rule and the pg-skip rule.
    assert "skipped" in result.stdout


# --- generic guarantees remain intact --------------------------------------


def test_any_skip_fails_even_without_require(tmp_path: Path) -> None:
    report = _write_report(tmp_path / "r.xml", plain=5, pg=9, pg_skipped=1)
    result = _run(report, "--min-total", "5", "--pg-min", "9")
    assert result.returncode == 1, result.stdout


def test_below_min_total_fails(tmp_path: Path) -> None:
    report = _write_report(tmp_path / "r.xml", plain=3)
    result = _run(report, "--min-total", "5")
    assert result.returncode == 1, result.stdout
    assert "expected >= 5" in result.stdout


def test_failure_fails(tmp_path: Path) -> None:
    report = _write_report(tmp_path / "r.xml", plain=5, plain_failed=1)
    result = _run(report, "--min-total", "5")
    assert result.returncode == 1, result.stdout
