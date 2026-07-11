import base64

from app.modules.inventory.collector import collect
from app.modules.readiness.engine import build_report


def b64(value: str) -> str:
    return base64.b64encode(value.encode()).decode()


class DnsClient:
    def __init__(self, records: list[dict] | None = None) -> None:
        self.zones: list[str] = []
        self.records = records or []

    def execute(self, module: str, function: str, params: dict | None = None) -> dict:
        if (module, function) == ("DomainInfo", "list_domains"):
            data: object = {
                "main_domain": "example.test",
                "addon_domains": ["addon.test"],
                "sub_domains": ["app.example.test", "dev.addon.test"],
                "parked_domains": ["alias.test"],
            }
        elif (module, function) == ("DNS", "parse_zone"):
            assert params is not None
            self.zones.append(params["zone"])
            data = self.records if params["zone"] == "example.test" else []
        else:
            data = []
        return {"result": {"status": 1, "data": data}}

    def api2(self, module: str, function: str, params: dict | None = None) -> dict:
        return {"cpanelresult": {"event": {"result": 1}, "data": []}}


def test_dns_queries_zone_owners_not_every_subdomain() -> None:
    client = DnsClient()
    data, _ = collect(client)  # type: ignore[arg-type]
    assert client.zones == ["addon.test", "alias.test", "example.test"]
    assert "app.example.test" not in client.zones
    assert data["coverage"]["dns_records"]["status"] == "empty"
    assert data["coverage"]["dns_contract"]["status"] == "succeeded"
    assert data["dns_contract"]["expected_zones"] == ["addon.test", "alias.test", "example.test"]
    assert data["dns_contract"]["fresh_read_strategy"] == "parse_zone_per_owned_zone"
    steps = [{"id": "dns_records:new.example.test|A", "key": "new.example.test|A", "category": "dns_records", "mode": "approval", "state": "pending", "comparison_state": "missing_on_destination"}]
    categories, step_results, _, _ = build_report(steps, data, data)
    assert next(item for item in categories if item["category"] == "dns_records")["status"] == "eligible_for_real_design"
    assert step_results[0]["status"] == "needs_operator_input"
    assert not any(gap["code"] == "dns_not_additive" for gap in step_results[0]["gaps"])


def test_dns_contract_detects_ambiguous_and_unsupported_records() -> None:
    a_record = {"type": "record", "record_type": "A", "dname_b64": b64("www.example.test"), "data_b64": [b64("192.0.2.10")], "ttl": 3600}
    records = [a_record, {**a_record, "data_b64": [b64("192.0.2.11")]}, {"type": "record", "record_type": "NAPTR", "dname_b64": b64("sip.example.test"), "data_b64": [b64("value")], "ttl": 3600}]
    data, _ = collect(DnsClient(records))  # type: ignore[arg-type]
    contract = data["dns_contract"]
    assert contract["collision_keys"] == ["www.example.test|A"]
    assert contract["unsupported_keys"] == ["sip.example.test|NAPTR"]

    steps = [
        {"id": "dns_records:www.example.test|A", "key": "www.example.test|A", "category": "dns_records", "mode": "approval", "state": "pending", "comparison_state": "missing_on_destination"},
        {"id": "dns_records:sip.example.test|NAPTR", "key": "sip.example.test|NAPTR", "category": "dns_records", "mode": "approval", "state": "pending", "comparison_state": "different"},
    ]
    _, step_results, _, _ = build_report(steps, data, data)
    by_id = {item["step_id"]: item for item in step_results}
    assert by_id["dns_records:www.example.test|A"]["status"] == "not_ready"
    assert any(gap["code"] == "dns_ambiguous_identity" for gap in by_id["dns_records:www.example.test|A"]["gaps"])
    assert by_id["dns_records:sip.example.test|NAPTR"]["status"] == "not_ready"
    assert {gap["code"] for gap in by_id["dns_records:sip.example.test|NAPTR"]["gaps"]} >= {"dns_not_additive", "dns_type_unsupported"}
