"""Tests for the pure Migration Strategy model (domain, no DB/network).

They pin the decision table that maps an access profile + probed capabilities
to a recommended strategy and an honest credential-preservation verdict. The
model must never over-claim: pre-smoke, the strongest verdict is
``possible_requires_smoke``.
"""

from __future__ import annotations

from domain.migration_strategy import (
    AccessProfile,
    CredentialPreservation,
    MigrationStrategy,
    recommend_strategy,
)

# Verdicts that mean "we cannot / will not preserve existing passwords".
_NO_PRESERVATION = {
    CredentialPreservation.UNAVAILABLE.value,
    CredentialPreservation.OPERATOR_SUPPLIED_ONLY.value,
}


def test_token_only_is_api_rebuild_without_preservation() -> None:
    out = recommend_strategy({"access_profile": "token_only"})
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["credential_preservation"] in _NO_PRESERVATION
    assert out["reason"]


def test_token_plus_password_without_restore_destination_is_api_rebuild() -> None:
    out = recommend_strategy(
        {
            "access_profile": "token_plus_cpanel_password",
            "can_generate_full_backup": False,
            "has_whm_reseller": False,
            "can_restore_cpanel_account": False,
        }
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["credential_preservation"] in _NO_PRESERVATION


def test_token_plus_password_with_backup_but_no_restore_is_api_rebuild() -> None:
    out = recommend_strategy(
        {
            "access_profile": "token_plus_cpanel_password",
            "can_generate_full_backup": True,
            "can_remote_backup_ftp": True,
            "can_remote_backup_scp": True,
            "can_skip_homedir": True,
            "has_whm_reseller": False,
            "can_restore_cpanel_account": False,
        }
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["credential_preservation"] in _NO_PRESERVATION
    # The reason must make the destination-side gap explicit, not vague.
    assert "restore" in out["reason"].lower()
    assert "rebuild" in out["reason"].lower()


def test_reseller_with_restore_and_homedir_skip_is_config_clone() -> None:
    out = recommend_strategy(
        {
            "access_profile": "whm_reseller",
            "can_generate_full_backup": True,
            "can_skip_homedir": True,
            "has_whm_reseller": True,
            "can_restore_cpanel_account": True,
        }
    )
    assert (
        out["recommended_strategy"]
        == MigrationStrategy.RESTORE_ASSISTED_CONFIG_CLONE.value
    )
    assert (
        out["credential_preservation"]
        == CredentialPreservation.POSSIBLE_REQUIRES_SMOKE.value
    )


def test_root_whm_is_root_transfer_or_full_restore() -> None:
    out = recommend_strategy({"access_profile": "root_whm"})
    assert out["recommended_strategy"] in {
        MigrationStrategy.ROOT_TRANSFER.value,
        MigrationStrategy.FULL_ACCOUNT_RESTORE.value,
    }
    # Root never downgrades preservation to "impossible"; it is at least
    # possible-pending-smoke.
    assert out["credential_preservation"] not in _NO_PRESERVATION


def test_backup_without_homedir_skip_is_full_restore_not_config_clone() -> None:
    out = recommend_strategy(
        {
            "access_profile": "whm_reseller",
            "can_generate_full_backup": True,
            "can_skip_homedir": False,
            "has_whm_reseller": True,
            "can_restore_cpanel_account": True,
        }
    )
    assert (
        out["recommended_strategy"]
        == MigrationStrategy.FULL_ACCOUNT_RESTORE.value
    )
    assert (
        out["credential_preservation"]
        == CredentialPreservation.POSSIBLE_REQUIRES_SMOKE.value
    )


def test_missing_or_unknown_access_profile_is_unknown_not_supported() -> None:
    for bad in ({}, {"access_profile": None}, {"access_profile": "banana"}, None):
        out = recommend_strategy(bad)  # type: ignore[arg-type]
        assert out["recommended_strategy"] == MigrationStrategy.UNKNOWN.value
        assert (
            out["credential_preservation"]
            == CredentialPreservation.NOT_SUPPORTED.value
        )
        assert out["reason"]


def test_enum_instance_accepted_as_access_profile() -> None:
    out = recommend_strategy({"access_profile": AccessProfile.TOKEN_ONLY})
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value


def test_non_true_truthy_values_do_not_count_as_capability() -> None:
    # A stringy "true" / integer 1 must NOT be read as an enabled capability:
    # the model requires an explicit boolean True so it never over-claims.
    out = recommend_strategy(
        {
            "access_profile": "whm_reseller",
            "can_generate_full_backup": "true",
            "can_skip_homedir": 1,
            "can_restore_cpanel_account": "yes",
        }
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value


def test_result_contract_is_exactly_three_string_keys() -> None:
    out = recommend_strategy({"access_profile": "token_only"})
    assert set(out) == {
        "recommended_strategy",
        "credential_preservation",
        "reason",
    }
    assert all(isinstance(v, str) for v in out.values())


def test_skip_homedir_and_restore_without_backup_is_not_a_restore_path() -> None:
    # A destination that can restore + can_skip_homedir, but NO source backup:
    # you cannot restore an archive you cannot generate → must fall back to
    # api_rebuild, never a restore/config-clone path.
    out = recommend_strategy(
        {
            "access_profile": "whm_reseller",
            "can_generate_full_backup": False,
            "can_skip_homedir": True,
            "has_whm_reseller": True,
            "can_restore_cpanel_account": True,
        }
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["credential_preservation"] in _NO_PRESERVATION


def test_reseller_alone_without_backup_yields_no_restore_path() -> None:
    # has_whm_reseller on its own (no source backup capability) must NOT unlock a
    # restore-based recommendation.
    out = recommend_strategy(
        {
            "access_profile": "whm_reseller",
            "has_whm_reseller": True,
        }
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["credential_preservation"] in _NO_PRESERVATION
