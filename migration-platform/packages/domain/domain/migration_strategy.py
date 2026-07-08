"""Pure Migration Strategy / Credential Preservation model.

Given a flat, migration-level *capability* view (what we can do on the source
and what the destination can accept), recommend which migration strategy is
viable and whether existing end-user credentials (email/FTP/MySQL passwords)
can be preserved **without ever knowing them in plaintext**.

Grounding fact this model encodes (see ``docs/CONFIG_CLONE_FEASIBILITY.md``):

* An **API rebuild** re-creates accounts through the official cPanel API. It can
  never carry an existing password — every account must be given a *new*,
  operator-supplied secret. So an API rebuild preserves nothing.
* The **only** way a password can travel without being known in plaintext is
  inside an official cPanel **backup/restore** of the account archive. That
  requires *both* a source that can generate the backup *and* a destination that
  can restore a cPanel account archive (reseller or root). Generating a backup
  is necessary but not sufficient: a backup nobody can restore is useless for
  credential preservation.

The model therefore never claims "passwords are preserved". Pre-smoke, the best
it will ever say is *possible, requires a real smoke test*. Only an executed
smoke may promote a category to ``confirmed_by_smoke`` — and that promotion is
**not** done here.

A second, narrower path is also modelled here — the **SSH-Assisted Email
Identity Clone** (see ``docs/SSH_EMAIL_IDENTITY_CLONE_FEASIBILITY.md``). Unlike
the backup/restore path, it preserves **email passwords only**, by reading the
mail password *hash* from the source account filesystem
(``~/etc/<domain>/shadow``) and re-applying it on the destination mailbox — it
never learns the plaintext, and it does not touch FTP/MySQL/API-token/WebDisk
credentials. Its entry point is :func:`recommend_email_identity_strategy`.
Because the hash is itself sensitive material, that path is refused unless the
caller can guarantee the hash is redacted everywhere (never persisted, logged or
exposed).

Infrastructure-free: no DB, no network, no FastAPI, no cPanel, no secrets. The
public entry points are the pure functions :func:`recommend_strategy` and
:func:`recommend_email_identity_strategy`.
"""

from __future__ import annotations

from enum import Enum


class AccessProfile(str, Enum):
    """The strongest access level we hold for the migration as a whole."""

    TOKEN_ONLY = "token_only"
    TOKEN_PLUS_CPANEL_PASSWORD = "token_plus_cpanel_password"
    WHM_RESELLER = "whm_reseller"
    ROOT_WHM = "root_whm"
    # Account-level shell/filesystem access (SSH as the cPanel user). Not a rung
    # on the backup/restore power ladder — a distinct axis that can read the mail
    # ~/etc/<domain>/{passwd,shadow} the token/UAPI layer never sees.
    SSH_ACCOUNT_ACCESS = "ssh_account_access"


class MigrationStrategy(str, Enum):
    API_REBUILD = "api_rebuild"
    RESTORE_ASSISTED_CONFIG_CLONE = "restore_assisted_config_clone"
    FULL_ACCOUNT_RESTORE = "full_account_restore"
    ROOT_TRANSFER = "root_transfer"
    # Account-level SSH path that preserves EMAIL passwords only, by cloning the
    # mail password hash from ~/etc/<domain>/shadow (never the plaintext).
    SSH_ASSISTED_EMAIL_IDENTITY_CLONE = "ssh_assisted_email_identity_clone"
    # Reserved forward-looking vocabulary (documented, not emitted by
    # recommend_strategy in this spike): a mix of restore for config + a
    # separate homedir/data move.
    HYBRID = "hybrid"
    # Returned when the input is missing/unrecognized — never a crash.
    UNKNOWN = "unknown"


class CredentialPreservation(str, Enum):
    # No mechanism to preserve credentials on this path.
    UNAVAILABLE = "unavailable"
    # Credentials can only be set as new values supplied by the operator.
    OPERATOR_SUPPLIED_ONLY = "operator_supplied_only"
    # Reserved: preservation is possible in principle but only via a restore
    # operation (category-level claim; set by the future plan integration).
    POSSIBLE_REQUIRES_RESTORE = "possible_requires_restore"
    # A restore-based path exists but is unproven: needs a real smoke test.
    POSSIBLE_REQUIRES_SMOKE = "possible_requires_smoke"
    # Reserved: only an executed smoke may promote a category to this value.
    CONFIRMED_BY_SMOKE = "confirmed_by_smoke"
    # The strategy fundamentally cannot preserve credentials / unknown input.
    NOT_SUPPORTED = "not_supported"


_PROFILE_BY_VALUE = {p.value: p for p in AccessProfile}


def _coerce_profile(value: object) -> AccessProfile | None:
    """Accept an ``AccessProfile`` or its string value; anything else → None."""
    if isinstance(value, AccessProfile):
        return value
    if isinstance(value, str):
        return _PROFILE_BY_VALUE.get(value.strip())
    return None


