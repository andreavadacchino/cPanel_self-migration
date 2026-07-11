from app.modules.inventory.collector import collect
from app.modules.readiness.engine import build_report
from app.modules.comparison.engine import compare


class MetadataClient:
    def __init__(self, *, fail_grant: bool = False, private: object = 1, complete_ftp: bool = True, legacy_private: object = "missing") -> None:
        self.fail_grant = fail_grant
        self.private = private
        self.complete_ftp = complete_ftp
        self.legacy_private = legacy_private

    def execute(self, module: str, function: str, params: dict | None = None) -> dict:
        values: dict[tuple[str, str], object] = {
            ("DomainInfo", "list_domains"): {"main_domain": "example.test", "addon_domains": [], "sub_domains": [], "parked_domains": []},
            ("Mysql", "list_databases"): [{"database": "acct_app"}],
            ("Mysql", "list_users"): [{"user": "acct_user"}],
            ("Mysql", "get_restrictions"): {"prefix": "acct_", "max_database_name_length": 64, "max_username_length": 32},
            ("Variables", "get_user_information"): {"maximum_databases": 10},
            ("Ftp", "list_ftp_with_disk"): [{"login": "deploy@example.test", "accttype": "sub", **({"diskquota": 250, "homedir": "/home/acct/site"} if self.complete_ftp else {})}],
            ("Email", "list_lists"): [{"list": "team", "domain": "example.test", **({"private": self.private} if self.private != "missing" else {})}],
            ("DNS", "parse_zone"): [],
            ("Email", "list_auto_responders"): [],
            ("Email", "list_filters"): [],
        }
        if (module, function) == ("Mysql", "get_privileges_on_database"):
            assert params == {"database": "acct_app", "user": "acct_user"}
            if self.fail_grant:
                raise RuntimeError("not readable")
            return {"result": {"status": 1, "data": ["SELECT", {"privilege": "INSERT"}]}}
        return {"result": {"status": 1, "data": values.get((module, function), [])}}

    def api2(self, module: str, function: str, params: dict | None = None) -> dict:
        data = []
        if (module, function) == ("Email", "listlists") and self.legacy_private != "missing":
            if self.legacy_private in {"private", "public"}:
                data = [{"list": "team@example.test", "archive_private": 1 if self.legacy_private == "private" else 0, "advertised": 0 if self.legacy_private == "private" else 1, "subscribe_policy": 2 if self.legacy_private == "private" else 1}]
            else:
                data = [{"list": "team@example.test", "listtype": self.legacy_private}]
        return {"cpanelresult": {"event": {"result": 1}, "data": data}}


def test_collects_mysql_grants_ftp_home_quota_and_mailing_privacy() -> None:
    data, _ = collect(MetadataClient())  # type: ignore[arg-type]
    assert data["mysql_grants"] == [{"database": "acct_app", "user": "acct_user", "privileges": ["INSERT", "SELECT"]}]
    assert data["coverage"]["mysql_grants"]["status"] == "succeeded"
    assert data["coverage"]["mysql_grants"]["pairs_checked"] == 1
    assert data["coverage"]["mysql_grant_contract"]["status"] == "succeeded"
    assert data["coverage"]["database_contract"]["status"] == "succeeded"
    assert data["database_contract"]["quota"] == {"maximum": 10, "current": 1, "known": True}
    assert data["ftp_accounts"][0]["_writer_metadata_status"] == "succeeded"
    assert data["coverage"]["ftp_accounts"]["status"] == "succeeded"
    assert data["mailing_lists"][0]["_privacy_status"] == "succeeded"

    categories, _, _, _ = build_report([], data, data)
    by_category = {item["category"]: item for item in categories}
    assert by_category["mysql_users"]["status"] == "eligible_for_real_design"
    assert by_category["databases"]["status"] == "eligible_for_real_design"
    assert by_category["ftp_accounts"]["status"] == "needs_contract_test"
    assert by_category["mailing_lists"]["status"] == "needs_contract_test"


def test_detail_failures_are_partial_and_never_empty() -> None:
    data, _ = collect(MetadataClient(fail_grant=True, private="missing", complete_ftp=False))  # type: ignore[arg-type]
    assert data["coverage"]["mysql_grants"]["status"] == "unavailable"
    assert data["coverage"]["mysql_grant_contract"]["status"] == "unavailable"
    assert data["coverage"]["mysql_grants"]["items_count"] is None
    assert data["coverage"]["ftp_accounts"]["status"] == "partial"
    assert data["coverage"]["mailing_lists"]["status"] == "partial"
    assert data["ftp_accounts"][0]["_writer_metadata_status"] == "failed"
    assert data["mailing_lists"][0]["_privacy_status"] == "failed"


def test_mailing_listtype_is_normalized_without_inventing_privacy() -> None:
    client = MetadataClient(private="missing")
    original = client.execute
    def execute(module: str, function: str, params: dict | None = None) -> dict:
        if (module, function) == ("Email", "list_lists"):
            return {"result": {"status": 1, "data": [{"list": "team", "domain": "example.test", "listtype": "private"}]}}
        return original(module, function, params)
    client.execute = execute  # type: ignore[method-assign]
    data, _ = collect(client)  # type: ignore[arg-type]
    assert data["mailing_lists"][0]["private"] == 1
    assert data["mailing_lists"][0]["_privacy_status"] == "succeeded"


def test_mailing_privacy_uses_read_only_api2_fallback() -> None:
    data, _ = collect(MetadataClient(private="missing", legacy_private="private"))  # type: ignore[arg-type]
    item = data["mailing_lists"][0]
    assert item["private"] == 1
    assert item["_privacy_status"] == "succeeded"
    assert item["_privacy_source"] == "api2"
    assert data["coverage"]["mailing_lists"]["status"] == "succeeded"


def test_mailing_privacy_api2_composite_public_case() -> None:
    data, _ = collect(MetadataClient(private="missing", legacy_private="public"))  # type: ignore[arg-type]
    assert data["mailing_lists"][0]["private"] == 0
    assert data["mailing_lists"][0]["_privacy_source"] == "api2"


def test_comparison_includes_grants_and_ftp_writer_metadata() -> None:
    source, _ = collect(MetadataClient())  # type: ignore[arg-type]
    destination, _ = collect(MetadataClient())  # type: ignore[arg-type]
    destination["mysql_grants"][0]["privileges"] = ["SELECT"]
    destination["ftp_accounts"][0]["homedir"] = "/home/acct/other"
    entries, _ = compare(source, destination)
    states = {(item["category"], item["key"]): item["state"] for item in entries}
    assert states[("mysql_grants", "acct_user@acct_app")] == "different"
    assert states[("ftp_accounts", "deploy@example.test")] == "different"
