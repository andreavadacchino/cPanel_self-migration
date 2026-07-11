from fastapi.testclient import TestClient


def test_mock_preflight_persists_two_explicit_inventories(client: TestClient) -> None:
    migration = client.post("/api/migrations", json={"name": "Preflight", "domain": "example.test"}).json()
    for role in ("source", "destination"):
        endpoint = client.post(
            f"/api/migrations/{migration['id']}/endpoints",
            json={"role": role, "host": f"{role}.test", "username": "account", "auth_type": "mock"},
        ).json()
        connected = client.post(f"/api/endpoints/{endpoint['id']}/test-connection")
        assert connected.json()["connection_status"] == "connected"

    started = client.post(f"/api/migrations/{migration['id']}/preflight")
    assert started.status_code == 200
    assert started.json()["status"] == "succeeded"

    inventory = client.get(f"/api/migrations/{migration['id']}/inventory").json()
    assert inventory["source"]["status"] == "succeeded"
    assert inventory["destination"]["status"] == "succeeded"
    domains = inventory["source"]["data"]["coverage"]["domains"]
    assert domains["status"] == "empty"
    assert domains["items_count"] == 0
    assert domains["read_only_verified"] is True
    assert inventory["source"]["data"]["coverage"]["redirects"]["status"] == "empty"

    events = client.get(f"/api/migrations/{migration['id']}/events").json()
    assert events[-1]["message"] == "Preflight completato"


def test_preflight_requires_connected_source_and_destination(client: TestClient) -> None:
    migration = client.post("/api/migrations", json={"name": "Blocked", "domain": "example.test"}).json()
    response = client.post(f"/api/migrations/{migration['id']}/preflight")
    assert response.status_code == 409


def test_equal_mock_inventories_do_not_create_manual_tasks(client: TestClient) -> None:
    migration = client.post("/api/migrations", json={"name": "Tasks", "domain": "example.test"}).json()
    for role in ("source", "destination"):
        endpoint = client.post(
            f"/api/migrations/{migration['id']}/endpoints",
            json={"role": role, "host": f"{role}.test", "username": "account", "auth_type": "mock"},
        ).json()
        client.post(f"/api/endpoints/{endpoint['id']}/test-connection")
    client.post(f"/api/migrations/{migration['id']}/preflight")
    assert client.post(f"/api/migrations/{migration['id']}/comparison").status_code == 200
    tasks = client.get(f"/api/migrations/{migration['id']}/manual-tasks").json()
    assert tasks == []
