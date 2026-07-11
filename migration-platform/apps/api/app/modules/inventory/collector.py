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
    _collect_mysql_grants(client, data, coverage)
    _assess_mysql_grant_contract(data, coverage)
    _collect_database_contract(client, data, coverage)
    _assess_ftp_writer_metadata(data, coverage)
    _assess_mailing_list_privacy(client, data, coverage)
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


def _named_items(value: object, *keys: str) -> list[str]:
    items = value if isinstance(value, list) else []
    result: list[str] = []
    for item in items:
        if isinstance(item, str):
            result.append(item)
        elif isinstance(item, dict):
            name = next((item.get(key) for key in keys if item.get(key)), None)
            if name:
                result.append(str(name))
    return sorted(set(result))


def _collect_mysql_grants(client: CpanelClient, data: dict, coverage: dict) -> None:
    databases = _named_items(data.get("databases"), "database", "name")
    users = _named_items(data.get("mysql_users"), "user", "name", "username")
    if coverage.get("databases", {}).get("status") not in {"succeeded", "empty"} or coverage.get("mysql_users", {}).get("status") not in {"succeeded", "empty"}:
        data["mysql_grants"] = []
        coverage["mysql_grants"] = {"status": "unavailable", "method": "UAPI Mysql::get_privileges_on_database (per user/database)", "read_only_verified": True, "items_count": None, "message": "Database o utenti MySQL non leggibili."}
        return
    grants: list[dict] = []
    failures: list[str] = []
    checked = 0
    for database in databases:
        for user in users:
            try:
                raw = _items(client.execute("Mysql", "get_privileges_on_database", {"database": database, "user": user}))
                privileges = _privilege_names(raw)
                checked += 1
                if privileges:
                    grants.append({"database": database, "user": user, "privileges": privileges})
            except Exception:
                failures.append(f"{user}@{database}")
    data["mysql_grants"] = grants
    status = "partial" if failures and checked else "unavailable" if failures else "empty" if not grants else "succeeded"
    coverage["mysql_grants"] = {
        "status": status, "method": "UAPI Mysql::get_privileges_on_database (per user/database)",
        "read_only_verified": True, "items_count": len(grants) if status != "unavailable" else None,
        "message": f"{len(failures)} coppie non verificabili." if failures else None,
        "pairs_checked": checked, "pairs_total": len(databases) * len(users),
    }


MYSQL_PRIVILEGES = {
    "ALL PRIVILEGES", "ALTER", "ALTER ROUTINE", "CREATE", "CREATE ROUTINE",
    "CREATE TEMPORARY TABLES", "CREATE VIEW", "DELETE", "DROP", "EVENT",
    "EXECUTE", "INDEX", "INSERT", "LOCK TABLES", "REFERENCES", "SELECT",
    "SHOW VIEW", "TRIGGER", "UPDATE",
}


def _assess_mysql_grant_contract(data: dict, coverage: dict) -> None:
    grant_coverage = coverage.get("mysql_grants", {})
    grants = data.get("mysql_grants", []) if isinstance(data.get("mysql_grants"), list) else []
    complete = grant_coverage.get("status") in {"succeeded", "empty"} and grant_coverage.get("pairs_checked") == grant_coverage.get("pairs_total")
    invalid = sorted({privilege for grant in grants if isinstance(grant, dict) for privilege in grant.get("privileges", []) if privilege not in MYSQL_PRIVILEGES})
    status = "succeeded" if complete and not invalid else "failed" if invalid else "unavailable"
    data["mysql_grant_contract"] = {"pairs_checked": grant_coverage.get("pairs_checked", 0), "pairs_total": grant_coverage.get("pairs_total", 0), "grants_count": len(grants), "invalid_privileges": invalid}
    coverage["mysql_grant_contract"] = {"status": status, "method": "UAPI Mysql::get_privileges_on_database contract validation", "read_only_verified": True, "items_count": 1 if status == "succeeded" else None, "message": "Privilegi non supportati dal contratto." if invalid else None}


def _collect_database_contract(client: CpanelClient, data: dict, coverage: dict) -> None:
    if coverage.get("account", {}).get("status") not in {"succeeded", "empty"} or coverage.get("databases", {}).get("status") not in {"succeeded", "empty"}:
        coverage["database_contract"] = {"status": "unavailable", "method": "UAPI Mysql::get_restrictions + inventory quota", "read_only_verified": True, "items_count": None, "message": "Account o database non leggibili."}
        return
    try:
        restrictions = _items(client.execute("Mysql", "get_restrictions"))
        if not isinstance(restrictions, dict) or not restrictions:
            raise ValueError("restrizioni MySQL assenti")
        account = data.get("account") if isinstance(data.get("account"), dict) else {}
        maximum = account.get("maximum_databases")
        current = len(data.get("databases", [])) if isinstance(data.get("databases"), list) else 0
        quota_known = maximum not in {None, ""}
        data["database_contract"] = {"restrictions": restrictions, "quota": {"maximum": maximum, "current": current, "known": quota_known}}
        coverage["database_contract"] = {"status": "succeeded" if quota_known else "partial", "method": "UAPI Mysql::get_restrictions + Variables::get_user_information", "read_only_verified": True, "items_count": 1, "message": None if quota_known else "Limite database non disponibile."}
    except Exception as exc:
        data["database_contract"] = {}
        coverage["database_contract"] = {"status": "unavailable", "method": "UAPI Mysql::get_restrictions + Variables::get_user_information", "read_only_verified": True, "items_count": None, "message": str(exc)}


