from __future__ import annotations

WRITER_CATEGORIES = (
    "domains", "databases", "mysql_users", "email_forwarders", "cron_jobs",
    "ftp_accounts", "mailing_lists", "dns_records", "email_autoresponders",
)
READABLE = {"succeeded", "empty"}
EVIDENCE_CATEGORIES = {
    "database_contract", "mysql_grant_contract", "mysql_grants",
    "ftp_contract", "mailing_list_contract", "forwarder_contract",
    "autoresponder_contract", "dns_contract",
}
PRIORITY = {
    "not_ready": 0, "needs_inventory": 1, "needs_contract_test": 2,
    "needs_operator_input": 3, "eligible_for_real_design": 4,
}

GAPS = {
    "domains": [("not_ready", "writer_contract", "Manca un contratto ufficiale del writer e una procedura di recovery/rollback manuale.")],
    "databases": [("needs_contract_test", "account_level_contract", "Servono contract test account-level e verifica preventiva della quota database.")],
    "mysql_users": [("not_ready", "privilege_mapping", "L'inventario non conserva ancora la mappatura sorgente utente→database→privilegi.")],
    "email_forwarders": [("needs_contract_test", "fresh_read", "Serve una fresh read reale immediatamente precedente alla scrittura additiva.")],
    "cron_jobs": [("not_ready", "api2_rollback", "Il writer API 2 richiede contratto, approval forte e procedura di rollback.")],
    "ftp_accounts": [("needs_inventory", "quota_home", "Quota e home directory non sono ancora inventariate con affidabilità per il writer.")],
    "mailing_lists": [("needs_inventory", "private_visibility", "Il campo private può non essere verificato dall'inventario corrente.")],
    "dns_records": [("not_ready", "collision_and_zone_verification", "Servono gestione collisioni/record differenti e verifica fresca dell'intera zona.")],
    "email_autoresponders": [("needs_contract_test", "fresh_uapi", "Serve una fresh UAPI reale anti-upsert prima della scrittura additiva.")],
}


def _coverage(snapshot_data: dict | None, category: str) -> str:
    value = (snapshot_data or {}).get("coverage", {}).get(category, {})
    return value.get("status", "unverified") if isinstance(value, dict) else "unverified"


def _category_gaps(category: str, source_data: dict | None, destination_data: dict | None = None) -> list[tuple[str, str, str]]:
    data = source_data or {}
    if category == "email_forwarders" and _coverage(data, "forwarder_contract") == "succeeded" and _coverage(destination_data or {}, "forwarder_contract") == "succeeded":
        return [("eligible_for_real_design", "forwarder_contract_verified", "La fresh read per coppia completa è supportata da evidenze read-only correnti su entrambi gli endpoint.")]
    if category == "email_autoresponders" and _coverage(data, "autoresponder_contract") == "succeeded" and _coverage(destination_data or {}, "autoresponder_contract") == "succeeded":
        return [("eligible_for_real_design", "autoresponder_contract_verified", "Lista e dettaglio per indirizzo supportano il futuro controllo anti-upsert su entrambi gli endpoint.")]
    if category == "dns_records" and _coverage(data, "dns_contract") == "succeeded" and _coverage(destination_data or {}, "dns_contract") == "succeeded":
        return [("eligible_for_real_design", "dns_contract_verified", "Zone proprietarie, collisioni e strategia di fresh read sono censite su entrambi gli endpoint.")]
    if category == "databases" and _coverage(data, "database_contract") == "succeeded" and _coverage(destination_data or {}, "database_contract") == "succeeded":
        return [("eligible_for_real_design", "database_contract_verified", "Restrizioni e quota database sono state verificate in lettura su evidenze correnti.")]
    if category == "mysql_users" and _coverage(data, "mysql_grant_contract") == "succeeded" and _coverage(destination_data or {}, "mysql_grant_contract") == "succeeded":
        return [("eligible_for_real_design", "privilege_contract_verified", "Matrice e privilegi MySQL rispettano il contratto read-only su entrambi gli endpoint.")]
    if category == "mysql_users" and _coverage(data, "mysql_grants") in READABLE:
        return [("needs_contract_test", "privilege_contract", "La matrice utente→database→privilegi è disponibile; serve validare il contratto reale di grant.")]
    if category == "ftp_accounts":
        if _coverage(data, "ftp_contract") == "succeeded" and _coverage(destination_data or {}, "ftp_contract") == "succeeded":
            return [("eligible_for_real_design", "ftp_contract_verified", "Mapping quota/home e limite account FTP sono verificati in lettura su entrambi gli endpoint.")]
        items = data.get("ftp_accounts", [])
        migratable = [item for item in items if isinstance(item, dict) and (item.get("accttype") == "sub" or item.get("type") == "sub")]
        if all(item.get("_writer_metadata_status") == "succeeded" for item in migratable):
            return [("needs_contract_test", "ftp_contract", "Quota e home sono disponibili; serve un contract test account-level del writer.")]
    if category == "mailing_lists":
        if _coverage(data, "mailing_list_contract") == "succeeded" and _coverage(destination_data or {}, "mailing_list_contract") == "succeeded":
            return [("eligible_for_real_design", "mailing_list_contract_verified", "Mapping private e limite mailing list sono verificati in lettura su entrambi gli endpoint.")]
        items = data.get("mailing_lists", [])
        if all(isinstance(item, dict) and item.get("_privacy_status") == "succeeded" for item in items):
            return [("needs_contract_test", "mailing_list_contract", "La privacy è verificata; serve un contract test account-level del writer.")]
    return GAPS[category]


