"""The ephemeral SSH workspace contract: private, deterministic, disposable.

The workspace is the only place a resolved secret ever touches a disk. Its whole
value is that the disk it touches is private (0700/0600), unpredictable, outside
the repository, and removed on every exit path — including the failing ones.

Modes are asserted from the real bits via ``os.stat``, never from a mocked
``chmod``: a test that trusts the call it just made proves nothing about the file.

Two formats are pinned against the Go engine, which is the only authority:
``known_hosts`` must match ``golang.org/x/crypto/ssh/knownhosts`` (bare host at
port 22, ``[host]:port`` otherwise — see Normalize), and ``host.yaml`` must match
``internal/config``'s strict parser. The Go side of that contract is proven in
``internal/config/generated_hostyaml_test.go``; here we pin the bytes.
"""

from __future__ import annotations

import os
import stat
from pathlib import Path

import pytest
import yaml
from adapters.ssh_host_keys import parse_host_key
from adapters.ssh_runtime import SshCredentials, SshRuntimeSnapshot
from adapters.ssh_workspace import (
    HOST_CONFIG_NAME,
    KNOWN_HOSTS_NAME,
    SSH_DIR_NAME,
    WorkspaceSecurityError,
    build_ssh_workspace,
    known_hosts_line,
)
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

_PASSWORD = "pw-sentinel-0xDEADBEEF"
_PASSPHRASE = "pp-sentinel-0xFEEDFACE"


def _host_key_line() -> str:
    key = Ed25519PrivateKey.generate().public_key()
    return key.public_bytes(
        encoding=serialization.Encoding.OpenSSH,
        format=serialization.PublicFormat.OpenSSH,
    ).decode()


def _private_key() -> str:
    return (
        Ed25519PrivateKey.generate()
        .private_bytes(
            encoding=serialization.Encoding.PEM,
            format=serialization.PrivateFormat.OpenSSH,
            encryption_algorithm=serialization.NoEncryption(),
        )
        .decode()
    )


_HOST_KEY = _host_key_line()
_KEY_MATERIAL = _private_key()


def _snapshot(**overrides: object) -> SshRuntimeSnapshot:
    base: dict[str, object] = {
        "endpoint_id": 7,
        "host": "server.example.com",
        "port": 22,
        "username": "cpaneluser",
        # Already proven by the loader through validate_persisted_host_key: the
        # builder receives a validated key, it does not re-derive one.
        "host_key": parse_host_key(_HOST_KEY),
        "credentials": SshCredentials(auth_method="password", password=_PASSWORD),
    }
    base.update(overrides)
    return SshRuntimeSnapshot(**base)  # type: ignore[arg-type]


def _key_snapshot(**overrides: object) -> SshRuntimeSnapshot:
    return _snapshot(
        credentials=SshCredentials(
            auth_method="private_key", private_key=_KEY_MATERIAL, **overrides
        )
    )


@pytest.fixture()
def runtime_root(tmp_path: Path) -> Path:
    root = tmp_path / "runtime"
    root.mkdir(mode=0o700)
    return root


def _mode(path: Path) -> int:
    return stat.S_IMODE(os.stat(path).st_mode)


# --- known_hosts format: pinned against knownhosts.Normalize ---------------


def test_known_hosts_uses_a_bare_host_on_port_22() -> None:
    line = known_hosts_line("server.example.com", 22, _HOST_KEY)

    assert line == f"server.example.com {_HOST_KEY}"


def test_known_hosts_brackets_a_non_standard_port() -> None:
    line = known_hosts_line("server.example.com", 2222, _HOST_KEY)

    assert line == f"[server.example.com]:2222 {_HOST_KEY}"


def test_known_hosts_handles_ipv4() -> None:
    assert known_hosts_line("203.0.113.10", 22, _HOST_KEY) == f"203.0.113.10 {_HOST_KEY}"
    assert (
        known_hosts_line("203.0.113.10", 2222, _HOST_KEY)
        == f"[203.0.113.10]:2222 {_HOST_KEY}"
    )


def test_known_hosts_handles_ipv6() -> None:
    """Normalize strips brackets and re-adds them only for a non-22 port."""
    assert known_hosts_line("2001:db8::1", 22, _HOST_KEY) == f"2001:db8::1 {_HOST_KEY}"
    assert (
        known_hosts_line("2001:db8::1", 2222, _HOST_KEY)
        == f"[2001:db8::1]:2222 {_HOST_KEY}"
    )


def test_known_hosts_accepts_an_already_bracketed_ipv6_literal() -> None:
    assert known_hosts_line("[2001:db8::1]", 22, _HOST_KEY) == f"2001:db8::1 {_HOST_KEY}"


def test_known_hosts_carries_the_key_not_the_fingerprint() -> None:
    line = known_hosts_line("h", 22, _HOST_KEY)

    assert line.endswith(_HOST_KEY)
    assert "SHA256:" not in line


