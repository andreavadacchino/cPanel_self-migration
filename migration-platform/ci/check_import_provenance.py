#!/usr/bin/env python3
"""Fail-closed import-provenance check for CI.

An editable install pointing at the wrong worktree, or a stray global package,
produces a false green: the tests run against code that is not this checkout.
This asserts that every platform package resolves under ``$GITHUB_WORKSPACE``.
"""

from __future__ import annotations

import importlib
import os
import sys

MODULES = ("app", "domain", "adapters", "worker")


def main() -> int:
    workspace = os.environ.get("GITHUB_WORKSPACE")
    if not workspace:
        print("GITHUB_WORKSPACE is not set")
        return 1
    workspace = os.path.realpath(workspace)

    bad: list[str] = []
    for name in MODULES:
        module = importlib.import_module(name)
        path = os.path.realpath(module.__file__ or "")
        ok = path.startswith(workspace + os.sep)
        print(f"{'OK ' if ok else 'BAD'} {name}: {path}")
        if not ok:
            bad.append(name)

    if bad:
        print(f"imports not resolved from the checkout: {bad}")
        return 1
    print("provenance OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
