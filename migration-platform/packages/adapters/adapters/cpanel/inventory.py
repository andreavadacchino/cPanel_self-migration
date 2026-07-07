"""cPanel read-only capability scanner + inventory collector.

Runs a fixed set of **verified** UAPI read-only functions, normalizes the
results into counts + minimal data (never raw, never secrets) and reports which
capabilities the host actually supports (probe-driven, not hardcoded).

Verified UAPI functions (api.docs.cpanel.net):
  account   → StatsBar::get_stats
  domains   → DomainInfo::list_domains
  email     → Email::list_pops
  databases → Mysql::list_databases
  cron      → Cron::list_cron
  ssl       → SSL::installed_hosts
DNS read is intentionally not attempted (no verified account-level read-only
function) → ``can_read_dns`` stays false with a limitation.
"""

from __future__ import annotations

from adapters.cpanel.errors import (
    CpanelAuthError,
    CpanelConnectionError,
    CpanelError,
    CpanelParseError,
    CpanelTimeoutError,
)
from adapters.inventory import (
    DNS_LIMITATION,
    CapabilityReport,
    InventoryError,
    InventoryResult,
    ProbeOutcome,
    build_summary,
)

# Reaching the host but failing at TCP/TLS/timeout is fatal to a snapshot.
_FATAL = (CpanelConnectionError, CpanelTimeoutError)


def _norm_domains(data: object) -> tuple[list, int]:
    if not isinstance(data, dict):
        return [], 0
    items: list[dict] = []
    main = data.get("main_domain")
    if main:
        items.append({"domain": str(main), "type": "main"})
    for key, typ in (
        ("addon_domains", "addon"),
        ("parked_domains", "parked"),
        ("sub_domains", "sub"),
    ):
        for d in data.get(key) or []:
            items.append({"domain": str(d), "type": typ})
    return items, len(items)


def _norm_email(data: object) -> tuple[list, int]:
    rows = data if isinstance(data, list) else []
    out: list[dict] = []
    for r in rows:
        if isinstance(r, dict):
            email = r.get("email")
            if not email and r.get("user") and r.get("domain"):
                email = f"{r.get('user')}@{r.get('domain')}"
            out.append({"email": email, "domain": r.get("domain")})
        else:
            out.append({"email": str(r), "domain": None})
    return out, len(out)


def _norm_databases(data: object) -> tuple[list, int]:
    rows = data if isinstance(data, list) else []
    out: list[dict] = []
    for r in rows:
        name = (r.get("database") or r.get("name")) if isinstance(r, dict) else str(r)
        out.append({"name": name})
    return out, len(out)


# A cron *command* commonly embeds secrets (`mysqldump -pSECRET`, tokens in
# URLs, `curl -H "Authorization: …"`). We deliberately never persist it — only
# the (non-sensitive) schedule and the count.
_CRON_SCHEDULE_KEYS = ("minute", "hour", "day", "month", "weekday")


def _norm_cron(data: object) -> tuple[list, int]:
    rows = data if isinstance(data, list) else []
    out = []
    for r in rows:
        if isinstance(r, dict):
            out.append(
                {k: r.get(k) for k in _CRON_SCHEDULE_KEYS if r.get(k) is not None}
            )
        else:
            out.append({})
    return out, len(out)


def _norm_ssl(data: object) -> tuple[list, int]:
    rows = data if isinstance(data, list) else []
    out: list[dict] = []
    for r in rows:
        if isinstance(r, dict):
            out.append({"host": r.get("host") or r.get("servername")})
        else:
            out.append({"host": str(r)})
    return out, len(out)


def _norm_account(data: object) -> tuple[dict, None]:
    # Do not persist raw stats (paths/values); presence is enough for Sprint 2.
    return {"available": True}, None


