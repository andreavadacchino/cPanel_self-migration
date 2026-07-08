"""cPanel read-only capability scanner + inventory collector.

Runs a fixed set of **verified** read-only functions, normalizes the results
into counts + minimal data (never raw, never secrets) and reports, per category,
a coverage status (succeeded / empty / partial / unsupported / unavailable /
failed / unverified). Capabilities are derived from that coverage — probe-driven,
never hardcoded to success.

Verified functions (api.docs.cpanel.net, cPanel & WHM v136):
  account         → StatsBar::get_stats            (UAPI)
  domains         → DomainInfo::list_domains       (UAPI, connect/auth gate)
  email_accounts  → Email::list_pops               (UAPI)
  databases       → Mysql::list_databases          (UAPI)
  ssl             → SSL::installed_hosts           (UAPI)
  cron_jobs       → Cron::listcron                 (cPanel API 2 — no UAPI Cron)
  dns_records     → DNS::parse_zone                (UAPI, one call per zone)
  email_forwarders→ Email::list_forwarders         (UAPI)
  email_autorespon→ Email::list_auto_responders    (UAPI)
  ftp_accounts    → Ftp::list_ftp                  (UAPI, never the password)

Categories with no verified read-only function are reported as ``unverified``
(see adapters.inventory.UNVERIFIED_CATEGORIES) — the collector never calls them.
"""

from __future__ import annotations

import base64
import binascii

from adapters.cpanel.errors import (
    CpanelApiError,
    CpanelAuthError,
    CpanelConnectionError,
    CpanelError,
    CpanelParseError,
    CpanelTimeoutError,
    CpanelUnsupportedFunctionError,
)
from adapters.inventory import (
    COVERAGE_EMPTY,
    COVERAGE_FAILED,
    COVERAGE_PARTIAL,
    COVERAGE_SUCCEEDED,
    COVERAGE_UNAVAILABLE,
    COVERAGE_UNSUPPORTED,
    READABLE_COVERAGE_STATUSES,
    CapabilityReport,
    CoverageEntry,
    InventoryError,
    InventoryResult,
    ProbeOutcome,
    _limitations_from_coverage,
    build_summary,
    unverified_coverage,
)

# Reaching the host but failing at TCP/TLS/timeout is fatal to a snapshot.
_FATAL = (CpanelConnectionError, CpanelTimeoutError)

# Fixed, category-safe coverage messages. Deliberately NOT derived from the
# exception text: a parse/api error can embed a raw response-body snippet, and
# for Cron::listcron that body is the cron listing whose command may hold
# secrets — storing it would leak into the snapshot/API/UI.
_COVERAGE_ERROR_MESSAGE = {
    COVERAGE_UNSUPPORTED: "The function is not available on this host.",
    COVERAGE_UNAVAILABLE: (
        "The read call failed (function unavailable or module disabled)."
    ),
    COVERAGE_FAILED: "The read returned an unexpected response shape.",
}


# --- normalizers ------------------------------------------------------------


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
# the (non-sensitive) schedule, the count, and a boolean presence flag.
_CRON_SCHEDULE_KEYS = ("minute", "hour", "day", "month", "weekday")


def _norm_cron(data: object) -> tuple[list, int]:
    """Normalize cPanel API 2 ``Cron::listcron`` data (a list of job dicts).

    The command is never stored — only the schedule and ``command_present``.
    The trailing count-only artifact (``{"count": N}``) is dropped.
    """
    rows = data if isinstance(data, list) else []
    out: list[dict] = []
    for r in rows:
        if not isinstance(r, dict):
            continue
        schedule = {
            k: r.get(k) for k in _CRON_SCHEDULE_KEYS if r.get(k) is not None
        }
        has_command = bool(r.get("command"))
        if not schedule and not has_command:
            continue  # trailing "{count: N}" summary item, not a real job
        item = dict(schedule)
        item["command_present"] = has_command
        out.append(item)
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


