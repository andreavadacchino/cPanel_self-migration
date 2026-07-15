# Disposable cPanel characterization lab — provisioning runbook (R2-c4-LAB-PREFLIGHT)

Operator runbook to stand up an **isolated, disposable** cPanel environment for the opt-in live
`add_forwarder` characterization harness at commit `a95c922`
(`app/tests/live/forwarder_live_characterization.py`).

> This document contains **no** real hosts, endpoints, domains, or credentials — only placeholders.
> Do not paste real values here. The real secrets live only in a gitignored `.env.live`.

## Why a dedicated lab is required (preflight finding)

The preflight investigation found **no** qualified disposable environment:

- The only known cPanel access is the production `orbit-superadmin` fleet and real customer
  servers — both **out of scope** and forbidden by the task guardrails.
- The data model (`Endpoint`) has **no environment/classification field**, so production vs. lab
  cannot be distinguished at the data layer; isolation must be established out-of-band.
- No cPanel service exists in `docker-compose.yml` (cPanel is not containerizable), so there is no
  local sandbox.
- The repository provides the transport primitive (`CpanelClient.read/.write`) and the forwarder
  ops (`list_forwarders_op`, `add_forwarder_op`) but **no ready gateway** exposing the harness's
  `list_domains()` / `list_forwarders()` / `add_forwarder()` shape — a thin shim must be written.

Verdict: `CPANEL_DISPOSABLE_LAB_MISSING_RUNBOOK_READY`. Follow this runbook to create one.

## LAB_READY qualification (all 15 must hold before the live run)

1. hosts no real customers or data;
2. not reachable by any production Orbit runtime;
3. shares no email account with production;
4. has dedicated endpoint + credentials;
5. can be fully destroyed or restored (VPS snapshot);
6. explicit reset/destroy approval recorded (`CPANEL_TEST_ACCOUNT_RESET_APPROVED=1`);
7. outbound SMTP blocked/confined at firewall/provider;
8. a dedicated, real cPanel-configured domain exists on the account;
9. that domain carries no real mail (no real MX);
10. cPanel/WHM version known and compatible with production;
11. the gateway shim implements read + `add_forwarder` against this account;
12. its endpoint id can be allowlisted **without** removing it from the production denylist;
13. secrets injected via a secure mechanism, never committed;
14. no credential appears in any CLI arg, log, or report;
15. the harness runs from commit `a95c922` with a clean working tree.

If even one fails → `LAB_MISSING` / `LAB_NOT_QUALIFIED`; do not run the live test.

## Provisioning steps (operator; nothing here is automated in this session)

1. **VPS requirements** — a throwaway VPS with enough RAM/disk for cPanel; a provider that
   supports full snapshots and complete destruction; a network you can firewall.
2. **cPanel install** — install a cPanel/WHM version equal to or compatible with production under a
   regular trial/lab licence. *(Do not automate this here.)* Record the exact version for gate #10.
3. **Disposable account** — create ONE dedicated cPanel account; never reuse a customer username.
4. **Domain** — add ONE dedicated technical domain (or subdomain) to that account. It must be
   really configured on the account (the harness read-only `domain_owned` check requires it).
5. **Network + mail isolation** — set no real MX for the domain; block outbound SMTP (ports 25/465/
   587) at the firewall/provider so no mail can leave. Confirm the account cannot reach production.
6. **Minimal API credentials** — create a cPanel API token scoped to the minimum needed for
   `Email::list_forwarders`, `Email::add_forwarder`, and domain listing. No root, no WHM.
7. **Secure secret injection** — reuse the repo convention: keep all secrets in a **gitignored**
   `.env.live` (see `.env.live.example`); never place them on a CLI, in logs, or in the report.
   For DB-stored credentials the encrypted store (`CREDENTIAL_ENCRYPTION_KEY`, Fernet) may be used.
8. **The six required variables** — populate in `.env.live` (values only there):
   `RUN_LIVE_CPANEL_DESTRUCTIVE_TESTS`, `CPANEL_TEST_ACCOUNT_DISPOSABLE`,
   `CPANEL_TEST_ACCOUNT_RESET_APPROVED`, `CPANEL_TEST_ENDPOINT`,
   `CPANEL_TEST_ENDPOINT_ALLOWLIST`, `CPANEL_TEST_DISPOSABLE_DOMAIN`.
9. **Denylist/allowlist** — put the lab endpoint id into `CPANEL_TEST_ENDPOINT_ALLOWLIST`; verify it
   is NOT present in `CPANEL_TEST_PRODUCTION_ENDPOINTS` (adding the lab must not weaken the
   production denylist).
10. **Gateway shim** — implement an object exposing `list_domains()`, `list_forwarders()`,
    `add_forwarder(source, destination)` on top of `CpanelClient` + the forwarder ops, and wire it
    into `test_live_add_forwarder_characterization` (currently an intentional placeholder).
11. **Read-only preflight** — run only the read paths: connection/version check, `list_domains`,
    prove the disposable domain is owned, `list_forwarders`, prove no real data, confirm SMTP is
    confined, confirm reset/destroy is concretely possible. Issue **zero** writes.
12. **Snapshot** — take a full VPS snapshot BEFORE any write, so the account can be restored.
13. **Operator authorization** — record explicit approval to run the destructive test and to reset/
    destroy the account afterward.
14. **Harness execution** — from a clean tree at commit `a95c922`, with all six vars set, run the
    single live test. The gate re-verifies clean-tree/committed-HEAD/ownership/empty-baseline
    before any write.
15. **Sanitized report** — collect the report (identity token, normalized counts, response class,
    flags only) to a gitignored path (e.g. `/tmp`); never commit it.
16. **Reset / destroy + revoke** — restore from snapshot or destroy the VPS entirely (there is **no**
    in-scope delete primitive for the created forwarders), then revoke the API token.

## cPanel version & adapter notes

- Forwarder read = `UAPI Email::list_forwarders`; write = `UAPI Email::add_forwarder`
  (`idempotent=False`); routing uses `API2 Email::setmxcheck`. Any modern cPanel supporting UAPI/
  API2 is compatible; still record the exact WHM version for gate #10 and match production.
- Transport = `CpanelClient` over HTTPS to the cPanel API port (default 2083), token auth, TLS
  verification on by default.

## Hard guardrails (unchanged)

No production, no customer accounts/domains, no DNS changes, no SMTP opening, no credential
creation/rotation from this session, no allowlist code changes, no push/deploy, and **the live
harness is not run until every gate above is satisfied**. A positive result still does NOT promote
the capability: `email_forwarders` stays `manual_only` until a separate decision.
