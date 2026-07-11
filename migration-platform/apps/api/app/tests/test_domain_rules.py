"""Unit tests for the pure domain safety rules (B3a)."""

from __future__ import annotations

import pytest

from adapters.cpanel.domains import DomainRecord, DomainType
from app.modules.executions.domain_rules import (
    AdditiveAction,
    DomainRuleError,
    RequestedDomain,
    decide_additive,
    normalize_domain,
    validate_docroot,
)

HOME = "/home/u"


def _addon(name: str, docroot: str, label: str | None = None) -> DomainRecord:
    return DomainRecord(name=name, type=DomainType.addon, docroot=docroot, internal_label=label)


# -- normalization ----------------------------------------------------------


def test_normalize_folds_case_and_trailing_dot() -> None:
    assert normalize_domain("Example.TEST.") == "example.test"


def test_normalize_idna() -> None:
    assert normalize_domain("exämple.test") == "xn--exmple-cua.test"


def test_normalize_passthrough_punycode() -> None:
    assert normalize_domain("xn--exmple-cua.test") == "xn--exmple-cua.test"


@pytest.mark.parametrize("bad", [
    "", "  ", "a..b.test", "a/b.test", "space domain.test", "a@b.test",
    "bad!domain.test", "under_score.test", "nodot", "-lead.test", "trail-.test",
])
def test_normalize_rejects_invalid(bad: str) -> None:
    with pytest.raises(DomainRuleError):
        normalize_domain(bad)


# -- docroot validation -----------------------------------------------------


def test_docroot_inside_home_is_accepted() -> None:
    assert validate_docroot("/home/u/public_html/site", HOME) == "/home/u/public_html/site"


@pytest.mark.parametrize("bad", [
    "/home/u/../v/site",      # traversal
    "/home/other/site",       # foreign home
    "relative/path",          # not absolute
    "/home/u/\\evil",         # backslash
    "/home/u/~root",          # tilde
    "",                        # empty
])
def test_unsafe_docroot_is_blocked(bad: str) -> None:
    with pytest.raises(DomainRuleError):
        validate_docroot(bad, HOME)


# -- additive decision ------------------------------------------------------


def test_missing_domain_is_safe_to_create_with_compensation() -> None:
    requested = RequestedDomain("new.test", DomainType.addon, "/home/u/new", "new")
    decision = decide_additive(requested, [_addon("addon.test", "/home/u/addon", "addon")], HOME)
    assert decision.action is AdditiveAction.create
    assert decision.normalized_name == "new.test"
    assert decision.compensation is not None
    assert decision.compensation["reverse"] == "manual_removal_only"
    assert "tok" not in repr(decision.compensation)


def test_equivalent_present_is_idempotent_no_op() -> None:
    fresh = [_addon("new.test", "/home/u/new", "new")]
    requested = RequestedDomain("New.Test.", DomainType.addon, "/home/u/new", "new")
    decision = decide_additive(requested, fresh, HOME)
    assert decision.action is AdditiveAction.already_present


def test_present_with_different_type_is_blocked() -> None:
    fresh = [DomainRecord("new.test", DomainType.alias, None)]
    requested = RequestedDomain("new.test", DomainType.addon, "/home/u/new")
    assert decide_additive(requested, fresh, HOME).action is AdditiveAction.blocked


def test_present_with_different_docroot_is_blocked() -> None:
    fresh = [_addon("new.test", "/home/u/other")]
    requested = RequestedDomain("new.test", DomainType.addon, "/home/u/new")
    assert decide_additive(requested, fresh, HOME).action is AdditiveAction.blocked


def test_internal_label_collision_is_blocked() -> None:
    fresh = [_addon("existing.test", "/home/u/existing", "shared")]
    requested = RequestedDomain("new.test", DomainType.addon, "/home/u/new", "shared")
    decision = decide_additive(requested, fresh, HOME)
    assert decision.action is AdditiveAction.blocked
    assert decision.reason == "internal_label_collision"


def test_docroot_overlap_is_blocked() -> None:
    fresh = [_addon("existing.test", "/home/u/shared")]
    requested = RequestedDomain("new.test", DomainType.addon, "/home/u/shared/nested", "new")
    decision = decide_additive(requested, fresh, HOME)
    assert decision.action is AdditiveAction.blocked
    assert decision.reason == "docroot_overlap"


def test_addon_nested_under_main_public_html_is_allowed() -> None:
    # The normal cPanel layout: an addon docroot lives under the main domain's
    # public_html. That is NOT an unsafe overlap and must remain creatable.
    fresh = [
        DomainRecord("example.test", DomainType.main, "/home/u/public_html"),
    ]
    requested = RequestedDomain("addon.test", DomainType.addon, "/home/u/public_html/addon", "addon")
    assert decide_additive(requested, fresh, HOME).action is AdditiveAction.create


