"""R2-c4-LAB-GATEWAY — test-only cPanel lab credential loader (token-file).

There is NO Vault resolver in this repo and `CREDENTIAL_ENCRYPTION_KEY` is at-rest encryption, not
secret injection, so the lab token is loaded from a strictly-protected local file OUTSIDE the repo.
These tests prove every protection with real temp files; the token value never appears in any
error/repr, and the credential is never read until the non-secret gates pass. No network, no cPanel.
"""
from __future__ import annotations

import os

import pytest

from app.tests.live import lab_credentials as lc

_TOKEN = "lab-token-VALUE-should-never-leak"


def _write(path, data: str, mode: int):
    path.write_text(data)
    os.chmod(path, mode)
    return str(path)


@pytest.fixture
def repo_root(tmp_path):
    root = tmp_path / "repo"
    root.mkdir()
    return root


@pytest.fixture
def outside(tmp_path):
    d = tmp_path / "outside"
    d.mkdir()
    return d


def test_valid_0600_token_accepted(repo_root, outside):
    p = _write(outside / "tok", _TOKEN + "\n", 0o600)
    assert lc.load_lab_token(p, repo_root=str(repo_root)) == _TOKEN


def test_group_or_world_readable_rejected(repo_root, outside):
    for mode in (0o640, 0o604, 0o660, 0o644):
        p = _write(outside / f"tok{mode}", _TOKEN, mode)
        with pytest.raises(lc.LabCredentialError):
            lc.load_lab_token(p, repo_root=str(repo_root))


def test_relative_path_rejected(repo_root):
    with pytest.raises(lc.LabCredentialError):
        lc.load_lab_token("relative/tok", repo_root=str(repo_root))


def test_symlink_rejected(repo_root, outside):
    target = _write(outside / "real", _TOKEN, 0o600)
    link = outside / "link"
    os.symlink(target, link)
    with pytest.raises(lc.LabCredentialError):
        lc.load_lab_token(str(link), repo_root=str(repo_root))


def test_file_inside_repo_rejected(repo_root):
    p = _write(repo_root / "tok", _TOKEN, 0o600)
    with pytest.raises(lc.LabCredentialError):
        lc.load_lab_token(p, repo_root=str(repo_root))


def test_missing_file_rejected(repo_root, outside):
    with pytest.raises(lc.LabCredentialError):
        lc.load_lab_token(str(outside / "nope"), repo_root=str(repo_root))


def test_empty_file_rejected(repo_root, outside):
    p = _write(outside / "tok", "   \n", 0o600)
    with pytest.raises(lc.LabCredentialError):
        lc.load_lab_token(p, repo_root=str(repo_root))


def test_oversized_file_rejected(repo_root, outside):
    p = _write(outside / "tok", "x" * (lc.LAB_TOKEN_MAX_BYTES + 1), 0o600)
    with pytest.raises(lc.LabCredentialError):
        lc.load_lab_token(p, repo_root=str(repo_root))


def test_owner_mismatch_rejected(repo_root, outside):
    p = _write(outside / "tok", _TOKEN, 0o600)
    with pytest.raises(lc.LabCredentialError):
        lc.load_lab_token(p, repo_root=str(repo_root), current_uid=os.getuid() + 12345)


def test_directory_rejected(repo_root, outside):
    d = outside / "adir"
    d.mkdir()
    os.chmod(d, 0o700)
    with pytest.raises(lc.LabCredentialError):
        lc.load_lab_token(str(d), repo_root=str(repo_root))


def test_token_never_in_error(repo_root, outside):
    # a bad-mode file whose CONTENT is the token: the raised error must not leak it.
    p = _write(outside / "tok", _TOKEN, 0o644)
    try:
        lc.load_lab_token(p, repo_root=str(repo_root))
        raise AssertionError("should have refused")
    except lc.LabCredentialError as exc:
        assert _TOKEN not in str(exc) and _TOKEN not in repr(exc)


# -- LabConnectionGateReceipt: the opaque object that REPLACES the falsifiable boolean ----

_COMMIT = "a0e72e3feedface"
_ENDPOINT = "disposable-1"
_DOMAIN = "throwaway-account.test"
_GATES = {"run_destructive": True, "disposable": True, "reset_approved": True,
          "endpoint_allowlisted": True, "not_production": True, "domain_configured": True,
          "working_tree_clean": True, "commit_present": True}


def _receipt(*, gates=None, commit=_COMMIT, endpoint=_ENDPOINT, domain=_DOMAIN,
             issued_at=1000.0, ttl=60.0, session_nonce="sess-1"):
    return lc.issue_connection_receipt(
        gates=_GATES if gates is None else gates, commit=commit, endpoint=endpoint,
        disposable_domain=domain, issued_at=issued_at, ttl_seconds=ttl, session_nonce=session_nonce)


