#!/usr/bin/env python3
"""Fail-closed gate over a pytest JUnit XML report.

Console text is fragile; this reads the structured JUnit report pytest writes
with ``--junitxml`` and fails when a required guarantee is not met:

  - any skipped test at all (a silent skip is how a "green" run hides that the
    PostgreSQL suite never ran because TEST_POSTGRES_URL was unset);
  - fewer than a floor of total tests (a broken collection);
  - the PostgreSQL module present but under-run or partly skipped.

Usage:
    check_pytest_report.py <report.xml> --min-total N --pg-module NAME --pg-min N
"""

from __future__ import annotations

import argparse
import sys
import xml.etree.ElementTree as ET


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("report")
    parser.add_argument("--min-total", type=int, default=0)
    parser.add_argument("--pg-module", default="test_execution_attempts_pg")
    parser.add_argument("--pg-min", type=int, default=0)
    args = parser.parse_args()

    root = ET.parse(args.report).getroot()

    total = skipped = failed = errored = 0
    pg_total = pg_skipped = 0
    for testcase in root.iter("testcase"):
        total += 1
        classname = testcase.get("classname", "")
        file_attr = testcase.get("file", "")
        is_pg = args.pg_module in classname or args.pg_module in file_attr
        is_skip = testcase.find("skipped") is not None
        if is_skip:
            skipped += 1
        if testcase.find("failure") is not None:
            failed += 1
        if testcase.find("error") is not None:
            errored += 1
        if is_pg:
            pg_total += 1
            if is_skip:
                pg_skipped += 1

    passed = total - skipped - failed - errored
    print(
        f"total={total} passed={passed} skipped={skipped} "
        f"failed={failed} errored={errored} pg_module={pg_total}"
    )

    problems: list[str] = []
    if skipped > 0:
        problems.append(f"{skipped} skipped test(s): silent skips are forbidden")
    if failed or errored:
        problems.append(f"{failed} failed, {errored} errored")
    if total < args.min_total:
        problems.append(f"only {total} tests ran, expected >= {args.min_total}")
    # The PostgreSQL module lives in a separate PR (#114). When it is present it
    # must be fully run; when absent (e.g. this CI PR on main) there is nothing
    # to enforce yet.
    if pg_total and pg_total < args.pg_min:
        problems.append(f"pg module ran {pg_total} tests, expected >= {args.pg_min}")
    if pg_total and pg_skipped:
        problems.append(f"pg module had {pg_skipped} skipped test(s)")

    if problems:
        print("GATE FAILED:")
        for problem in problems:
            print(f"  - {problem}")
        return 1
    print("GATE OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
