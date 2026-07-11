"""Unit tests for the SSH contract: command builder, host keys, redaction."""

from __future__ import annotations

import pytest

from adapters.ssh.contract import (
    SessionRole,
    SshCredentials,
    SshEndpoint,
    command,
    redact,
)
from adapters.ssh.errors import (
    SshCommandRejectedError,
    SshHostKeyChangedError,
    SshHostKeyUnknownError,
)
from adapters.ssh.hostkeys import HostKeyPolicy, HostKeyRecord, KnownHostsStore
from adapters.ssh.fakes import make_host_key

SECRET = "pw-SUPER-SECRET-value"


# -- command builder / no shell injection ---------------------------------


def test_command_quotes_arguments_so_no_shell_injection() -> None:
    cmd = command("echo", "$(rm -rf /)")
    # shlex.join quotes the argument; the metacharacters are delivered literally.
    assert cmd.wire == "echo '$(rm -rf /)'"
    assert cmd.program == "echo"
    assert cmd.is_write is False


def test_command_rejects_unsafe_program_name() -> None:
    for bad in ("rm -rf", "a;b", "cat|less", "", "x`y`"):
        with pytest.raises(SshCommandRejectedError):
            command(bad)


def test_command_rejects_nul_and_non_string_arguments() -> None:
    with pytest.raises(SshCommandRejectedError):
        command("cat", "file\x00name")
    with pytest.raises(SshCommandRejectedError):
        command("cat", 123)  # type: ignore[arg-type]


def test_command_label_summarises_arguments_not_content() -> None:
    assert command("ls").label() == "ls"
    assert command("cat", SECRET).label() == "cat (+1 args)"
    assert SECRET not in command("cat", SECRET).label()


# -- credentials / endpoint validation ------------------------------------


def test_credentials_require_a_secret_and_report_method() -> None:
    with pytest.raises(ValueError):
        SshCredentials()
    assert SshCredentials(password=SECRET).auth_method == "password"
    assert SshCredentials(private_key_pem="KEY").auth_method == "key"
    assert SshCredentials(password=SECRET, private_key_pem="KEY").auth_method == "key+password"


def test_credentials_repr_never_shows_secret() -> None:
    creds = SshCredentials(password=SECRET, private_key_passphrase="phrase")
    assert SECRET not in repr(creds)
    assert "phrase" not in repr(creds)
    assert set(creds.secret_values()) == {SECRET, "phrase"}


def test_endpoint_rejects_unsafe_host_and_username() -> None:
    for bad in ("evil host", "a/b", "u@h", "x\ny", "http://h"):
        with pytest.raises(ValueError):
            SshEndpoint(host=bad, username="acct")
    with pytest.raises(ValueError):
        SshEndpoint(host="ok.example", username="a b")


def test_session_role_capabilities() -> None:
    assert SessionRole.SOURCE_READ.is_source is True
    assert SessionRole.SOURCE_READ.can_write is False
    assert SessionRole.DESTINATION_WRITE.can_write is True
    assert SessionRole.DESTINATION_READ.can_write is False


# -- redaction ------------------------------------------------------------


def test_redact_masks_known_secrets_and_sensitive_pairs() -> None:
    text = f"auth {SECRET} password={SECRET} token: abc123"
    cleaned = redact(text, (SECRET,))
    assert SECRET not in cleaned
    assert "***" in cleaned
    assert "abc123" not in cleaned  # token: value masked by key rule


# -- host-key policy ------------------------------------------------------


def test_known_host_key_matched() -> None:
    store = KnownHostsStore()
    key = make_host_key("h.example")
    store.add(key)
    decision = HostKeyPolicy("strict").verify(store, key)
    assert decision.status == "matched"
    assert decision.fingerprint.startswith("SHA256:")


def test_unknown_host_rejected_in_strict_mode() -> None:
    store = KnownHostsStore()
    with pytest.raises(SshHostKeyUnknownError):
        HostKeyPolicy("strict").verify(store, make_host_key("h.example"))


def test_accept_new_records_the_key_audibly() -> None:
    store = KnownHostsStore()
    key = make_host_key("h.example")
    decision = HostKeyPolicy("accept_new").verify(store, key)
    assert decision.status == "accepted_new"
    assert store.lookup("h.example", 22) == key


def test_changed_host_key_rejected_even_when_accept_new() -> None:
    store = KnownHostsStore()
    store.add(make_host_key("h.example", seed="original"))
    changed = make_host_key("h.example", seed="attacker")
    for mode in ("strict", "accept_new"):
        with pytest.raises(SshHostKeyChangedError):
            HostKeyPolicy(mode).verify(store, changed)


def test_known_hosts_store_persists_across_instances(tmp_path) -> None:
    path = tmp_path / "known_hosts"
    key = make_host_key("h.example")
    KnownHostsStore(path).add(key)
    reloaded = KnownHostsStore(path)
    assert reloaded.lookup("h.example", 22) == key


def test_known_hosts_store_skips_malformed_lines(tmp_path) -> None:
    path = tmp_path / "known_hosts"
    good = make_host_key("h.example")
    path.write_text(
        "# comment\n"
        "\n"
        "malformed line without enough fields\n"
        "nohost ssh-rsa AAAA\n"  # address without :port
        f"{good.address} {good.key_type} {good.key_base64}\n",
        encoding="utf-8",
    )
    store = KnownHostsStore(path)
    assert store.lookup("h.example", 22) == good
    assert store.lookup("nohost", 22) is None


def test_adding_same_key_twice_is_a_noop(tmp_path) -> None:
    path = tmp_path / "known_hosts"
    key = make_host_key("h.example")
    store = KnownHostsStore(path)
    store.add(key)
    store.add(key)  # identical: no duplicate, no error
    assert store.lookup("h.example", 22) == key


def test_host_key_fingerprint_is_stable_and_auditable() -> None:
    key = HostKeyRecord("h", 22, "ssh-ed25519", make_host_key("h").key_base64)
    assert key.fingerprint == make_host_key("h").fingerprint