def _result(
    strategy: MigrationStrategy,
    preservation: CredentialPreservation,
    reason: str,
) -> dict:
    """The stable 3-key contract returned by :func:`recommend_strategy`."""
    return {
        "recommended_strategy": strategy.value,
        "credential_preservation": preservation.value,
        "reason": reason,
    }


def recommend_strategy(capabilities: dict) -> dict:
    """Recommend a migration strategy + credential-preservation verdict.

    ``capabilities`` is a flat, migration-level dict. Recognized keys (all
    optional; anything missing or not exactly ``True`` is treated as "not
    available", never guessed):

    * ``access_profile``            — one of :class:`AccessProfile` values
    * ``can_generate_full_backup``  — source can produce an official backup
    * ``can_remote_backup_ftp``     — source can ship the backup over FTP
    * ``can_remote_backup_scp``     — source can ship the backup over SCP
    * ``can_skip_homedir``          — source can omit the homedir from the backup
    * ``has_whm_reseller``          — a reseller WHM is available on destination
    * ``can_restore_cpanel_account``— destination can restore a cPanel archive

    Returns ``{"recommended_strategy", "credential_preservation", "reason"}``
    with plain string values. Never raises on malformed input.
    """
    caps = capabilities if isinstance(capabilities, dict) else {}
    profile = _coerce_profile(caps.get("access_profile"))

    if profile is None:
        return _result(
            MigrationStrategy.UNKNOWN,
            CredentialPreservation.NOT_SUPPORTED,
            "Access profile is missing or unrecognized: cannot recommend a "
            "strategy without knowing the level of access.",
        )

    def flag(key: str) -> bool:
        # Strict: only an explicit True counts. None/missing/False/"true"/1 are
        # all treated as "not available" so the recommendation never over-claims.
        return caps.get(key) is True

    source_can_backup = flag("can_generate_full_backup")
    can_skip_homedir = flag("can_skip_homedir")
    destination_can_restore = (
        flag("can_restore_cpanel_account")
        or flag("has_whm_reseller")
        or profile == AccessProfile.ROOT_WHM
    )

    # Root/WHM on the destination unlocks the most credential-complete path: a
    # full server-to-server account transfer / restore that carries the whole
    # account archive. Preservation is plausible but must be proven by smoke.
    if profile == AccessProfile.ROOT_WHM:
        return _result(
            MigrationStrategy.ROOT_TRANSFER,
            CredentialPreservation.POSSIBLE_REQUIRES_SMOKE,
            "Root/WHM access enables a full account transfer/restore that "
            "carries the account archive (email/FTP/MySQL credentials). "
            "Preservation still requires a real smoke test to confirm.",
        )

    # Restore-based paths need BOTH a source that can generate the backup AND a
    # destination that can restore a cPanel account archive.
    if source_can_backup and destination_can_restore:
        if can_skip_homedir:
            return _result(
                MigrationStrategy.RESTORE_ASSISTED_CONFIG_CLONE,
                CredentialPreservation.POSSIBLE_REQUIRES_SMOKE,
                "Source can generate a backup with homedir skipped and the "
                "destination has reseller restore capability: config clone is "
                "potentially possible, to be confirmed by a real smoke test. "
                "Caveat: skipping the homedir also drops the mail password "
                "hashes (~/etc/<domain>/shadow), so email credentials are NOT "
                "preserved on this path unless the mail config is carried "
                "separately — see docs/CONFIG_CLONE_FEASIBILITY.md.",
            )
        return _result(
            MigrationStrategy.FULL_ACCOUNT_RESTORE,
            CredentialPreservation.POSSIBLE_REQUIRES_SMOKE,
            "Source can generate a backup and the destination can restore it, "
            "but homedir cannot be skipped: only a full account restore is "
            "available (no config clone). Preservation is plausible but "
            "requires a smoke test and room for the full-size archive.",
        )

    # No restore path on the destination (or the source cannot back up at all)
    # → API rebuild, which can never preserve an existing password.
    if profile == AccessProfile.TOKEN_ONLY:
        return _result(
            MigrationStrategy.API_REBUILD,
            CredentialPreservation.UNAVAILABLE,
            "Only a cPanel API token is available: no source password material "
            "and the destination cannot restore archives. API rebuild cannot "
            "preserve credentials.",
        )

    if source_can_backup and not destination_can_restore:
        reason = (
            "Source can generate backups but the destination cannot restore "
            "cPanel archives: the only path is API rebuild, which does not "
            "preserve credentials (they must be reset by the operator)."
        )
    else:
        reason = (
            "The destination cannot restore cPanel archives: proceeding with "
            "API rebuild. Credentials cannot be preserved and must be supplied "
            "by the operator."
        )
    return _result(
        MigrationStrategy.API_REBUILD,
        CredentialPreservation.OPERATOR_SUPPLIED_ONLY,
        reason,
    )


