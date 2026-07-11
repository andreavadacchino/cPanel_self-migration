import httpx

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.schemas import CpanelCredentials


def test_ping_accepts_modern_flat_uapi_response(monkeypatch) -> None:
    def fake_get(*args, **kwargs):
        return httpx.Response(200, json={"status": 1, "data": {"user": "account"}, "errors": None}, request=httpx.Request("GET", args[0]))

    monkeypatch.setattr(httpx, "get", fake_get)
    client = CpanelClient(CpanelCredentials(host="cpanel.test", username="account", api_token="secret"))
    assert client.ping()["data"]["user"] == "account"


def test_ping_accepts_wrapped_uapi_response(monkeypatch) -> None:
    def fake_get(*args, **kwargs):
        return httpx.Response(200, json={"result": {"status": 1, "data": {"user": "account"}}}, request=httpx.Request("GET", args[0]))

    monkeypatch.setattr(httpx, "get", fake_get)
    client = CpanelClient(CpanelCredentials(host="cpanel.test", username="account", api_token="secret"))
    assert client.ping()["result"]["status"] == 1


def test_api2_accepts_successful_event(monkeypatch) -> None:
    def fake_get(*args, **kwargs):
        return httpx.Response(200, json={"cpanelresult": {"event": {"result": 1}, "data": [{"command": "echo ok"}]}}, request=httpx.Request("GET", args[0]))

    monkeypatch.setattr(httpx, "get", fake_get)
    client = CpanelClient(CpanelCredentials(host="cpanel.test", username="account", api_token="secret"))
    assert client.api2("Cron", "listcron")["cpanelresult"]["data"][0]["command"] == "echo ok"
