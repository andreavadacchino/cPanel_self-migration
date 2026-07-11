from app.modules.plans.engine import build_steps


def entry(category: str, key: str, state: str = "missing_on_destination", severity: str = "blocker") -> dict:
    return {"category": category, "key": key, "state": state, "severity": severity, "title": f"{category}: {key}"}


def test_plan_classifies_and_deduplicates_ftp_subaccount() -> None:
    steps, summary = build_steps([
        entry("domains", "demo.example.test"),
        entry("ftp_accounts", "demoftp@example.test"),
        entry("subaccounts", "demoftp"),
        entry("email_accounts", "mailbox@example.test", state="different", severity="warning"),
        entry("subaccounts", "mailbox", state="different", severity="warning"),
        entry("subaccounts", "account_logs", state="different", severity="warning"),
        entry("cron_jobs", "* * * * *|echo ok"),
        entry("php_settings", "demo.example.test"),
    ])
    by_id = {step["id"]: step for step in steps}
    assert by_id["domains:demo.example.test"]["mode"] == "automatic"
    assert by_id["ftp_accounts:demoftp@example.test"]["mode"] == "secret_required"
    assert by_id["subaccounts:demoftp"]["mode"] == "excluded"
    assert by_id["subaccounts:mailbox"]["mode"] == "excluded"
    assert by_id["subaccounts:account_logs"]["mode"] == "excluded"
    assert by_id["cron_jobs:* * * * *|echo ok"]["mode"] == "approval"
    assert by_id["php_settings:demo.example.test"]["mode"] == "manual"
    assert by_id["php_settings:demo.example.test"]["depends_on_categories"] == ["domains"]
    assert summary["excluded"] == 3