def test_exact_docroot_reuse_with_main_is_blocked() -> None:
    fresh = [DomainRecord("example.test", DomainType.main, "/home/u/public_html")]
    requested = RequestedDomain("addon.test", DomainType.addon, "/home/u/public_html", "addon")
    assert decide_additive(requested, fresh, HOME).action is AdditiveAction.blocked


def test_unsupported_type_is_manual() -> None:
    requested = RequestedDomain("brandnew.test", DomainType.main, "/home/u/public_html")
    decision = decide_additive(requested, [], HOME)
    assert decision.action is AdditiveAction.unsupported


@pytest.mark.parametrize("kind", [DomainType.addon, DomainType.subdomain])
def test_addon_subdomain_without_docroot_is_blocked(kind: DomainType) -> None:
    requested = RequestedDomain("new.test", kind, None, "new")
    decision = decide_additive(requested, [], HOME)
    assert decision.action is AdditiveAction.blocked
    assert decision.reason == "missing_docroot"


def test_unsafe_docroot_on_create_is_blocked() -> None:
    requested = RequestedDomain("new.test", DomainType.addon, "/home/u/../escape", "new")
    decision = decide_additive(requested, [], HOME)
    assert decision.action is AdditiveAction.blocked
    assert decision.reason.startswith("unsafe_docroot")


def test_invalid_domain_name_is_blocked() -> None:
    requested = RequestedDomain("a..b.test", DomainType.addon, "/home/u/new")
    decision = decide_additive(requested, [], HOME)
    assert decision.action is AdditiveAction.blocked
    assert decision.reason.startswith("invalid_domain")


def test_normalize_rejects_non_string() -> None:
    with pytest.raises(DomainRuleError):
        normalize_domain(123)  # type: ignore[arg-type]


def test_normalize_rejects_overlong_label() -> None:
    with pytest.raises(DomainRuleError):
        normalize_domain("a" * 64 + ".test")


@pytest.mark.parametrize("bad", [None, "/home/u/bad\x00root"])
def test_docroot_rejects_non_string_and_nul(bad) -> None:
    with pytest.raises(DomainRuleError):
        validate_docroot(bad, HOME)


def test_alias_without_docroot_is_idempotent() -> None:
    fresh = [DomainRecord("alias.test", DomainType.alias, None)]
    requested = RequestedDomain("Alias.Test", DomainType.alias, None)
    assert decide_additive(requested, fresh, HOME).action is AdditiveAction.already_present


def test_create_skips_none_docroot_and_invalid_names_in_fresh() -> None:
    # A fresh read may contain an alias (no docroot) and an unparseable name; the
    # overlap/label scans must skip both without raising, still allowing a create.
    fresh = [
        DomainRecord("alias.test", DomainType.alias, None),
        DomainRecord("z" * 64 + ".test", DomainType.addon, "/home/u/broken", "broken"),
    ]
    requested = RequestedDomain("new.test", DomainType.addon, "/home/u/new", "new")
    assert decide_additive(requested, fresh, HOME).action is AdditiveAction.create


def test_create_without_internal_label_skips_label_scan() -> None:
    requested = RequestedDomain("sub.example.test", DomainType.subdomain, "/home/u/sub", None)
    assert decide_additive(requested, [], HOME).action is AdditiveAction.create


def test_same_name_type_but_missing_existing_docroot_is_blocked() -> None:
    # Existing record has no docroot while the request carries one: not equivalent,
    # so it fails closed rather than assuming a match.
    fresh = [DomainRecord("new.test", DomainType.addon, None, "new")]
    requested = RequestedDomain("new.test", DomainType.addon, "/home/u/new", "new")
    assert decide_additive(requested, fresh, HOME).action is AdditiveAction.blocked


def test_label_collision_with_unparseable_existing_name_blocks() -> None:
    # A live domain with an unparseable name but the same internal label still
    # collides; the label scan must treat the mismatched (None) name as "other".
    fresh = [DomainRecord("z" * 64 + ".test", DomainType.addon, "/home/u/broken", "shared")]
    requested = RequestedDomain("new.test", DomainType.addon, "/home/u/new", "shared")
    decision = decide_additive(requested, fresh, HOME)
    assert decision.action is AdditiveAction.blocked
    assert decision.reason == "internal_label_collision"


def test_collision_appeared_after_snapshot_is_detected() -> None:
    # The planning snapshot was empty, but the live fresh read now shows the name:
    # decide operates on the fresh read, so it detects the late collision.
    fresh = [_addon("new.test", "/home/u/new", "new")]
    requested = RequestedDomain("new.test", DomainType.addon, "/home/u/DIFFERENT")
    assert decide_additive(requested, fresh, HOME).action is AdditiveAction.blocked