class CpanelInventorySource:
    def __init__(self, client, *, host: str) -> None:
        self._client = client
        self._host = host

    def close(self) -> None:
        self._client.close()

    def probe(self) -> ProbeOutcome:
        """Minimal connect+auth check via a single cheap read-only call."""
        try:
            self._client.call_uapi("DomainInfo", "list_domains")
        except CpanelAuthError as exc:
            return ProbeOutcome(
                connected=True,
                authenticated=False,
                capabilities=CapabilityReport(
                    source="cpanel", can_connect=True, can_authenticate=False
                ),
                error=str(exc),
            )
        except _FATAL as exc:
            return ProbeOutcome(
                connected=False,
                authenticated=False,
                capabilities=CapabilityReport(source="cpanel"),
                error=str(exc),
            )
        except CpanelParseError as exc:
            # Reached a host (HTTP 2xx) but the body is not a UAPI envelope — it
            # is not cPanel / auth is unconfirmed. Do NOT report authenticated.
            return ProbeOutcome(
                connected=True,
                authenticated=False,
                capabilities=CapabilityReport(
                    source="cpanel", can_connect=True, can_authenticate=False
                ),
                error=str(exc),
            )
        except CpanelError as exc:
            # A structured UAPI error (status=0 / unsupported) means we reached
            # cPanel and got past auth.
            return ProbeOutcome(
                connected=True,
                authenticated=True,
                capabilities=CapabilityReport(
                    source="cpanel", can_connect=True, can_authenticate=True
                ),
                error=str(exc),
            )
        return ProbeOutcome(
            connected=True,
            authenticated=True,
            capabilities=CapabilityReport(
                source="cpanel", can_connect=True, can_authenticate=True
            ),
        )

    def _read(self, cap_attr, module, function, normalizer, caps, failures):
        try:
            resp = self._client.call_uapi(module, function)
        except _FATAL as exc:
            raise InventoryError(f"Cannot reach {self._host}: {exc}") from exc
        except CpanelAuthError as exc:
            raise InventoryError(
                f"Authentication failed for {self._host}: {exc}"
            ) from exc
        except CpanelError:
            # Reached + authenticated, but this specific read is unavailable.
            caps.can_connect = True
            caps.can_authenticate = True
            failures.append(f"{cap_attr}_read_unavailable")
            return None, None
        caps.can_connect = True
        caps.can_authenticate = True
        setattr(caps, f"can_read_{cap_attr}", True)
        return normalizer(resp.data)

    def collect(self) -> InventoryResult:
        caps = CapabilityReport(source="cpanel")
        failures: list[str] = []

        # Domains doubles as the connect/auth gate (fatal on connection/auth).
        domains, domains_count = self._read(
            "domains", "DomainInfo", "list_domains", _norm_domains, caps, failures
        )
        account, _ = self._read(
            "account_info", "StatsBar", "get_stats", _norm_account, caps, failures
        )
        email, email_count = self._read(
            "email", "Email", "list_pops", _norm_email, caps, failures
        )
        databases, databases_count = self._read(
            "databases", "Mysql", "list_databases", _norm_databases, caps, failures
        )
        cron, cron_count = self._read(
            "cron", "Cron", "list_cron", _norm_cron, caps, failures
        )
        ssl, ssl_count = self._read(
            "ssl", "SSL", "installed_hosts", _norm_ssl, caps, failures
        )

        # DNS: no verified read-only account-level function → not attempted.
        caps.can_read_dns = False
        caps.limitations = [DNS_LIMITATION, *failures]
        warnings = [DNS_LIMITATION, *failures]

        data = {
            "account": account,
            "domains": domains,
            "email_accounts": email,
            "databases": databases,
            "cron_jobs": cron,
            "ssl": ssl,
            "dns": None,
            "warnings": warnings,
        }
        summary = build_summary(
            domains_count=domains_count,
            email_accounts_count=email_count,
            databases_count=databases_count,
            cron_jobs_count=cron_count,
            dns_records_count=None,
            ssl_items_count=ssl_count,
            warnings_count=len(warnings),
        )
        return InventoryResult(capabilities=caps, summary=summary, data=data)