def test_the_known_hosts_file_is_a_single_line_with_a_trailing_newline(
    runtime_root: Path,
) -> None:
    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as ws:
        text = ws.known_hosts_path.read_text()

    assert text == f"server.example.com {_HOST_KEY}\n"
    assert text.count("\n") == 1
    assert "#" not in text


def test_known_hosts_holds_one_entry_per_configured_host(runtime_root: Path) -> None:
    dest = _snapshot(endpoint_id=8, host="dest.example.com", port=2222)

    with build_ssh_workspace(
        _snapshot(), destination=dest, runtime_root=runtime_root
    ) as ws:
        lines = ws.known_hosts_path.read_text().splitlines()

    assert lines == [
        f"server.example.com {_HOST_KEY}",
        f"[dest.example.com]:2222 {_HOST_KEY}",
    ]


# --- filesystem: the real bits ---------------------------------------------


def test_the_workspace_directory_is_private(runtime_root: Path) -> None:
    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as ws:
        assert _mode(ws.root) == 0o700


def test_every_file_is_owner_only(runtime_root: Path) -> None:
    with build_ssh_workspace(_key_snapshot(), runtime_root=runtime_root) as ws:
        assert _mode(ws.host_config_path) == 0o600
        assert _mode(ws.known_hosts_path) == 0o600
        assert ws.source_key_path is not None
        assert _mode(ws.source_key_path) == 0o600
        assert _mode(ws.known_hosts_path.parent) == 0o700


def test_modes_do_not_depend_on_a_permissive_umask(runtime_root: Path) -> None:
    """0o600 must come from the open(), not from an umask that happens to help."""
    old = os.umask(0o000)
    try:
        with build_ssh_workspace(_key_snapshot(), runtime_root=runtime_root) as ws:
            assert _mode(ws.host_config_path) == 0o600
            assert _mode(ws.root) == 0o700
            assert ws.source_key_path is not None
            assert _mode(ws.source_key_path) == 0o600
    finally:
        os.umask(old)


def test_the_workspace_name_is_unpredictable_and_leaks_nothing(
    runtime_root: Path,
) -> None:
    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as first:
        with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as second:
            # Same input, different name: the randomness is the system's, not a
            # hash of the row (which would be stable and therefore guessable).
            assert first.root != second.root
            for ws in (first, second):
                name = ws.root.name
                for leak in ("server", "example.com", "cpaneluser", _PASSWORD):
                    assert leak not in name


def test_file_names_are_constant_and_not_derived_from_the_row(
    runtime_root: Path,
) -> None:
    hostile = _snapshot(host="../../etc/pwn", username="../../root")

    with build_ssh_workspace(hostile, runtime_root=runtime_root) as ws:
        assert ws.host_config_path.name == HOST_CONFIG_NAME
        assert ws.known_hosts_path.name == KNOWN_HOSTS_NAME
        assert ws.host_config_path.parent == ws.root
        assert ws.known_hosts_path.parent == ws.root / SSH_DIR_NAME


def test_no_file_is_written_outside_the_workspace(runtime_root: Path) -> None:
    hostile = _snapshot(host="../../etc/pwn", username="../../root")

    with build_ssh_workspace(hostile, runtime_root=runtime_root) as ws:
        produced = {p for p in runtime_root.rglob("*")}
        assert all(str(p).startswith(str(ws.root)) for p in produced)
        assert not (runtime_root.parent / "etc").exists()


def test_a_password_workspace_writes_no_private_key(runtime_root: Path) -> None:
    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as ws:
        assert ws.source_key_path is None
        names = {p.name for p in ws.root.rglob("*") if p.is_file()}
        assert names == {HOST_CONFIG_NAME, KNOWN_HOSTS_NAME}


def test_the_private_key_file_holds_exactly_the_material(runtime_root: Path) -> None:
    with build_ssh_workspace(_key_snapshot(), runtime_root=runtime_root) as ws:
        assert ws.source_key_path is not None
        assert ws.source_key_path.read_text() == _KEY_MATERIAL


# --- security of the root and of the writes --------------------------------


def test_a_symlinked_runtime_root_is_refused(tmp_path: Path) -> None:
    real = tmp_path / "real"
    real.mkdir(mode=0o700)
    link = tmp_path / "link"
    link.symlink_to(real, target_is_directory=True)

    with pytest.raises(WorkspaceSecurityError):
        with build_ssh_workspace(_snapshot(), runtime_root=link):
            pass


def test_a_missing_runtime_root_is_refused(tmp_path: Path) -> None:
    with pytest.raises(WorkspaceSecurityError):
        with build_ssh_workspace(_snapshot(), runtime_root=tmp_path / "absent"):
            pass


