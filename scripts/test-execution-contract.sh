#!/usr/bin/env bash
# Cross-language gate for the execution contract (see docs/ADR_V2_GO_EXECUTOR.md).
#
# "go test green + pytest green" proves nothing on its own if each language
# exercises its own cases. Both halves here read the SAME corpus,
# testdata/execution-contract/manifest.json, and must agree on every verdict and
# on the error substring behind every rejection.
#
# No Docker, no network, no cPanel: this is pure contract and serialization.
#
# Usage:
#   scripts/test-execution-contract.sh
#   PYTHON=/path/to/venv/bin/python scripts/test-execution-contract.sh

set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
PYTHON="${PYTHON:-python3}"

fail() { printf '\n\033[31mFAIL\033[0m %s\n' "$1" >&2; exit 1; }
step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

step "Fixture corpus"
test -f testdata/execution-contract/manifest.json \
  || fail "missing testdata/execution-contract/manifest.json"
"$PYTHON" - <<'PY' || fail "manifest is not consistent with the fixtures on disk"
import json, pathlib, sys
root = pathlib.Path("testdata/execution-contract")
manifest = json.loads((root / "manifest.json").read_text())
declared = {f["path"] for f in manifest["fixtures"]}
on_disk = {f"{d}/{p.name}" for d in ("valid", "invalid") for p in (root / d).iterdir() if p.is_file()}
if declared != on_disk:
    print("declared but missing:", sorted(declared - on_disk))
    print("on disk but undeclared:", sorted(on_disk - declared))
    sys.exit(1)
valid = sum(1 for f in manifest["fixtures"] if f["expected_valid"])
print(f"{len(declared)} fixtures: {valid} valid, {len(declared) - valid} invalid")
PY

step "Schemas parse and carry no raw control characters"
for f in schemas/*.json; do
  "$PYTHON" - "$f" <<'PY' || fail "invalid schema: $f"
import json, pathlib, sys
raw = pathlib.Path(sys.argv[1]).read_bytes()
bad = [b for b in raw if b < 0x20 and b not in (0x09, 0x0a, 0x0d)]
if bad:
    sys.exit(f"raw control characters in {sys.argv[1]}")
json.loads(raw)
PY
  printf '  ok %s\n' "$f"
done

step "Go: gofmt"
unformatted="$(gofmt -l internal/events internal/executioncontract)"
[ -z "$unformatted" ] || fail "gofmt needed: $unformatted"

step "Go: vet"
go vet ./internal/events/... ./internal/executioncontract/... || fail "go vet"

step "Go: contract + events tests (incl. real writer output)"
go test ./internal/events/... ./internal/executioncontract/... || fail "go test"

step "Python: domain contract tests"
"$PYTHON" -c 'import pydantic, pytest' 2>/dev/null || fail \
  "the Python environment needs pydantic and pytest. Install the domain package:
     python3 -m venv .venv && .venv/bin/pip install -e migration-platform/packages/domain[test]
     PYTHON=.venv/bin/python scripts/test-execution-contract.sh"
( cd migration-platform/packages/domain && "$PYTHON" -m pytest ) || fail "pytest"

step "Cross-language agreement"
# Both halves ran the same manifest above. Assert the counts match, so a corpus
# silently skipped on one side cannot pass as agreement.
go_count="$(cd "$ROOT" && go test ./internal/executioncontract/ -run TestManifestFixtures -v 2>/dev/null | grep -c '^    --- PASS' || true)"
py_count="$(cd "$ROOT/migration-platform/packages/domain" && "$PYTHON" -m pytest --collect-only 2>/dev/null | grep -c 'test_manifest_fixture\[' || true)"
printf '  Go ran %s manifest fixtures, Python ran %s\n' "$go_count" "$py_count"
[ "$go_count" -gt 0 ] || fail "Go ran no manifest fixtures"
[ "$go_count" = "$py_count" ] || fail "Go and Python did not run the same number of fixtures"

printf '\n\033[32mPASS\033[0m execution contract: Go and Python agree on all %s fixtures\n' "$go_count"