def _norm_forwarders(data: object) -> tuple[list, int]:
    rows = data if isinstance(data, list) else []
    out: list[dict] = []
    for r in rows:
        if isinstance(r, dict):
            out.append(
                {
                    "source": r.get("dest") or r.get("source"),
                    "destination": r.get("forward") or r.get("destination"),
                }
            )
        else:
            out.append({"source": str(r), "destination": None})
    return out, len(out)


def _norm_autoresponders(data: object) -> tuple[list, int]:
    rows = data if isinstance(data, list) else []
    out: list[dict] = []
    for r in rows:
        email = r.get("email") if isinstance(r, dict) else str(r)
        out.append({"email": email})
    return out, len(out)


def _norm_ftp(data: object) -> tuple[list, int]:
    # Only user + type — never the (hashed or plaintext) password.
    rows = data if isinstance(data, list) else []
    out: list[dict] = []
    for r in rows:
        if isinstance(r, dict):
            out.append({"user": r.get("user") or r.get("login"), "type": r.get("type")})
        else:
            out.append({"user": str(r), "type": None})
    return out, len(out)


def _b64decode(value: object) -> str:
    if not isinstance(value, str) or not value:
        return ""
    try:
        return base64.b64decode(value).decode("utf-8", "replace").strip()
    except (ValueError, binascii.Error):
        return ""


def _norm_dns_records(data: object, zone: str) -> tuple[list, int]:
    """Normalize one zone's ``DNS::parse_zone`` output (base64-encoded records).

    Keeps only actual records (``type == "record"``) as
    ``{domain, name, type, value, ttl}`` with fully decoded values. TXT RDATA is
    split into &lt;=255-byte character-strings on the wire purely for the length
    limit, so its segments are re-joined with NO separator; multi-field types
    (MX/SRV) keep a space between their positional fields. The value is never
    truncated — a cap would corrupt the comparison identity/fingerprint and make
    two distinct DKIM/TXT records collapse into a false "match".
    """
    rows = data if isinstance(data, list) else []
    out: list[dict] = []
    for r in rows:
        if not isinstance(r, dict) or r.get("type") != "record":
            continue
        rtype = r.get("record_type")
        segments = [_b64decode(x) for x in (r.get("data_b64") or [])]
        sep = "" if str(rtype).upper() == "TXT" else " "
        value = sep.join(s for s in segments if s)
        out.append(
            {
                "domain": zone,
                "name": _b64decode(r.get("dname_b64")),
                "type": rtype,
                "value": value,
                "ttl": r.get("ttl"),
            }
        )
    return out, len(out)


def _norm_account(data: object) -> tuple[dict, None]:
    # Do not persist raw stats (paths/values); presence is enough.
    return {"available": True}, None


