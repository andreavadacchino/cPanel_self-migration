# Debugging guide

Diagnostics that go beyond `--log-level debug`. These are opt-in, off by default,
and aimed at investigating how a **specific cPanel build** behaves rather than at
everyday use.

> ⚠️ The **golden rule still holds**: the SOURCE host is read-only in every mode.
> None of the switches below change what the tool writes — they only change what
> it *logs*.

---

## Table of contents

1. [Verbose diagnostics: `--log-level debug`](#1-verbose-diagnostics---log-level-debug)
2. [Raw UAPI response debug (`CPSM_DEBUG_RAW_UAPI`)](#2-raw-uapi-response-debug-cpsm_debug_raw_uapi)
3. [Case study: addon-domain creation fails with "did not return an expiry"](#3-case-study-addon-domain-creation-fails-with-did-not-return-an-expiry)

---

## 1. Verbose diagnostics: `--log-level debug`

```sh
./cpanel-self-migration --apply --log-level debug 2> debug.txt
```

Prints verbose traces to **stderr** (so they never corrupt the stdout log
artifacts). See [USAGE.md → `--log-level debug`](USAGE.md#--log-level-debug) for
what the traces include.

One deliberate gap: on the **success** path, UAPI calls log only the response
*length*, never the body. That is a security choice — some success bodies carry a
secret (most importantly `Tokens::create_full_access`, whose `data.token` is a
live API token). The next switch lifts that gap safely when you need it.

---

## 2. Raw UAPI response debug (`CPSM_DEBUG_RAW_UAPI`)

When you must see the **shape** of a UAPI response (which keys exist, of which
type) — for example to find out whether a given cPanel build echoes an
`expires_at` field — enable the raw-response debug:

```sh
CPSM_DEBUG_RAW_UAPI=1 ./cpanel-self-migration --apply --log-level debug 2> debug.txt
```

With it on, `RunUAPI` logs the full response body **with every secret value
redacted** in addition to the usual length line:

```text
  [debug +15.2s] Tokens::create_full_access: UAPI call succeeded (242 bytes response)
  [debug +15.2s] Tokens::create_full_access: raw response (secrets redacted): {"result":{"data":{"create_time":1699999100,"name":"cpsm_ab12","token":"<redacted>"},"status":1}}
```

Guarantees:

- **Off by default.** It is enabled only when `CPSM_DEBUG_RAW_UAPI` is set to a
  truthy value (`1`, `true`, `yes`, `on`). A normal run never logs a body.
- **Secrets are redacted.** The value of any sensitive key (`token`,
  `api_token`, `secret`, `password`, `pass`, `passwd`, `private_key`, …) at any
  depth is replaced with `<redacted>`. The **structure is preserved**, which is
  the whole point — you can see *that* `expires_at` is present or absent without
  ever seeing the token.
- **Fail-safe.** If a body is not valid JSON (or cannot be re-serialized after
  redaction), the tool logs a short value-free placeholder, never the raw bytes.
- **Still debug-gated.** The redacted body only appears at `--log-level debug`.

Empty or null secret values (`"token":""`, `"secret":null`) are left as-is: they
carry nothing to hide and the empty/absent state is itself useful to see.

### Enabling it from tests

The same toggle backs the package-level `rawResponseDebug` variable in
`internal/cpanel/debug.go`. In-package tests may set it directly (restore it with
a `defer`) to assert on the redacted output:

```go
defer func(prev bool) { rawResponseDebug = prev }(rawResponseDebug)
rawResponseDebug = true
```

See `internal/cpanel/debug_test.go` for the redaction unit tests and an
end-to-end test proving a live token never reaches the log sink.

---

## 3. Case study: addon-domain creation fails with "did not return an expiry"

### Symptom

`--apply` ends quickly with every addon domain failed and the dependent
mail/files/databases skipped:

```text
-> addon example.com: create returned an error; will verify existence before deciding:
   create API token: Tokens::create_full_access did not return an expiry for the temporary token
! domain creation step done with 6 FAILED and 0 BLOCKED domain(s)
```

### Why

To create an addon domain the tool mints a **temporary full-access API token**
(the only token type a cPanel *user* — no WHM — can create), uses it for the
legacy `AddonDomain` api2 call, then revokes it **immediately after use**
(`internal/migrate/apply_domains.go`). As a safety measure
(`internal/cpanel/token.go`) it also **requests a short `expires_at`** so a crash
between create and revoke cannot strand a permanent full-access token. The expiry
is a third layer of defense, on top of the immediate revoke and the
`cpsm_`-prefix leftover cleanup on the next run.

Some cPanel builds **do not honor / do not echo `expires_at`** on a user-level
`create_full_access`: the create succeeds and returns a valid token, but with no
expiry (`expires_at == 0`). The log above is what an **older, strict** build did
in that case — it failed closed and could not create addon domains at all.

**Current behavior:** the tool still *requests* the expiry, but when the host
ignores it (`expires_at == 0`, `errTokenExpiryIgnored`) it **proceeds** with the
otherwise-valid token rather than blocking the migration. The library only *traces*
the condition at debug (`internal/cpanel/token.go`):

```text
  [debug +15.2s] Tokens::create_full_access: host did not apply a token expiry (ExpiresAt=0); proceeding, caller warns + revokes
```

The operator-facing warning is the caller's (`internal/migrate/apply_domains.go`): a
short, **self-erasing** caveat that the revoke outcome overwrites in place, so a clean
run leaves no stale warning. On a TTY the caveat —

```text
     ! token has no expiry; remove "cpsm_"* by hand if interrupted
```

— is replaced, once the token is revoked, by `     -> temporary API token revoked`. If
the run is interrupted **before** the revoke the caveat stays on screen (clean up the
`cpsm_`* token by hand); a failed revoke replaces it with a `revoke FAILED … remove
manually` warning. On such a host the token's lifetime is bounded by the immediate
revoke-after-use (and leftover cleanup), not by an expiry. A **returned-but-invalid**
expiry (in the past, materially shorter than requested, or excessive —
`errTokenExpiryInvalid`) is still treated as a hard failure and the token is revoked.

### Confirming the host actually ignores the expiry

Use the raw-response debug to inspect the body:

```sh
CPSM_DEBUG_RAW_UAPI=1 ./cpanel-self-migration --apply --file --log-level debug 2> debug.txt
grep -A1 'create_full_access' debug.txt
```

- **`expires_at` is `0`/absent AND no other expiry-shaped field is present** →
  the host genuinely does not apply a token expiry for user tokens. This is the
  warn-and-proceed case; the immediate revoke after use bounds the exposure.
- **`expires_at` is `0`/absent BUT another field carries the expiry** (e.g.
  `"expires"`, `"expiry"`, `"expire_time"`, …) → the host *does* apply an expiry,
  under a name `CreateTokenData` does not decode. The tool fails closed with
  `errTokenExpiryUnrecognized` ("the tool needs updating") rather than masking it
  as "ignored". Fix: add that field's spelling to `CreateTokenData` in
  `internal/cpanel/types.go` (and, if it is a brand-new alternate, to
  `hasUnboundExpiry`). The tool only auto-detects `expires`/`expiry` as
  alternates today; a different spelling reaches this only if you spot it here.
- **`expires_at` is present and non-zero** → it is validated normally
  (`validateTokenExpiry`); a past / materially-shorter / excessive value fails
  closed as `errTokenExpiryInvalid`.

This is the cheapest way to tell a host-incompatibility apart from a tool bug.
