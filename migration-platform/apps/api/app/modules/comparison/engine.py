"""Pure deterministic comparison of two normalized/raw inventory payloads."""

from __future__ import annotations

import hashlib
import json
import base64

READABLE = {"succeeded", "empty", "partial"}
IDENTITY_FIELDS = ("email", "login", "database", "user", "username", "domain", "domain_name", "name", "id")
IGNORED_FIELDS = {"diskused", "disk_usage", "humandiskused", "mtime", "last_login"}


def _fingerprint(value: object) -> str:
    if isinstance(value, dict):
        value = {k: v for k, v in value.items() if k not in IGNORED_FIELDS}
    raw = json.dumps(value, sort_keys=True, separators=(",", ":"), default=str)
    return hashlib.sha256(raw.encode()).hexdigest()


def _flatten(value: object) -> list[object]:
    if isinstance(value, list):
        return value
    if isinstance(value, dict):
        # UAPI list_domains returns named arrays; preserve their domain values.
        if all(isinstance(v, list) for v in value.values()):
            return [item for values in value.values() for item in values]
        return [value]
    return []


def _normalize(category: str, value: object) -> list[object]:
    if category == "account" and isinstance(value, dict):
        fields = (
            "user", "domain", "maximum_databases", "maximum_mail_accounts",
            "maximum_ftp_accounts", "maximum_addon_domains", "maximum_subdomains",
            "maximum_mailing_lists", "disk_block_limit", "bandwidth_limit",
            "shell", "mailbox_format", "dkim_enabled", "spf_enabled",
        )
        return [{field: value.get(field) for field in fields}]
    if category == "domains" and isinstance(value, dict):
        result: list[dict] = []
        main = value.get("main_domain")
        if main:
            result.append({"domain": main, "type": "main"})
        for field, kind in (("addon_domains", "addon"), ("sub_domains", "subdomain"), ("parked_domains", "alias")):
            for domain in value.get(field, []) or []:
                result.append({"domain": domain, "type": kind})
        return result
    items = _flatten(value)
    if category == "email_accounts":
        return [{"email": item.get("email") or item.get("login"), "quota": item.get("diskquota"), "suspended": item.get("suspended_login")} for item in items if isinstance(item, dict)]
    if category == "email_forwarders":
        return [{"name": f"{item.get('dest')} -> {item.get('forward')}", "source": item.get("dest"), "destination": item.get("forward")} for item in items if isinstance(item, dict)]
    if category == "email_autoresponders":
        return [{
            "email": item.get("email"), "subject": item.get("subject"),
            "body": item.get("body"), "from": item.get("from"),
            "interval": item.get("interval"), "is_html": item.get("is_html"),
            "charset": item.get("charset"), "start": item.get("start"),
            "stop": item.get("stop"), "detail_status": item.get("_detail_status"),
        } for item in items if isinstance(item, dict)]
    if category == "email_filters":
        return [{"name": f"{item.get('_account')}:{item.get('filtername') or item.get('name')}", "account": item.get("_account"), "rules": item.get("rules"), "actions": item.get("actions"), "enabled": item.get("enabled", 1)} for item in items if isinstance(item, dict)]
    if category == "mailing_lists":
        return [{"name": item.get("list") or item.get("listname") or item.get("email"), "domain": item.get("domain"), "private": item.get("private")} for item in items if isinstance(item, dict)]
    if category == "redirects":
        return [{"name": item.get("sourceurl") or item.get("source") or item.get("domain"), "destination": item.get("destination"), "type": item.get("type"), "wildcard": item.get("wildcard")} for item in items if isinstance(item, dict)]
    if category == "php_settings":
        return [{"domain": item.get("vhost") or item.get("domain"), "version": item.get("version") or item.get("phpversion")} for item in items if isinstance(item, dict)]
    if category == "postgres_databases":
        return [{"database": item.get("database") or item.get("name"), "users": item.get("users")} for item in items if isinstance(item, dict)]
    if category == "subaccounts":
        return [{"email": item.get("email") or item.get("username"), "services": item.get("services"), "domain": item.get("domain")} for item in items if isinstance(item, dict)]
    if category == "cron_jobs":
        return [{"name": f"{item.get('minute')} {item.get('hour')} {item.get('day')} {item.get('month')} {item.get('weekday')}|{item.get('command')}", "minute": item.get("minute"), "hour": item.get("hour"), "day": item.get("day"), "month": item.get("month"), "weekday": item.get("weekday"), "command": item.get("command")} for item in items if isinstance(item, dict)]
    if category == "ftp_accounts":
        return [{"login": item.get("login"), "type": "sub"} for item in items if isinstance(item, dict) and (item.get("accttype") == "sub" or item.get("type") == "sub")]
    if category == "ssl":
        by_domain: dict[str, dict] = {}
        for item in items:
            if not isinstance(item, dict):
                continue
            for domain in item.get("domains", []) or []:
                by_domain[str(domain).lower()] = {
                    "domain": str(domain).lower(),
                    "self_signed": bool(item.get("is_self_signed")),
                    "validation_type": item.get("validation_type"),
                }
        return list(by_domain.values())
    if category == "dns_records":
        result = []
        for item in items:
            if not isinstance(item, dict):
                continue
            if item.get("type") != "record":
                continue
            record_type = item.get("record_type")
            if record_type in {"SOA", "NS"}:
                continue
            name = item.get("dname_raw") or _decode_b64(item.get("dname_b64")) or item.get("_zone")
            if _generated_dns_name(str(name), str(item.get("_zone") or "")):
                continue
            encoded_data = item.get("data_b64") or []
            values = [_decode_b64(part) for part in encoded_data] if isinstance(encoded_data, list) else []
            value_field = " ".join(value for value in values if value)
            identity = f"{name}|{record_type}"
            if record_type in {"MX", "SRV", "TXT", "CAA"}:
                identity = f"{identity}|{value_field}"
            result.append({
                "name": identity,
                "zone": item.get("_zone"), "record_name": name,
                "type": record_type, "value": value_field, "ttl": item.get("ttl"),
            })
        return result
    return items


