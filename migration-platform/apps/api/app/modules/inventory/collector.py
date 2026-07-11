"""Best-effort account-level inventory with explicit per-category evidence."""

from __future__ import annotations

from adapters.cpanel.client import CpanelClient

CATEGORIES = {
    "account": ("Variables", "get_user_information"),
    "domains": ("DomainInfo", "list_domains"),
    "email_accounts": ("Email", "list_pops_with_disk"),
    "databases": ("Mysql", "list_databases"),
    "mysql_users": ("Mysql", "list_users"),
    "ssl": ("SSL", "list_certs"),
    "email_forwarders": ("Email", "list_forwarders"),
    "ftp_accounts": ("Ftp", "list_ftp_with_disk"),
    "redirects": ("Mime", "list_redirects"),
    "mailing_lists": ("Email", "list_lists"),
    "php_settings": ("LangPHP", "php_get_vhost_versions"),
    "postgres_databases": ("Postgresql", "list_databases"),
    "subaccounts": ("UserManager", "list_users"),
}

UNVERIFIED: tuple[str, ...] = ()


def _items(payload: dict) -> list | dict:
    envelope = payload.get("result") if isinstance(payload.get("result"), dict) else payload
    data = envelope.get("data")
    if data is None:
        return []
    return data


def collect(client: CpanelClient) -> tuple[dict, dict]:
    data: dict[str, object] = {"coverage": {}}
    coverage: dict[str, dict] = data["coverage"]  # type: ignore[assignment]
    warnings = 0
    for category, (module, function) in CATEGORIES.items():
        try:
            value = _items(client.execute(module, function))
            count = _count(category, value)
            data[category] = value
            coverage[category] = {
                "status": "empty" if count == 0 else "succeeded",
                "method": f"UAPI {module}::{function}",
                "read_only_verified": True,
                "items_count": count,
                "message": None,
            }
        except Exception as exc:
            message = str(exc)
            unsupported = "does not support this functionality" in message.lower()
            if not unsupported:
                warnings += 1
            coverage[category] = {
                "status": "unsupported" if unsupported else "unavailable",
                "method": f"UAPI {module}::{function}",
                "read_only_verified": True,
                "items_count": None,
                "message": message,
            }
    _collect_dns(client, data, coverage)
    if coverage["dns_records"]["status"] in {"partial", "unavailable"}:
        warnings += 1
    _collect_autoresponders(client, data, coverage)
    if coverage["email_autoresponders"]["status"] in {"partial", "unavailable"}:
        warnings += 1
    _collect_email_filters(client, data, coverage)
    if coverage["email_filters"]["status"] in {"partial", "unavailable"}:
        warnings += 1
    _collect_cron(client, data, coverage)
    if coverage["cron_jobs"]["status"] in {"unsupported", "unavailable"}:
        warnings += 1
    for category in UNVERIFIED:
        coverage[category] = {
            "status": "unverified", "method": None, "read_only_verified": False,
            "items_count": None, "message": "Collector not implemented yet.",
        }
        warnings += 1
    summary = {
        "domains_count": coverage["domains"]["items_count"],
        "email_accounts_count": coverage["email_accounts"]["items_count"],
        "databases_count": coverage["databases"]["items_count"],
        "cron_jobs_count": coverage["cron_jobs"]["items_count"],
        "dns_records_count": coverage["dns_records"]["items_count"],
        "ssl_items_count": coverage["ssl"]["items_count"],
        "warnings_count": warnings,
    }
    return data, summary