# --- collector --------------------------------------------------------------


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

    def _fatal(self, exc: Exception) -> InventoryError:
        return InventoryError(f"Cannot reach {self._host}: {exc}")

    def _coverage_for_error(self, method: str, exc: Exception) -> CoverageEntry:
        """Map a non-fatal read failure to a coverage status.

        The persisted ``message`` is a FIXED, category-safe string — never the
        exception text. A parse/api error can embed a snippet of the raw
        response body (see client._describe), and for ``Cron::listcron`` that
        body IS the cron listing, whose ``command`` field may contain secrets.
        Storing ``str(exc)`` would leak it into the snapshot / API / UI, defeating
        the "cron command is never persisted" invariant. The rich exception text
        remains available to the caller for transient handling, just not stored.
        """
        if isinstance(exc, CpanelUnsupportedFunctionError):
            status = COVERAGE_UNSUPPORTED
        elif isinstance(exc, CpanelApiError):
            status = COVERAGE_UNAVAILABLE
        else:  # CpanelParseError / unexpected shape
            status = COVERAGE_FAILED
        return CoverageEntry(
            status=status,
            method=method,
            read_only_verified=True,
            message=_COVERAGE_ERROR_MESSAGE[status],
        )

    def _read(
        self, module, function, normalizer, *, method, params=None
    ) -> tuple[object, int | None, CoverageEntry]:
        """Read one UAPI category. Returns (data, count, coverage).

        Fatal errors (connection/timeout/auth) abort the whole snapshot; every
        other failure becomes a coverage status so the read continues.
        """
        try:
            resp = self._client.call_uapi(module, function, params)
        except _FATAL as exc:
            raise self._fatal(exc) from exc
        except CpanelAuthError as exc:
            raise InventoryError(
                f"Authentication failed for {self._host}: {exc}"
            ) from exc
        except CpanelError as exc:
            return None, None, self._coverage_for_error(method, exc)
        data, count = normalizer(resp.data)
        if count is None:
            status = COVERAGE_SUCCEEDED  # non-list read (e.g. account) that worked
        else:
            status = COVERAGE_SUCCEEDED if count > 0 else COVERAGE_EMPTY
        return (
            data,
            count,
            CoverageEntry(
                status=status, method=method, read_only_verified=True, items_count=count
            ),
        )

    def _read_cron(self) -> tuple[object, int | None, CoverageEntry]:
        """Cron via cPanel API 2 (UAPI has no Cron module). Read-only, no params."""
        method = "Cron::listcron"
        try:
            data = self._client.call_cpapi2("Cron", "listcron")
        except _FATAL as exc:
            raise self._fatal(exc) from exc
        except CpanelAuthError as exc:
            raise InventoryError(
                f"Authentication failed for {self._host}: {exc}"
            ) from exc
        except CpanelError as exc:
            return None, None, self._coverage_for_error(method, exc)
        jobs, count = _norm_cron(data)
        status = COVERAGE_SUCCEEDED if count > 0 else COVERAGE_EMPTY
        return (
            jobs,
            count,
            CoverageEntry(
                status=status, method=method, read_only_verified=True, items_count=count
            ),
        )

    def _read_dns(self, domains: object) -> tuple[object, int | None, CoverageEntry]:
        """DNS via UAPI ``DNS::parse_zone``, one call per zone (read-only).

        Subdomains share the parent zone, so only main/addon/parked are parsed.
        """
        method = "DNS::parse_zone"
        if not isinstance(domains, list):
            # Domains soft-failed (non-fatal): we don't know which zones exist,
            # so DNS is a read gap, not "successfully verified as empty".
            return (
                None,
                None,
                CoverageEntry(
                    status=COVERAGE_UNAVAILABLE, method=method,
                    read_only_verified=True,
                    message="Domains could not be read; DNS was not attempted.",
                ),
            )
        zones = [
            str(d["domain"])
            for d in domains
            if isinstance(d, dict)
            and d.get("type") in ("main", "addon", "parked")
            and d.get("domain")
        ]
        if not zones:
            return (
                [],
                0,
                CoverageEntry(
                    status=COVERAGE_EMPTY, method=method, read_only_verified=True,
                    items_count=0, message="No zones to parse.",
                ),
            )

        records: list[dict] = []
        ok = 0
        failed = 0
        for zone in zones:
            try:
                resp = self._client.call_uapi("DNS", "parse_zone", {"zone": zone})
            except _FATAL as exc:
                raise self._fatal(exc) from exc
            except CpanelAuthError as exc:
                raise InventoryError(
                    f"Authentication failed for {self._host}: {exc}"
                ) from exc
            except CpanelUnsupportedFunctionError:
                if ok == 0:
                    # The function is absent entirely — no point trying more zones.
                    return (
                        None,
                        None,
                        CoverageEntry(
                            status=COVERAGE_UNSUPPORTED, method=method,
                            read_only_verified=True,
                            message="DNS::parse_zone is not available on this host.",
                        ),
                    )
                # It worked for earlier zones, so this is a per-zone gap.
                failed += 1
                continue
            except CpanelError:
                failed += 1
                continue
            recs, _ = _norm_dns_records(resp.data, zone)
            records.extend(recs)
            ok += 1

        if ok == 0:
            return (
                None,
                None,
                CoverageEntry(
                    status=COVERAGE_UNAVAILABLE, method=method,
                    read_only_verified=True,
                    message=f"parse_zone failed for all {failed} zone(s).",
                ),
            )
        if failed > 0:
            status, message = COVERAGE_PARTIAL, (
                f"{failed} of {ok + failed} zone(s) could not be parsed."
            )
        else:
            status = COVERAGE_SUCCEEDED if records else COVERAGE_EMPTY
            message = None
        return (
            records,
            len(records),
            CoverageEntry(
                status=status, method=method, read_only_verified=True,
                items_count=len(records), message=message,
            ),
        )

    def _caps_from_coverage(self, coverage: dict[str, CoverageEntry]) -> CapabilityReport:
        def readable(cat: str) -> bool:
            entry = coverage.get(cat)
            return bool(entry and entry.status in READABLE_COVERAGE_STATUSES)

        return CapabilityReport(
            source="cpanel",
            can_connect=True,
            can_authenticate=True,
            can_read_account_info=readable("account"),
            can_read_domains=readable("domains"),
            can_read_email=readable("email_accounts"),
            can_read_databases=readable("databases"),
            can_read_cron=readable("cron_jobs"),
            can_read_dns=readable("dns_records"),
            can_read_ssl=readable("ssl"),
            can_read_forwarders=readable("email_forwarders"),
            can_read_autoresponders=readable("email_autoresponders"),
            can_read_ftp=readable("ftp_accounts"),
            limitations=_limitations_from_coverage(coverage),
        )

    def collect(self) -> InventoryResult:
        coverage: dict[str, CoverageEntry] = {}

        # Domains doubles as the connect/auth gate (fatal on connection/auth).
        domains, domains_count, coverage["domains"] = self._read(
            "DomainInfo", "list_domains", _norm_domains,
            method="DomainInfo::list_domains",
        )
        account, _, coverage["account"] = self._read(
            "StatsBar", "get_stats", _norm_account, method="StatsBar::get_stats"
        )
        email, email_count, coverage["email_accounts"] = self._read(
            "Email", "list_pops", _norm_email, method="Email::list_pops"
        )
        databases, databases_count, coverage["databases"] = self._read(
            "Mysql", "list_databases", _norm_databases, method="Mysql::list_databases"
        )
        ssl, ssl_count, coverage["ssl"] = self._read(
            "SSL", "installed_hosts", _norm_ssl, method="SSL::installed_hosts"
        )
        cron, cron_count, coverage["cron_jobs"] = self._read_cron()
        dns_records, dns_count, coverage["dns_records"] = self._read_dns(domains)
        forwarders, _, coverage["email_forwarders"] = self._read(
            "Email", "list_forwarders", _norm_forwarders,
            method="Email::list_forwarders",
        )
        autoresponders, _, coverage["email_autoresponders"] = self._read(
            "Email", "list_auto_responders", _norm_autoresponders,
            method="Email::list_auto_responders",
        )
        ftp, _, coverage["ftp_accounts"] = self._read(
            "Ftp", "list_ftp", _norm_ftp, method="Ftp::list_ftp"
        )
        coverage.update(unverified_coverage())

        caps = self._caps_from_coverage(coverage)
        warnings = _limitations_from_coverage(coverage)

        data = {
            "account": account,
            "domains": domains,
            "email_accounts": email,
            "databases": databases,
            "cron_jobs": cron,
            "ssl": ssl,
            "dns_records": dns_records,
            "email_forwarders": forwarders,
            "email_autoresponders": autoresponders,
            "ftp_accounts": ftp,
            "dns": None,  # kept for Sprint 2 backward compatibility
            "warnings": warnings,
            "coverage": {k: v.model_dump() for k, v in coverage.items()},
        }
        summary = build_summary(
            domains_count=domains_count,
            email_accounts_count=email_count,
            databases_count=databases_count,
            cron_jobs_count=cron_count,
            dns_records_count=dns_count,
            ssl_items_count=ssl_count,
            warnings_count=len(warnings),
        )
        return InventoryResult(capabilities=caps, summary=summary, data=data)
