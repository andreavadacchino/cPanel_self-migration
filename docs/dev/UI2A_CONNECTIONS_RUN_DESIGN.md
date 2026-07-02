# UI phase 2a — connections + run-from-browser (micro-design)

Requirement (operator): configure the source and destination servers
(IP + credentials) and launch the analysis FROM the browser — the
terminal should not be needed for the daily read-only loop.

## Trust model update

Phase 1's "the UI never opens SSH" narrows to: **the UI process never
opens SSH itself and never mutates servers**. It becomes a LAUNCHER:

- `POST /config` writes the LOCAL `host.yaml` (same file, same plaintext
  credential model, same 0600 the CLI uses today — nothing new leaves
  the machine);
- `POST /run` spawns the tool's OWN binary as a subprocess running the
  READ-ONLY pipeline into `--dir`:
  1. `--account-inventory --config <dir>/host.yaml --output-dir <dir>`
  2. `inventory diff …`
  3. `inventory policy …`
  4. `inventory checklist …` (+ `--acceptances` when present, +
     `--migration-report` when an APPLY report.json is present)
- the CLI remains the single authority for every step; `--apply` stays
  terminal-only.

## New HTTP surface → mandatory protections (built first, not later)

The moment the UI accepts POST, a malicious page in the operator's
browser could target localhost. Defenses, all enforced in a middleware
that wraps every route:

1. **Host header allowlist**: the request's Host must be a loopback
   literal or localhost — blocks DNS rebinding (`evil.com → 127.0.0.1`).
2. **Origin check**: when an Origin header is present it must match the
   local origin — blocks cross-origin form posts.
3. **CSRF token**: random 32-byte value generated per server start,
   embedded as a hidden field in every form; POSTs without it are 403.
   Constant-time comparison. The run context descends from a base context
   the ui cancels on interrupt (kills the SSH subprocess, no orphan); the
   job goroutine recovers panics into a failed run instead of crashing.
4. Existing gates stay: loopback bind, GET/HEAD/POST only, single-page
   routing, no raw file serving.

## Config form

- Fields: src/dest × (ip, port, ssh_user, ssh_pass); defaults port 22,
  timeout 15s (not exposed in v1).
- Empty password on edit keeps the stored one (no need to re-type; the
  page never echoes passwords back). **Security invariant**: because a
  blank password inherits the stored one while ip/user are editable in
  the same form, the CSRF + Origin + anti-framing gates are load-bearing
  for credential CONFIDENTIALITY (not just "no unwanted run"): bypassing
  them could redirect src to an attacker host and replay the stored
  password to it. Changes to the security middleware need extra scrutiny.
- Validation is delegated to the AUTHORITY: the handler marshals the
  Config, writes it to a temp file, runs `config.Load` on it, and only
  on success renames it over `<dir>/host.yaml` (0600, atomic). The UI
  can never write a config the CLI would reject.
- Destination required for the analysis job (diff/checklist need both
  sides); config.Load's source-only mode stays a CLI affair.

## Job manager

- ONE run at a time (mutex); a second `POST /run` while running → 409.
- Job state: idle | running(step) | done | failed(step, error), started
  timestamp, bounded per-step output tail (last 4 KiB) for display.
- Steps run via an injectable runner (`func(ctx, name, argv) error` +
  output writer): production uses os/exec on `os.Executable()`; tests
  inject a scripted runner. 30-minute context timeout backstop.
- Progress display with ZERO JS: while running, the dashboard embeds
  `<meta http-equiv="refresh" content="2">` — every refresh re-reads
  job state AND the artifacts, so results appear as steps land.

## Testing (TDD)

- Security: Host-header rejection table, Origin rejection, CSRF missing/
  wrong/valid, methods.
- Config: POST writes a host.yaml that `config.Load` accepts with the
  posted values; invalid input → 4xx and NO file change; empty password
  inherits; passwords never appear in any response body.
- Run: fake runner records argv sequence (4 steps, right flags), state
  transitions idle→running→done, failure marks failed with the step
  name, concurrent POST /run → 409, artifacts dir wired as --output-dir.
- Live smoke with the real binary against an empty config → step 1 fails
  fast (no SSH reachable), job shows failed — proving the wiring without
  servers.