def test_a_runtime_root_that_is_a_file_is_refused(tmp_path: Path) -> None:
    plain = tmp_path / "file"
    plain.write_text("x")

    with pytest.raises(WorkspaceSecurityError):
        with build_ssh_workspace(_snapshot(), runtime_root=plain):
            pass


def test_a_world_writable_runtime_root_is_refused(tmp_path: Path) -> None:
    root = tmp_path / "open"
    root.mkdir()
    os.chmod(root, 0o777)  # chmod, not mkdir's mode: the umask would mask it away

    with pytest.raises(WorkspaceSecurityError):
        with build_ssh_workspace(_snapshot(), runtime_root=root):
            pass


def test_a_world_writable_sticky_root_is_accepted(tmp_path: Path) -> None:
    """/tmp itself is 1777; the sticky bit is what makes it usable."""
    root = tmp_path / "sticky"
    root.mkdir()
    os.chmod(root, 0o1777)

    with build_ssh_workspace(_snapshot(), runtime_root=root) as ws:
        assert _mode(ws.root) == 0o700


# --- lifecycle and cleanup -------------------------------------------------


def test_the_workspace_is_gone_after_a_normal_exit(runtime_root: Path) -> None:
    with build_ssh_workspace(_key_snapshot(), runtime_root=runtime_root) as ws:
        root = ws.root
        assert root.exists()

    assert not root.exists()
    assert list(runtime_root.iterdir()) == []


def test_the_workspace_is_gone_after_an_exception_inside_the_block(
    runtime_root: Path,
) -> None:
    root = None
    with pytest.raises(RuntimeError):
        with build_ssh_workspace(_key_snapshot(), runtime_root=runtime_root) as ws:
            root = ws.root
            raise RuntimeError("boom")

    assert root is not None and not root.exists()
    assert list(runtime_root.iterdir()) == []


def test_cleanup_is_idempotent(runtime_root: Path) -> None:
    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as ws:
        pass

    ws.cleanup()
    ws.cleanup()  # a third time, after the context manager already ran one

    assert not ws.root.exists()


def test_cleanup_survives_a_workspace_removed_underneath_it(
    runtime_root: Path,
) -> None:
    import shutil

    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as ws:
        shutil.rmtree(ws.root)

    assert not ws.root.exists()


def test_cleanup_does_not_remove_the_runtime_root(runtime_root: Path) -> None:
    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root):
        pass

    assert runtime_root.exists()
    assert _mode(runtime_root) == 0o700


@pytest.mark.parametrize(
    "failing_file", [HOST_CONFIG_NAME, KNOWN_HOSTS_NAME, "source_key"]
)
def test_a_failure_while_building_leaves_nothing_behind(
    runtime_root: Path, monkeypatch: pytest.MonkeyPatch, failing_file: str
) -> None:
    """A partially built workspace is still a workspace full of secrets.

    Injected at the real write, one file at a time, so each stage of the build is
    proven to unwind — not only the last one.
    """
    import adapters.ssh_workspace as mod

    real = mod._write_private_file

    def explode(root: Path, name: str, content: str) -> Path:
        if name == failing_file:
            raise OSError("disk full")
        return real(root, name, content)

    monkeypatch.setattr(mod, "_write_private_file", explode)

    with pytest.raises(Exception):
        with build_ssh_workspace(_key_snapshot(), runtime_root=runtime_root):
            pass

    assert list(runtime_root.iterdir()) == []


# --- host.yaml: pinned bytes, Go-parseable shape ---------------------------


def test_host_yaml_password_mode(runtime_root: Path) -> None:
    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as ws:
        doc = yaml.safe_load(ws.host_config_path.read_text())

    assert doc == {
        "src": {
            "ip": "server.example.com",
            "port": 22,
            "ssh_user": "cpaneluser",
            "ssh_pass": _PASSWORD,
            "timeout": "30s",
        }
    }


def test_host_yaml_key_mode_points_at_the_workspace_key(runtime_root: Path) -> None:
    with build_ssh_workspace(_key_snapshot(), runtime_root=runtime_root) as ws:
        doc = yaml.safe_load(ws.host_config_path.read_text())
        assert doc["src"]["ssh_key_path"] == str(ws.source_key_path)

    assert "ssh_pass" not in doc["src"]
    assert "ssh_key_passphrase" not in doc["src"]
    assert Path(doc["src"]["ssh_key_path"]).is_absolute()


def test_host_yaml_key_with_passphrase(runtime_root: Path) -> None:
    snap = _key_snapshot(passphrase=_PASSPHRASE)

    with build_ssh_workspace(snap, runtime_root=runtime_root) as ws:
        doc = yaml.safe_load(ws.host_config_path.read_text())

    assert doc["src"]["ssh_key_passphrase"] == _PASSPHRASE
    assert "ssh_pass" not in doc["src"]


