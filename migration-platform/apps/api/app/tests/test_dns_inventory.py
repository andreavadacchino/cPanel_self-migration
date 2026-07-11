from app.modules.inventory.collector import collect


class DnsClient:
    def __init__(self) -> None:
        self.zones: list[str] = []

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
            data = []
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