def _decode_b64(value: object) -> str:
    if not isinstance(value, str) or not value:
        return ""
    try:
        return base64.b64decode(value).decode("utf-8")
    except Exception:
        return value


def _generated_dns_name(name: str, zone: str) -> bool:
    normalized = name.rstrip(".").lower()
    zone = zone.rstrip(".").lower()
    relative = normalized.removesuffix(f".{zone}").rstrip(".") if zone else normalized
    first = relative.split(".", 1)[0]
    return first in {
        "cpanel", "whm", "webmail", "webdisk", "cpcontacts", "cpcalendars",
        "autoconfig", "autodiscover", "_acme-challenge", "_cpanel-dcv-test-record",
    }


def _key(item: object, index: int) -> str:
    if isinstance(item, str):
        return item.lower()
    if isinstance(item, dict):
        for field in IDENTITY_FIELDS:
            value = item.get(field)
            if value not in (None, ""):
                return str(value).lower()
    return f"item-{index + 1}:{_fingerprint(item)[:12]}"


def _index(category: str, value: object) -> dict[str, str]:
    result = {}
    for index, item in enumerate(_normalize(category, value)):
        result[_key(item, index)] = _fingerprint(item)
    return result


def compare(source: dict, destination: dict) -> tuple[list[dict], dict]:
    entries: list[dict] = []
    by_category: dict[str, dict] = {}
    source_coverage = source.get("coverage", {})
    destination_coverage = destination.get("coverage", {})
    categories = sorted(set(source_coverage) | set(destination_coverage))
    for category in categories:
        src_status = source_coverage.get(category, {}).get("status", "unverified")
        dst_status = destination_coverage.get(category, {}).get("status", "unverified")
        if src_status not in READABLE or dst_status not in READABLE:
            by_category[category] = {"source": 0, "destination": 0, "match": 0, "blocker": 0, "warning": 0, "info": 0, "skipped": True}
            if src_status == "unsupported" and dst_status == "unsupported":
                continue
            entries.append({
                "category": category, "key": "__coverage__", "state": "unknown", "severity": "warning",
                "title": f"{category}: confronto non affidabile",
                "message": f"Copertura sorgente={src_status}, destinazione={dst_status}. Verifica manuale necessaria.",
                "source": {"exists": src_status in READABLE, "fingerprint": None},
                "destination": {"exists": dst_status in READABLE, "fingerprint": None},
            })
            continue
        src = _index(category, source.get(category, []))
        dst = _index(category, destination.get(category, []))
        stats = {"source": len(src), "destination": len(dst), "match": 0, "blocker": 0, "warning": 0, "info": 0, "skipped": False}
        for key in sorted(set(src) | set(dst)):
            if key not in dst:
                severity = "warning" if category in {"ssl", "dns_records"} else "blocker"
                state, message = "missing_on_destination", "Presente sul sorgente e assente sulla destinazione."
            elif key not in src:
                state, severity, message = "only_on_destination", "info", "Presente solo sulla destinazione; controllare prima di sovrascrivere."
            elif src[key] != dst[key]:
                state, severity, message = "different", "warning", "Presente su entrambi ma con configurazione differente."
            else:
                state, severity, message = "match", "info", "Corrisponde sui due endpoint."
                stats["match"] += 1
            stats[severity] += 1
            entries.append({
                "category": category, "key": key, "state": state, "severity": severity,
                "title": f"{category}: {key}", "message": message,
                "source": {"exists": key in src, "fingerprint": src.get(key)},
                "destination": {"exists": key in dst, "fingerprint": dst.get(key)},
            })
        by_category[category] = stats
    counts = {level: sum(1 for entry in entries if entry["severity"] == level) for level in ("blocker", "warning", "info")}
    summary = {
        "blockers_count": counts["blocker"], "warnings_count": counts["warning"], "infos_count": counts["info"],
        "categories": categories, "by_category": by_category,
    }
    return entries, summary