def test_host_yaml_omits_dest_entirely_when_there_is_none(runtime_root: Path) -> None:
    """The Go parser treats a partially filled dest as an error; absent is valid."""
    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as ws:
        text = ws.host_config_path.read_text()

    assert "dest" not in text
    assert yaml.safe_load(text).keys() == {"src"}


def test_host_yaml_emits_a_full_dest_when_there_is_one(runtime_root: Path) -> None:
    dest = _snapshot(endpoint_id=8, host="dest.example.com", port=2222, username="destuser")

    with build_ssh_workspace(
        _snapshot(), destination=dest, runtime_root=runtime_root
    ) as ws:
        doc = yaml.safe_load(ws.host_config_path.read_text())

    assert doc["dest"] == {
        "ip": "dest.example.com",
        "port": 2222,
        "ssh_user": "destuser",
        "ssh_pass": _PASSWORD,
        "timeout": "30s",
    }


def test_host_yaml_is_a_single_document_with_no_python_tags(
    runtime_root: Path,
) -> None:
    """The Go loader rejects a second document, and safe YAML has no !!python tags."""
    with build_ssh_workspace(_key_snapshot(), runtime_root=runtime_root) as ws:
        text = ws.host_config_path.read_text()

    assert "!!python" not in text
    assert text.count("---") == 0
    assert len(list(yaml.safe_load_all(text))) == 1


def test_host_yaml_is_deterministic(runtime_root: Path) -> None:
    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as first:
        a = first.host_config_path.read_text()
    with build_ssh_workspace(_snapshot(), runtime_root=runtime_root) as second:
        b = second.host_config_path.read_text()

    assert a == b


def test_host_yaml_carries_no_field_the_go_parser_would_reject(
    runtime_root: Path,
) -> None:
    """KnownFields(true): an extra key is a hard parse error, not a warning."""
    allowed = {"ip", "port", "ssh_user", "ssh_pass", "ssh_key_path", "ssh_key_passphrase", "timeout"}

    with build_ssh_workspace(_key_snapshot(), runtime_root=runtime_root) as ws:
        doc = yaml.safe_load(ws.host_config_path.read_text())

    assert doc.keys() <= {"src", "dest", "databases"}
    for block in ("src", "dest"):
        if block in doc:
            assert set(doc[block]) <= allowed
            # No null placeholders: the Go parser has no defaults and would read a
            # null timeout as 0, which it then rejects.
            assert all(v is not None for v in doc[block].values())


# --- redaction -------------------------------------------------------------


def test_the_workspace_repr_shows_no_secret(runtime_root: Path) -> None:
    snap = _key_snapshot(passphrase=_PASSPHRASE)

    with build_ssh_workspace(snap, runtime_root=runtime_root) as ws:
        text = repr(ws)

    assert _PASSPHRASE not in text
    assert _KEY_MATERIAL not in text
    assert _PASSWORD not in text


def test_the_snapshot_repr_shows_no_secret() -> None:
    snap = _key_snapshot(passphrase=_PASSPHRASE)

    text = repr(snap)

    assert _PASSPHRASE not in text
    assert _KEY_MATERIAL not in text
    assert "PRIVATE KEY" not in text


def test_a_build_error_never_echoes_a_secret(
    runtime_root: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import adapters.ssh_workspace as mod

    def explode(root: Path, name: str, content: str) -> Path:
        raise OSError("disk full")

    monkeypatch.setattr(mod, "_write_private_file", explode)

    with pytest.raises(Exception) as excinfo:
        with build_ssh_workspace(_snapshot(), runtime_root=runtime_root):
            pass

    assert _PASSWORD not in str(excinfo.value)


def test_an_existing_file_in_the_workspace_cannot_be_clobbered(
    runtime_root: Path,
) -> None:
    """O_EXCL: the builder creates, it never opens something already there."""
    import adapters.ssh_workspace as mod

    target = runtime_root / HOST_CONFIG_NAME
    target.write_text("pre-existing")

    with pytest.raises(OSError):
        mod._write_private_file(runtime_root, HOST_CONFIG_NAME, "new")

    assert target.read_text() == "pre-existing"


def test_a_symlinked_file_target_is_not_followed(
    runtime_root: Path, tmp_path: Path
) -> None:
    """Without O_EXCL|O_NOFOLLOW a planted symlink would redirect the write —
    and the private key would land wherever the link points."""
    import adapters.ssh_workspace as mod

    outside = tmp_path / "outside.txt"
    outside.write_text("untouched")
    (runtime_root / HOST_CONFIG_NAME).symlink_to(outside)

    with pytest.raises(OSError):
        mod._write_private_file(runtime_root, HOST_CONFIG_NAME, "secret")

    assert outside.read_text() == "untouched"