def test_boolean_surface_removed():
    # the falsifiable `authorized=True` public entrypoint no longer exists
    assert not hasattr(lc, "resolve_lab_token_after_gates")


def test_receipt_issuance_binds_non_secret_state():
    r = _receipt()
    assert r.commit == _COMMIT and r.disposable_domain == _DOMAIN
    assert r.session_nonce == "sess-1"
    assert r.endpoint_fingerprint == lc.endpoint_fingerprint(_ENDPOINT)
    assert r.valid_at(1000.0) is True and r.valid_at(1061.0) is False


def test_receipt_direct_construction_rejected():
    with pytest.raises(lc.LabCredentialError):
        lc.LabConnectionGateReceipt(  # missing module-private sentinel
            None, commit=_COMMIT, endpoint=_ENDPOINT,
            endpoint_fingerprint=lc.endpoint_fingerprint(_ENDPOINT), disposable_domain=_DOMAIN,
            issued_at=1000.0, expires_at=1060.0, session_nonce="s")


@pytest.mark.parametrize("bad_gate", sorted(_GATES))
def test_receipt_refused_when_any_gate_unsatisfied(bad_gate):
    with pytest.raises(lc.LabCredentialError):
        _receipt(gates={**_GATES, bad_gate: False})


def test_receipt_repr_has_no_endpoint_username_or_token():
    r = _receipt()
    blob = repr(r)
    assert _ENDPOINT not in blob and "token" not in blob.lower()
    # only the fingerprint (not the raw endpoint) may appear
    assert lc.endpoint_fingerprint(_ENDPOINT) in blob


# -- resolve_lab_token: accepts ONLY a valid receipt; loader untouched on any refusal ----

def _spy():
    calls = {"n": 0}

    def loader(*a, **k):
        calls["n"] += 1
        return _TOKEN

    return calls, loader


def test_resolve_reads_token_only_with_valid_receipt(repo_root, outside):
    p = _write(outside / "tok", _TOKEN, 0o600)
    tok = lc.resolve_lab_token(_receipt(), p, repo_root=str(repo_root), now=1000.0,
                               expected_commit=_COMMIT, expected_endpoint=_ENDPOINT,
                               expected_domain=_DOMAIN)
    assert tok == _TOKEN


@pytest.mark.parametrize("kw", [
    {"receipt_override": "counterfeit"},          # forged (not a real receipt)
    {"now": 2000.0},                              # expired
    {"expected_commit": "other"},                 # commit mismatch
    {"expected_endpoint": "disposable-2"},        # endpoint mismatch
    {"expected_domain": "other.test"},            # domain mismatch
])
def test_resolve_refuses_and_never_opens_token_file(repo_root, kw):
    calls, loader = _spy()
    receipt = kw.pop("receipt_override", None) or _receipt()
    with pytest.raises(lc.LabCredentialError):
        lc.resolve_lab_token(receipt, "/nonexistent/should-not-open", repo_root=str(repo_root),
                             now=kw.get("now", 1000.0),
                             expected_commit=kw.get("expected_commit", _COMMIT),
                             expected_endpoint=kw.get("expected_endpoint", _ENDPOINT),
                             expected_domain=kw.get("expected_domain", _DOMAIN), loader=loader)
    assert calls["n"] == 0  # the token file is never opened on any refusal


# -- CpanelCredentials assembly (receipt-gated; token never in repr) ----------

def _env(p):
    return {"CPANEL_TEST_USERNAME": "labuser", "CPANEL_TEST_API_HOST": "lab.example",
            "CPANEL_TEST_API_PORT": "2083", "CPANEL_TEST_TOKEN_FILE": p}


def test_build_credentials_token_not_in_repr(repo_root, outside):
    p = _write(outside / "tok", _TOKEN, 0o600)
    creds = lc.build_lab_credentials(_env(p), _receipt(), repo_root=str(repo_root), now=1000.0,
                                     expected_commit=_COMMIT, expected_endpoint=_ENDPOINT,
                                     expected_domain=_DOMAIN)
    assert creds.username == "labuser" and creds.verify_tls is True
    assert _TOKEN not in repr(creds)  # pydantic repr=False on api_token


def test_build_credentials_missing_username_rejected(repo_root, outside):
    p = _write(outside / "tok", _TOKEN, 0o600)
    env = {"CPANEL_TEST_API_HOST": "lab.example", "CPANEL_TEST_TOKEN_FILE": p}
    with pytest.raises(lc.LabCredentialError):
        lc.build_lab_credentials(env, _receipt(), repo_root=str(repo_root), now=1000.0,
                                 expected_commit=_COMMIT, expected_endpoint=_ENDPOINT,
                                 expected_domain=_DOMAIN)
