"""The Python half of the host.yaml contract with the Go engine.

``internal/config`` is the only authority on what the engine consumes, and PyYAML
accepting its own output proves nothing about it. So the contract is a shared
corpus, like ``testdata/execution-contract``: the fixtures under
``internal/config/testdata/generated_hostyaml`` are the byte-for-byte output of
:func:`render_host_config`, and ``internal/config/generated_hostyaml_test.go``
feeds those same bytes to ``config.Load``.

This module pins the Python end. If the builder's output ever drifts, this fails
and the fixtures must be regenerated — which re-runs the Go half against the new
bytes. Neither side can silently stop matching the other.

The values are inert placeholders, not credentials.
"""

from __future__ import annotations

from pathlib import Path

import pytest
from adapters.ssh_host_keys import parse_host_key
from adapters.ssh_runtime import SshCredentials, SshRuntimeSnapshot
from adapters.ssh_workspace import render_host_config
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey


def _repo_root() -> Path:
    """Walk up to the repository root (the Go module's directory)."""
    here = Path(__file__).resolve()
    for parent in here.parents:
        if (parent / "go.mod").is_file():
            return parent
    raise RuntimeError("repository root not found: no go.mod above this file")


FIXTURE_DIR = _repo_root() / "internal" / "config" / "testdata" / "generated_hostyaml"

# The host key never reaches host.yaml (the engine has no field for it), so the
# fixtures do not depend on which key this is.
_HOST_KEY = parse_host_key(
    Ed25519PrivateKey.generate()
    .public_key()
    .public_bytes(
        encoding=serialization.Encoding.OpenSSH,
        format=serialization.PublicFormat.OpenSSH,
    )
    .decode()
)

_WORKSPACE = Path("/run/migration-ssh-fixture")
_PASSWORD = SshCredentials(auth_method="password", password="not-a-real-password")
_KEY = SshCredentials(auth_method="private_key", private_key="unused-by-the-parser")
_KEY_PP = SshCredentials(
    auth_method="private_key", private_key="unused", passphrase="not-a-real-passphrase"
)


def _snap(
    host: str, port: int, username: str, creds: SshCredentials, endpoint_id: int = 1
) -> SshRuntimeSnapshot:
    return SshRuntimeSnapshot(
        endpoint_id=endpoint_id,
        host=host,
        port=port,
        username=username,
        host_key=_HOST_KEY,
        credentials=creds,
    )


def _password() -> str:
    return render_host_config(_snap("203.0.113.10", 22, "srcuser", _PASSWORD), None)


def _private_key() -> str:
    return render_host_config(
        _snap("203.0.113.10", 22, "srcuser", _KEY), _WORKSPACE / "source_key"
    )


def _private_key_passphrase() -> str:
    return render_host_config(
        _snap("203.0.113.10", 22, "srcuser", _KEY_PP), _WORKSPACE / "source_key"
    )


def _nonstandard_port() -> str:
    return render_host_config(_snap("server.example.com", 2222, "srcuser", _PASSWORD), None)


def _src_and_dest() -> str:
    return render_host_config(
        _snap("203.0.113.10", 22, "srcuser", _KEY),
        _WORKSPACE / "source_key",
        _snap("198.51.100.20", 2222, "destuser", _PASSWORD, endpoint_id=2),
        None,
    )


_CASES = {
    "password.yaml": _password,
    "private_key.yaml": _private_key,
    "private_key_passphrase.yaml": _private_key_passphrase,
    "nonstandard_port.yaml": _nonstandard_port,
    "src_and_dest.yaml": _src_and_dest,
}


@pytest.mark.parametrize("name", sorted(_CASES))
def test_the_fixture_is_still_exactly_what_the_builder_produces(name: str) -> None:
    """Byte-for-byte: the Go test validates these bytes, so drift must be loud."""
    expected = _CASES[name]()

    assert (FIXTURE_DIR / name).read_text() == expected, (
        f"{name} no longer matches render_host_config. Regenerate the fixtures "
        f"and re-run `go test ./internal/config/` — the Go parser, not this test, "
        f"decides whether the new bytes are acceptable."
    )


def test_every_fixture_on_disk_is_declared_here() -> None:
    """Mirrors the Go side's coverage rule from both directions."""
    on_disk = {p.name for p in FIXTURE_DIR.iterdir() if p.is_file()}

    assert on_disk == set(_CASES)


def test_no_fixture_carries_a_real_looking_secret() -> None:
    """The corpus is committed; it must never become a place secrets live."""
    for name in _CASES:
        text = (FIXTURE_DIR / name).read_text()
        assert "PRIVATE KEY" not in text
        assert "not-a-real" in text or "ssh_pass" not in text
