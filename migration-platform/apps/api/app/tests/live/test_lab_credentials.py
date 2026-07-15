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


# -- gate ordering: credential not read unless the non-secret gates passed ----

def test_credential_not_read_when_gates_fail(repo_root, outside):
    called = {"n": 0}

    def _spy_loader(*a, **k):
        called["n"] += 1
        return _TOKEN

    with pytest.raises(lc.LabCredentialError):
        lc.resolve_lab_token_after_gates(authorized=False, token_file="ignored",
                                         repo_root=str(repo_root), loader=_spy_loader)
    assert called["n"] == 0  # loader must never run when a prior gate failed


def test_credential_read_only_after_gates(repo_root, outside):
    p = _write(outside / "tok", _TOKEN, 0o600)
    tok = lc.resolve_lab_token_after_gates(authorized=True, token_file=p,
                                           repo_root=str(repo_root))
    assert tok == _TOKEN


# -- CpanelCredentials assembly (token never in repr) -------------------------

def test_build_credentials_token_not_in_repr(repo_root, outside):
    p = _write(outside / "tok", _TOKEN, 0o600)
    env = {"CPANEL_TEST_USERNAME": "labuser", "CPANEL_TEST_API_HOST": "lab.example",
           "CPANEL_TEST_API_PORT": "2083", "CPANEL_TEST_TOKEN_FILE": p}
    creds = lc.build_lab_credentials(env, authorized=True, repo_root=str(repo_root))
    assert creds.username == "labuser" and creds.verify_tls is True
    assert _TOKEN not in repr(creds)  # pydantic repr=False on api_token


def test_build_credentials_missing_username_rejected(repo_root, outside):
    p = _write(outside / "tok", _TOKEN, 0o600)
    env = {"CPANEL_TEST_API_HOST": "lab.example", "CPANEL_TEST_TOKEN_FILE": p}
    with pytest.raises(lc.LabCredentialError):
        lc.build_lab_credentials(env, authorized=True, repo_root=str(repo_root))