def _email_result(
    strategy: MigrationStrategy,
    preservation: CredentialPreservation,
    reason: str,
) -> dict:
    """The stable 3-key contract for :func:`recommend_email_identity_strategy`.

    Note the key is ``email_password_preservation`` (scoped to email), distinct
    from ``credential_preservation`` in :func:`recommend_strategy`, so a caller
    can never mistake an email-only verdict for an all-credentials one.
    """
    return {
        "recommended_strategy": strategy.value,
        "email_password_preservation": preservation.value,
        "reason": reason,
    }


def recommend_email_identity_strategy(capabilities: dict) -> dict:
    """Recommend whether the SSH-Assisted Email Identity Clone is viable.

    This models the legacy Go engine's mail path: read the mail password *hash*
    from ``~/etc/<domain>/shadow`` on the source (never the plaintext) and
    re-apply it on the destination mailbox (``Email::add_pop password_hash=…``
    for a new mailbox, or an atomic shadow-hash rewrite for an existing one),
    then copy and verify the Maildir. It preserves **email passwords only**.

    ``capabilities`` is a flat dict. Recognized keys (all optional; anything not
    exactly ``True`` is treated as "not available"):

    * ``access_profile``  — must be ``ssh_account_access`` for a clone
    * ``can_read_source_mail_shadow``  — read the hash source (hard requirement)
    * ``can_read_source_mail_passwd``  — enumerate the source mailboxes
    * ``can_create_destination_mailbox_with_password_hash``
    * ``can_update_destination_mail_shadow_hash``
    * ``can_copy_maildir`` / ``can_verify_maildir``
    * ``can_redact_hashes_everywhere``  — security gate (failure-closed)

    Returns ``{"recommended_strategy", "email_password_preservation", "reason"}``
    with plain string values. It preserves EMAIL passwords only and never claims
    ``confirmed_by_smoke``. Never raises on malformed input.
    """
    caps = capabilities if isinstance(capabilities, dict) else {}
    profile = _coerce_profile(caps.get("access_profile"))

    def flag(key: str) -> bool:
        return caps.get(key) is True

    # Gate 1 — need account-level access that can read the source mail shadow.
    if profile != AccessProfile.SSH_ACCOUNT_ACCESS or not flag(
        "can_read_source_mail_shadow"
    ):
        if profile == AccessProfile.TOKEN_ONLY:
            reason = "Token-only access cannot read source mail shadow hashes."
        elif profile is None:
            reason = (
                "Access profile is missing or unrecognized: cannot read source "
                "mail shadow hashes."
            )
        elif profile != AccessProfile.SSH_ACCOUNT_ACCESS:
            reason = (
                f"{profile.value} access does not read the account filesystem; "
                "source mail shadow hashes are not readable."
            )
        else:
            reason = (
                "Source mail shadow hashes are not readable with the current "
                "access."
            )
        return _email_result(
            MigrationStrategy.API_REBUILD, CredentialPreservation.UNAVAILABLE, reason
        )

    # Gate 2 — destination must be able to BOTH create a new mailbox from a hash
    # AND rewrite an existing mailbox's shadow hash (the legacy engine needs both).
    if not (
        flag("can_create_destination_mailbox_with_password_hash")
        and flag("can_update_destination_mail_shadow_hash")
    ):
        return _email_result(
            MigrationStrategy.API_REBUILD,
            CredentialPreservation.UNAVAILABLE,
            "Source hash is readable, but destination cannot create or update "
            "mailboxes with password hashes.",
        )

    # Gate 3 — a *verified* identity clone also needs the Maildir copy + verify.
    if not (flag("can_copy_maildir") and flag("can_verify_maildir")):
        return _email_result(
            MigrationStrategy.API_REBUILD,
            CredentialPreservation.UNAVAILABLE,
            "Mailbox identity is writable but Maildir copy/verify is not "
            "available; cannot perform a verified email identity clone.",
        )

    # Gate 4 (security, failure-closed) — the hash is sensitive material. Refuse
    # a hash-preserving migration unless the caller can guarantee redaction.
    if not flag("can_redact_hashes_everywhere"):
        return _email_result(
            MigrationStrategy.API_REBUILD,
            CredentialPreservation.UNAVAILABLE,
            "Email hashes are sensitive; hash-preserving migration is refused "
            "until redaction guarantees are available.",
        )

    # All gates pass → the clone is viable, pending a real smoke test. This
    # preserves EMAIL passwords ONLY and never claims it is confirmed.
    return _email_result(
        MigrationStrategy.SSH_ASSISTED_EMAIL_IDENTITY_CLONE,
        CredentialPreservation.POSSIBLE_REQUIRES_SMOKE,
        "Source mail hashes are readable and destination can create/update "
        "mailbox identity using hashes; Maildir copy and verify are available. "
        "Hashes must remain transient and redacted. Preserves EMAIL passwords "
        "only — NOT FTP, MySQL, API-token or WebDisk credentials — and requires "
        "a real smoke test to confirm.",
    )
