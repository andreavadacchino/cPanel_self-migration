"""Tests for the pure SSH-Assisted Email Identity Clone model (domain).

They pin the decision table for ``recommend_email_identity_strategy`` and its
honesty invariants: the clone preserves EMAIL passwords only, it is refused
without a redaction guarantee, and pre-smoke the strongest verdict is
``possible_requires_smoke`` — never ``confirmed_by_smoke``.
"""

from __future__ import annotations

from domain.migration_strategy import (
    AccessProfile,
    CredentialPreservation,
    MigrationStrategy,
    recommend_email_identity_strategy,
)

# A fully-capable SSH profile: SSH on both sides, source hash readable,
# destination writable both ways, Maildir copy+verify, and redaction guaranteed.
_FULL = {
    "access_profile": "ssh_account_access",
    "can_ssh_source_account": True,
    "can_ssh_destination_account": True,
    "can_read_source_mail_shadow": True,
    "can_read_source_mail_passwd": True,
    "can_create_destination_mailbox_with_password_hash": True,
    "can_update_destination_mail_shadow_hash": True,
    "can_copy_maildir": True,
    "can_verify_maildir": True,
    "can_redact_hashes_everywhere": True,
}

_UNAVAILABLE = CredentialPreservation.UNAVAILABLE.value


def test_token_only_cannot_preserve_email_password() -> None:
    out = recommend_email_identity_strategy(
        {
            "access_profile": "token_only",
            "can_read_source_mail_shadow": False,
            "can_create_destination_mailbox_with_password_hash": False,
            "can_copy_maildir": False,
        }
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE
    assert "shadow" in out["reason"].lower()


def test_ssh_without_reading_shadow_cannot_preserve() -> None:
    out = recommend_email_identity_strategy(
        {
            "access_profile": "ssh_account_access",
            "can_ssh_source_account": True,
            "can_ssh_destination_account": True,
            "can_read_source_mail_shadow": False,
            "can_create_destination_mailbox_with_password_hash": True,
            "can_update_destination_mail_shadow_hash": True,
            "can_copy_maildir": True,
            "can_verify_maildir": True,
            "can_redact_hashes_everywhere": True,
        }
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE


def test_ssh_with_shadow_but_no_destination_write_cannot_preserve() -> None:
    out = recommend_email_identity_strategy(
        {
            "access_profile": "ssh_account_access",
            "can_ssh_source_account": True,
            "can_ssh_destination_account": True,
            "can_read_source_mail_shadow": True,
            "can_create_destination_mailbox_with_password_hash": False,
            "can_update_destination_mail_shadow_hash": False,
            "can_copy_maildir": True,
        }
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE
    assert "destination cannot create or update" in out["reason"].lower()


def test_full_ssh_recommends_email_identity_clone_requires_smoke() -> None:
    out = recommend_email_identity_strategy(_FULL)
    assert (
        out["recommended_strategy"]
        == MigrationStrategy.SSH_ASSISTED_EMAIL_IDENTITY_CLONE.value
    )
    assert (
        out["email_password_preservation"]
        == CredentialPreservation.POSSIBLE_REQUIRES_SMOKE.value
    )


def test_full_ssh_without_redaction_is_refused() -> None:
    caps = {**_FULL, "can_redact_hashes_everywhere": False}
    out = recommend_email_identity_strategy(caps)
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE
    assert "redaction" in out["reason"].lower()


def test_full_ssh_without_maildir_copy_is_refused() -> None:
    # Gate 3: everything writable + redaction, but no Maildir copy.
    out = recommend_email_identity_strategy({**_FULL, "can_copy_maildir": False})
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE


def test_full_ssh_without_maildir_verify_is_refused() -> None:
    # Gate 3: everything writable + redaction, but no Maildir verify.
    out = recommend_email_identity_strategy({**_FULL, "can_verify_maildir": False})
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE


def test_destination_create_without_update_is_refused() -> None:
    # Gate 2 must stay AND, not OR: create alone is not enough.
    out = recommend_email_identity_strategy(
        {**_FULL, "can_update_destination_mail_shadow_hash": False}
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE


def test_destination_update_without_create_is_refused() -> None:
    # Gate 2 must stay AND, not OR: update alone is not enough.
    out = recommend_email_identity_strategy(
        {**_FULL, "can_create_destination_mailbox_with_password_hash": False}
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE


def test_missing_ssh_source_account_is_refused() -> None:
    # SSH access is an explicit prerequisite on BOTH sides — not implied by the
    # shadow/write flags. Source SSH missing → refuse.
    out = recommend_email_identity_strategy(
        {**_FULL, "can_ssh_source_account": False}
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE


def test_missing_ssh_destination_account_is_refused() -> None:
    out = recommend_email_identity_strategy(
        {**_FULL, "can_ssh_destination_account": False}
    )
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE


def test_non_true_truthy_values_do_not_count() -> None:
    # "true"/1 must NOT be read as an enabled capability — the model requires an
    # explicit boolean True so it never over-claims.
    caps = {k: ("true" if isinstance(v, bool) else v) for k, v in _FULL.items()}
    caps["access_profile"] = "ssh_account_access"
    out = recommend_email_identity_strategy(caps)
    assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
    assert out["email_password_preservation"] == _UNAVAILABLE


def test_malformed_input_does_not_crash() -> None:
    for bad in (None, {}, {"access_profile": None}, {"access_profile": "banana"}, []):
        out = recommend_email_identity_strategy(bad)  # type: ignore[arg-type]
        assert out["recommended_strategy"] == MigrationStrategy.API_REBUILD.value
        assert out["email_password_preservation"] == _UNAVAILABLE
        assert out["reason"]


def test_model_never_emits_confirmed_by_smoke() -> None:
    inputs = [
        None,
        {},
        {"access_profile": "token_only"},
        {"access_profile": "ssh_account_access"},
        _FULL,
        {**_FULL, "can_redact_hashes_everywhere": False},
    ]
    for caps in inputs:
        out = recommend_email_identity_strategy(caps)  # type: ignore[arg-type]
        assert (
            out["email_password_preservation"]
            != CredentialPreservation.CONFIRMED_BY_SMOKE.value
        )


def test_success_reason_names_the_email_only_caveat() -> None:
    reason = recommend_email_identity_strategy(_FULL)["reason"].lower()
    # It must state this preserves email only and needs a smoke.
    assert "email" in reason
    assert "smoke" in reason


def test_mysql_and_ftp_are_not_claimed_preserved_by_this_strategy() -> None:
    # The only preservation field is email-scoped, and the success reason must
    # explicitly disclaim FTP and MySQL — this strategy must never be read as
    # preserving those.
    out = recommend_email_identity_strategy(_FULL)
    assert set(out) == {
        "recommended_strategy",
        "email_password_preservation",
        "reason",
    }
    reason = out["reason"].lower()
    assert "ftp" in reason and "mysql" in reason
    assert "not" in reason or "only" in reason


def test_enum_instance_accepted_as_access_profile() -> None:
    out = recommend_email_identity_strategy(
        {**_FULL, "access_profile": AccessProfile.SSH_ACCOUNT_ACCESS}
    )
    assert (
        out["recommended_strategy"]
        == MigrationStrategy.SSH_ASSISTED_EMAIL_IDENTITY_CLONE.value
    )