def build_report(plan_steps: list[dict], source_data: dict | None, destination_data: dict | None) -> tuple[list[dict], list[dict], dict, list[dict]]:
    categories: list[dict] = []
    step_results: list[dict] = []
    for category in WRITER_CATEGORIES:
        source_status = _coverage(source_data, category)
        destination_status = _coverage(destination_data, category)
        configured_gaps = _category_gaps(category, source_data, destination_data)
        gaps = [{"code": code, "message": message} for _, code, message in configured_gaps]
        statuses = [status for status, _, _ in configured_gaps]
        if source_status not in READABLE or destination_status not in READABLE:
            statuses.append("needs_inventory")
            gaps.insert(0, {"code": "coverage_not_readable", "message": "La categoria non è leggibile in modo completo su entrambi gli snapshot."})
        category_steps = [step for step in plan_steps if step.get("category") == category]
        category_status = min(statuses, key=PRIORITY.__getitem__)
        categories.append({
            "category": category, "status": category_status,
            "source_coverage": source_status, "destination_coverage": destination_status,
            "step_count": len(category_steps), "gaps": gaps,
        })
        for step in category_steps:
            step_gaps = list(gaps)
            step_statuses = list(statuses)
            if category == "dns_records":
                source_contract = (source_data or {}).get("dns_contract", {})
                destination_contract = (destination_data or {}).get("dns_contract", {})
                key = str(step.get("key") or "")
                collision_keys = set(source_contract.get("collision_keys", [])) | set(destination_contract.get("collision_keys", []))
                unsupported_keys = set(source_contract.get("unsupported_keys", [])) | set(destination_contract.get("unsupported_keys", []))
                if step.get("comparison_state") != "missing_on_destination":
                    step_statuses.append("not_ready")
                    step_gaps.append({"code": "dns_not_additive", "message": "Il passo DNS non è puramente additivo: record differenti o ignoti richiedono intervento manuale."})
                if key in collision_keys:
                    step_statuses.append("not_ready")
                    step_gaps.append({"code": "dns_ambiguous_identity", "message": "Più record condividono la stessa identità di piano; il writer non può sceglierne uno implicitamente."})
                if key in unsupported_keys:
                    step_statuses.append("not_ready")
                    step_gaps.append({"code": "dns_type_unsupported", "message": "Il tipo record non appartiene al contratto additivo supportato."})
            if step.get("mode") == "secret_required":
                step_statuses.append("needs_operator_input")
                step_gaps.append({"code": "new_secret_required", "message": "L'operatore dovrà fornire una nuova password al momento dell'esecuzione."})
            if step.get("mode") == "approval":
                step_statuses.append("needs_operator_input")
                step_gaps.append({"code": "approval_required", "message": "Il passo richiederà approvazione forte dell'operatore."})
            dependencies = list(step.get("depends_on_categories", []))
            if dependencies:
                step_gaps.append({"code": "dependencies", "message": "Dipendenze da soddisfare: " + ", ".join(dependencies) + "."})
            step_results.append({
                "step_id": step.get("id"), "category": category, "mode": step.get("mode"),
                "status": min(step_statuses, key=PRIORITY.__getitem__),
                "depends_on_categories": dependencies, "gaps": step_gaps,
            })
    unsupported_categories = sorted({
        str(step.get("category")) for step in plan_steps
        if step.get("category") not in WRITER_CATEGORIES
        and step.get("category") not in EVIDENCE_CATEGORIES
    })
    for category in unsupported_categories:
        category_steps = [step for step in plan_steps if step.get("category") == category]
        source_status = _coverage(source_data, category)
        destination_status = _coverage(destination_data, category)
        gaps = [{"code": "no_writer_contract", "message": "Non esiste un writer mock/real supportato per questa categoria; il passo resta manuale o escluso."}]
        categories.append({
            "category": category, "status": "not_ready",
            "source_coverage": source_status, "destination_coverage": destination_status,
            "step_count": len(category_steps), "gaps": gaps,
        })
        for step in category_steps:
            dependencies = list(step.get("depends_on_categories", []))
            step_gaps = list(gaps)
            if dependencies:
                step_gaps.append({"code": "dependencies", "message": "Dipendenze da soddisfare: " + ", ".join(dependencies) + "."})
            step_results.append({
                "step_id": step.get("id"), "category": category, "mode": step.get("mode"),
                "status": "not_ready", "depends_on_categories": dependencies, "gaps": step_gaps,
            })
    summary = {status: sum(item["status"] == status for item in categories) for status in PRIORITY}
    summary["categories_total"] = len(categories)
    summary["steps_total"] = len(step_results)
    global_blockers = [{"code": "real_execution_absent", "message": "Non esiste un execution contract reale né un dispatch operativo; tutti i writer restano disabilitati."}]
    return categories, step_results, summary, global_blockers