def _privilege_names(value: object) -> list[str]:
    if isinstance(value, dict):
        value = value.get("privileges", value)
    if isinstance(value, str):
        return sorted({part.strip().upper() for part in value.split(",") if part.strip()})
    if isinstance(value, list):
        names = []
        for item in value:
            if isinstance(item, str):
                names.append(item)
            elif isinstance(item, dict):
                name = item.get("privilege") or item.get("name")
                if name:
                    names.append(str(name))
        return sorted({name.strip().upper() for name in names if name.strip()})
    return []


def _assess_ftp_writer_metadata(data: dict, coverage: dict) -> None:
    if coverage.get("ftp_accounts", {}).get("status") not in {"succeeded", "empty"}:
        return
    items = data.get("ftp_accounts") if isinstance(data.get("ftp_accounts"), list) else []
    subaccounts = [item for item in items if isinstance(item, dict) and (item.get("accttype") == "sub" or item.get("type") == "sub")]
    incomplete = 0
    for item in subaccounts:
        quota = item.get("diskquota", item.get("quota"))
        homedir = item.get("homedir", item.get("dir"))
        item["_writer_metadata_status"] = "succeeded" if quota is not None and homedir else "failed"
        incomplete += item["_writer_metadata_status"] == "failed"
    if incomplete:
        coverage["ftp_accounts"]["status"] = "partial"
        coverage["ftp_accounts"]["message"] = f"Quota/home mancanti per {incomplete} account FTP migrabili."


def _mailing_list_key(item: dict) -> str:
    value = item.get("list") or item.get("listname") or item.get("email") or ""
    if "@" not in str(value) and item.get("domain"):
        value = f"{value}@{item['domain']}"
    return str(value).lower()


def _privacy_value(item: dict) -> int | None:
    value = item.get("private")
    if value in {0, False, "0"}:
        return 0
    if value in {1, True, "1"}:
        return 1
    listtype = item.get("listtype")
    if listtype in {"private", "public"}:
        return 1 if listtype == "private" else 0
    archive_private = item.get("archive_private")
    advertised = item.get("advertised")
    subscribe_policy = item.get("subscribe_policy")
    explicit = {0, 1, False, True, "0", "1"}
    if archive_private in explicit and advertised in explicit and str(subscribe_policy) in {"1", "2", "3"}:
        archive_is_private = str(int(archive_private)) == "1"
        is_advertised = str(int(advertised)) == "1"
        approval_required = str(subscribe_policy) in {"2", "3"}
        return int(archive_is_private and not is_advertised and approval_required)
    return None


def _assess_mailing_list_privacy(client: CpanelClient, data: dict, coverage: dict) -> None:
    if coverage.get("mailing_lists", {}).get("status") not in {"succeeded", "empty"}:
        return
    items = data.get("mailing_lists") if isinstance(data.get("mailing_lists"), list) else []
    fallback: dict[str, int] = {}
    if any(isinstance(item, dict) and _privacy_value(item) is None for item in items):
        try:
            payload = client.api2("Email", "listlists")
            legacy_items = payload.get("cpanelresult", {}).get("data") or []
            fallback = {
                _mailing_list_key(item): value
                for item in legacy_items if isinstance(item, dict)
                if (value := _privacy_value(item)) is not None
            }
        except Exception:
            fallback = {}
    incomplete = 0
    for item in items:
        if not isinstance(item, dict):
            continue
        value = _privacy_value(item)
        source = "uapi"
        if value is None:
            value = fallback.get(_mailing_list_key(item))
            source = "api2" if value is not None else "unavailable"
        if value is not None:
            item["private"] = value
        item["_privacy_status"] = "succeeded" if value is not None else "failed"
        item["_privacy_source"] = source
        incomplete += item["_privacy_status"] == "failed"
    if incomplete:
        coverage["mailing_lists"]["status"] = "partial"
        coverage["mailing_lists"]["message"] = f"Privacy non verificata per {incomplete} mailing list."


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


def _dns_zones(value: object) -> list[str]:
    """Return domains that can own a cPanel DNS zone.

    Subdomains are resources, but normally live inside their parent domain's
    zone. Querying each one as an autonomous zone creates false partial reads.
    """
    if not isinstance(value, dict):
        return []
    found: set[str] = set()
    main = value.get("main_domain")
    if isinstance(main, str) and main:
        found.add(main)
    for field in ("addon_domains", "parked_domains"):
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
    zones = _dns_zones(data.get("domains"))
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
