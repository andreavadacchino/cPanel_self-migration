from app.modules.comparison.engine import compare
from app.modules.inventory.collector import collect


class FakeClient:
    def __init__(self, *, fail_detail: bool = False) -> None:
        self.fail_detail = fail_detail
        self.calls: list[tuple[str, str, dict | None]] = []

    def execute(self, module: str, function: str, params: dict | None = None) -> dict:
        self.calls.append((module, function, params))
        if (module, function) == ("DomainInfo", "list_domains"):
            return {"result": {"status": 1, "data": {"main_domain": "example.test", "addon_domains": [], "sub_domains": [], "parked_domains": []}}}
        if (module, function) == ("Email", "list_auto_responders"):
            assert params == {"domain": "example.test"}
            return {"result": {"status": 1, "data": [{"email": "away@example.test", "subject": "Assente"}]}}
        if (module, function) == ("Email", "get_auto_responder"):
            assert params == {"email": "away@example.test"}
            if self.fail_detail:
                raise RuntimeError("detail unavailable")
            return {"result": {"status": 1, "data": {"body": "Torno presto", "from": "Support", "subject": "Assente", "interval": 8, "is_html": 0, "charset": "utf-8", "start": None, "stop": None}}}
        if (module, function) == ("DNS", "parse_zone"):
            return {"result": {"status": 1, "data": []}}
        if (module, function) == ("Email", "list_filters"):
            return {"result": {"status": 1, "data": []}}
        return {"result": {"status": 1, "data": []}}

    def api2(self, module: str, function: str, params: dict | None = None) -> dict:
        return {"cpanelresult": {"event": {"result": 1}, "data": []}}


def test_autoresponder_inventory_collects_full_detail_per_domain() -> None:
    data, _ = collect(FakeClient())  # type: ignore[arg-type]
    coverage = data["coverage"]["email_autoresponders"]
    responder = data["email_autoresponders"][0]
    assert coverage["status"] == "succeeded"
    assert responder["body"] == "Torno presto"
    assert responder["from"] == "Support"
    assert responder["interval"] == 8
    assert responder["_detail_status"] == "succeeded"


def test_autoresponder_detail_failure_is_partial_never_empty() -> None:
    data, _ = collect(FakeClient(fail_detail=True))  # type: ignore[arg-type]
    coverage = data["coverage"]["email_autoresponders"]
    assert coverage["status"] == "partial"
    assert coverage["items_count"] == 1
    assert data["email_autoresponders"][0]["_detail_status"] == "failed"


def test_autoresponder_comparison_includes_body_and_schedule() -> None:
    coverage = {"email_autoresponders": {"status": "succeeded"}}
    base = {"email": "away@example.test", "subject": "Assente", "body": "Uno", "from": "Support", "interval": 8, "is_html": 0, "charset": "utf-8", "start": None, "stop": None, "_detail_status": "succeeded"}
    entries, _ = compare({"coverage": coverage, "email_autoresponders": [base]}, {"coverage": coverage, "email_autoresponders": [{**base, "body": "Due"}]})
    assert entries[0]["state"] == "different"
