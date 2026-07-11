from __future__ import annotations

AUTO = {"domains", "databases", "email_forwarders"}
APPROVAL = {"cron_jobs", "dns_records"}
SECRET = {"ftp_accounts", "mailing_lists", "mysql_users"}
MANUAL = {"email_autoresponders", "php_settings"}


def build_steps(entries: list[dict]) -> tuple[list[dict], dict]:
    actionable = [entry for entry in entries if entry["state"] in {"missing_on_destination", "different", "unknown"}]
    ftp_names = {
        entry["key"].split("@", 1)[0]
        for entry in actionable if entry["category"] == "ftp_accounts"
    }
    email_names = {
        entry["key"].split("@", 1)[0]
        for entry in actionable if entry["category"] == "email_accounts"
    }
    steps: list[dict] = []
    for entry in actionable:
        category, key = entry["category"], entry["key"]
        if category in {"database_contract", "mysql_grant_contract"}:
            mode, reason = "excluded", "Evidenza di quota/restrizioni per il passo database; non è una risorsa autonoma da accodare."
        elif category in {"ftp_contract", "mailing_list_contract", "forwarder_contract", "autoresponder_contract", "dns_contract"}:
            mode, reason = "excluded", "Evidenza read-only per il writer; non è una risorsa autonoma da accodare."
        elif category == "mysql_grants":
            mode, reason = "excluded", "Evidenza di supporto per il passo utente MySQL; non è una risorsa autonoma da accodare."
        elif category == "subaccounts" and (key.split("@", 1)[0] in ftp_names or key.split("@", 1)[0] in email_names or key.endswith("_logs")):
            mode, reason = "excluded", "Identità già rappresentata da email/FTP oppure account di servizio cPanel."
        elif category in AUTO:
            mode, reason = "automatic", "Supportato da API account-level e verificabile con un nuovo preflight."
        elif category in APPROVAL:
            mode, reason = "approval", "Richiede conferma esplicita prima della scrittura."
        elif category in SECRET:
            mode, reason = "secret_required", "La password sorgente non è recuperabile; serve una nuova password."
        elif category == "ssl":
            mode, reason = "excluded", "Il certificato deve essere rigenerato da AutoSSL dopo domini e DNS, non copiato con la chiave privata."
        elif category in MANUAL:
            mode, reason = "manual", "I dati disponibili o i privilegi account-level non bastano per una scrittura automatica sicura."
        elif entry["state"] == "unknown":
            mode, reason = "manual", "Copertura insufficiente: verificare manualmente prima del cutover."
        else:
            mode, reason = "manual", "Nessun writer automatico sicuro disponibile."
        dependencies: list[str] = []
        if category in {"php_settings", "ssl", "dns_records"}:
            dependencies = ["domains"]
        if category in {"mysql_users"}:
            dependencies = ["databases"]
        steps.append({
            "id": f"{category}:{key}", "category": category, "key": key,
            "title": entry["title"], "mode": mode, "reason": reason,
            "state": "pending", "comparison_state": entry["state"], "severity": entry["severity"],
            "depends_on_categories": dependencies,
        })
    order = {"domains": 10, "databases": 20, "mysql_users": 30, "ftp_accounts": 40, "subaccounts": 41,
             "email_forwarders": 50, "email_autoresponders": 51, "mailing_lists": 52, "cron_jobs": 60,
             "php_settings": 70, "ssl": 80, "dns_records": 90}
    steps.sort(key=lambda step: (order.get(step["category"], 999), step["key"]))
    counts = {mode: sum(step["mode"] == mode for step in steps) for mode in ("automatic", "approval", "secret_required", "manual", "excluded")}
    counts["total"] = len(steps)
    counts["blocking"] = sum(step["severity"] == "blocker" and step["mode"] != "excluded" for step in steps)
    return steps, counts