def _collect_autoresponders(client: CpanelClient, data: dict, coverage: dict) -> None:
    responders: list[dict] = []
    failures: list[str] = []
    successful_lists = 0
    for domain in _domains(data.get("domains")):
        try:
            payload = _items(client.execute("Email", "list_auto_responders", {"domain": domain}))
            listed = payload if isinstance(payload, list) else []
            successful_lists += 1
        except Exception as exc:
            failures.append(f"list {domain}: {exc}")
            continue
        for summary in listed:
            if not isinstance(summary, dict):
                continue
            address = summary.get("email")
            if not address:
                failures.append(f"detail {domain}: indirizzo mancante")
                responders.append({**summary, "_domain": domain, "_detail_status": "failed"})
                continue
            try:
                detail = _items(client.execute("Email", "get_auto_responder", {"email": str(address)}))
                if not isinstance(detail, dict):
                    raise ValueError("risposta dettaglio non valida")
                responders.append({**summary, **detail, "email": str(address), "_domain": domain, "_detail_status": "succeeded"})
            except Exception as exc:
                failures.append(f"detail {address}: {exc}")
                responders.append({**summary, "_domain": domain, "_detail_status": "failed"})
    if failures and (responders or successful_lists):
        status = "partial"
    elif failures:
        status = "unavailable"
    else:
        status = "empty" if not responders else "succeeded"
    data["email_autoresponders"] = responders
    coverage["email_autoresponders"] = {
        "status": status,
        "method": "UAPI Email::list_auto_responders (per domain) + Email::get_auto_responder (per address)",
        "read_only_verified": True,
        "items_count": len(responders) if status in {"succeeded", "empty", "partial"} else None,
        "message": "; ".join(failures) if failures else None,
    }


def _collect_email_filters(client: CpanelClient, data: dict, coverage: dict) -> None:
    accounts = [None]
    accounts.extend(
        item.get("email") for item in data.get("email_accounts", [])
        if isinstance(item, dict) and item.get("email")
    )
    filters: list[dict] = []
    failures: list[str] = []
    for account in accounts:
        try:
            params = {"account": account} if account else None
            payload = _items(client.execute("Email", "list_filters", params))
            items = payload if isinstance(payload, list) else []
            filters.extend({**item, "_account": account or "account"} for item in items if isinstance(item, dict))
        except Exception as exc:
            failures.append(f"{account or 'account'}: {exc}")
    coverage["email_filters"] = {
        "status": "partial" if filters and failures else "unavailable" if failures else "empty" if not filters else "succeeded",
        "method": "UAPI Email::list_filters (account + mailbox)",
        "read_only_verified": True,
        "items_count": len(filters) if not failures or filters else None,
        "message": "; ".join(failures) if failures else None,
    }
    data["email_filters"] = filters


def _collect_cron(client: CpanelClient, data: dict, coverage: dict) -> None:
    try:
        payload = client.api2("Cron", "listcron")
        items = payload.get("cpanelresult", {}).get("data") or []
        data["cron_jobs"] = items
        coverage["cron_jobs"] = {
            "status": "empty" if not items else "succeeded",
            "method": "cPanel API 2 Cron::listcron",
            "read_only_verified": True,
            "items_count": len(items), "message": "Legacy API 2; no UAPI equivalent exists.",
        }
    except Exception as exc:
        data["cron_jobs"] = []
        coverage["cron_jobs"] = {
            "status": "unsupported", "method": "cPanel API 2 Cron::listcron",
            "read_only_verified": True, "items_count": None, "message": str(exc),
        }


def _domains(value: object) -> list[str]:
    if not isinstance(value, dict):
        return []
    found: set[str] = set()
    main = value.get("main_domain")
    if isinstance(main, str) and main:
        found.add(main)
    for field in ("addon_domains", "sub_domains", "parked_domains"):
        items = value.get(field, [])
        if isinstance(items, list):
            found.update(str(item) for item in items if item)
    return sorted(found)


def _count(category: str, value: object) -> int:
    if category == "account":
        return 1 if value else 0
    if category == "domains":
        return len(_domains(value))
    return len(value) if isinstance(value, (list, dict)) else int(value is not None)


def _collect_dns(client: CpanelClient, data: dict, coverage: dict) -> None:
    zones = _domains(data.get("domains"))
    records: list[dict] = []
    failures: list[str] = []
    for zone in zones:
        try:
            payload = _items(client.execute("DNS", "parse_zone", {"zone": zone}))
            items = payload if isinstance(payload, list) else [payload]
            for item in items:
                if isinstance(item, dict):
                    records.append({**item, "_zone": zone})
        except Exception as exc:
            failures.append(f"{zone}: {exc}")
    data["dns_records"] = records
    if failures and records:
        status = "partial"
    elif failures:
        status = "unavailable"
    else:
        status = "empty" if not records else "succeeded"
    coverage["dns_records"] = {
        "status": status,
        "method": "UAPI DNS::parse_zone (per zone)",
        "read_only_verified": True,
        "items_count": len(records) if status in {"succeeded", "empty", "partial"} else None,
        "message": "; ".join(failures) if failures else None,
    }
