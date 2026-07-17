"""The pinned-binary compatibility handshake: identify, ask, decide, fail-closed.

Three separations, each proven here:

* **identity** — the platform launches a *precise* binary, pinned by SHA-256
  over its content, never "whatever is in PATH". A missing file, a symlink, a
  directory, a non-executable and a digest mismatch are all refusals.
* **handshake** — the binary is asked ``capabilities`` under strict bounds:
  no shell, stdin closed, a stripped environment (a worker env can carry
  ``*_CPANEL_*`` secrets), a timeout, and a cap on the answer's size.
* **compatibility** — a typed decision over the parsed document: the contract
  versions the platform speaks must appear in every list, and the SSH facts a
  run needs must be present. Anything else refuses the launch.

The fake binaries here are small scripts: the cross-language proof that the
REAL binary emits a valid document is the shared corpus golden
(``testdata/execution-contract/valid/capabilities-emitted.json``), asserted by
both ``internal/executioncontract`` and the domain tests.
"""

from __future__ import annotations

import hashlib
import json
import os
import stat
from pathlib import Path

import pytest
from adapters.executor_handshake import (
    MAX_CAPABILITIES_BYTES,
    ExecutorCompatibilityError,
    ExecutorHandshakeError,
    ExecutorIdentityError,
    ensure_compatible,
    identify_executor_binary,
    run_capabilities_handshake,
)
from domain.execution_contract import parse_capabilities

_VALID_DOC = json.dumps(
    {
        "format_version": 1,
        "executor_version": "1.2.3",
        "contract": {"spec": [1], "event": [1], "result": [1]},
        "ssh": {
            "password": True,
            "private_key": True,
            "encrypted_private_key": True,
            "strict_host_config": True,
            "known_hosts_via_home": True,
        },
    }
)


def _write_fake_binary(path: Path, body: str) -> str:
    """Create an executable script and return its content digest."""
    script = "#!/bin/sh\n" + body + "\n"
    path.write_text(script)
    path.chmod(0o700)
    return hashlib.sha256(script.encode()).hexdigest()


def _fake_ok(tmp_path: Path, doc: str = _VALID_DOC) -> tuple[Path, str]:
    path = tmp_path / "executor"
    digest = _write_fake_binary(path, f"printf '%s' '{doc}'")
    return path, digest


# --- identity: a precise binary, never "whatever is in PATH" -----------------


def test_a_matching_digest_identifies_the_binary(tmp_path: Path) -> None:
    path, digest = _fake_ok(tmp_path)

    identity = identify_executor_binary(path, expected_sha256=digest)

    assert identity.path == path
    assert identity.sha256 == digest


def test_an_uppercase_expected_digest_is_normalized(tmp_path: Path) -> None:
    path, digest = _fake_ok(tmp_path)

    identity = identify_executor_binary(path, expected_sha256=digest.upper())

    assert identity.sha256 == digest


def test_a_digest_mismatch_is_refused_and_names_only_the_path(
    tmp_path: Path,
) -> None:
    path, _ = _fake_ok(tmp_path)
    wrong = "0" * 64

    with pytest.raises(ExecutorIdentityError) as excinfo:
        identify_executor_binary(path, expected_sha256=wrong)

    text = str(excinfo.value)
    assert str(path) in text
    # Neither digest belongs in the error: the operator re-computes, the
    # message stays stable, and nothing teaches an attacker what was expected.
    assert wrong not in text


def test_a_missing_binary_is_refused(tmp_path: Path) -> None:
    with pytest.raises(ExecutorIdentityError):
        identify_executor_binary(tmp_path / "absent", expected_sha256="0" * 64)


def test_a_symlinked_binary_is_refused(tmp_path: Path) -> None:
    """The digest pins content, but the path is what gets executed: a path an
    attacker can re-point is not a pinned binary."""
    real, digest = _fake_ok(tmp_path)
    link = tmp_path / "link"
    link.symlink_to(real)

    with pytest.raises(ExecutorIdentityError):
        identify_executor_binary(link, expected_sha256=digest)


def test_a_directory_is_refused(tmp_path: Path) -> None:
    with pytest.raises(ExecutorIdentityError):
        identify_executor_binary(tmp_path, expected_sha256="0" * 64)


def test_a_non_executable_file_is_refused(tmp_path: Path) -> None:
    path = tmp_path / "executor"
    path.write_text("#!/bin/sh\n")
    path.chmod(0o600)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()

    with pytest.raises(ExecutorIdentityError):
        identify_executor_binary(path, expected_sha256=digest)


@pytest.mark.parametrize("bad", ["", "zz" * 32, "abc123", "0" * 63, "0" * 65])
def test_a_malformed_expected_digest_is_refused(tmp_path: Path, bad: str) -> None:
    path, _ = _fake_ok(tmp_path)

    with pytest.raises(ExecutorIdentityError):
        identify_executor_binary(path, expected_sha256=bad)


# --- descriptor-to-exec: what is hashed must be what runs --------------------
#
# Hashing a descriptor and then handing the PATH to subprocess makes the kernel
# resolve that path a SECOND time. Between the two, the deployment directory can
# hand back a different file — so the bytes that were verified and the bytes that
# run are not the same artifact. Refusing a symlink at identify time does not
# touch this: the swap happens after the verdict.


def _doc(version: str) -> str:
    return _VALID_DOC.replace('"1.2.3"', f'"{version}"')


def test_replacing_the_source_after_identity_must_not_change_what_runs(
    tmp_path: Path,
) -> None:
    """The whole point of the pin: A was verified, so B must never answer."""
    source = tmp_path / "executor"
    digest_a = _write_fake_binary(source, f"printf '%s' '{_doc('trusted-A')}'")
    replacement = tmp_path / "replacement"
    _write_fake_binary(replacement, f"printf '%s' '{_doc('replaced-B')}'")

    identity = identify_executor_binary(source, expected_sha256=digest_a)
    os.replace(replacement, source)  # atomic: same path, different inode

    caps = run_capabilities_handshake(identity)

    assert caps.executor_version == "trusted-A", (
        "the handshake ran the file the path points at NOW, not the artifact "
        "whose bytes produced the verified digest"
    )


def test_editing_the_source_in_place_after_identity_must_not_change_what_runs(
    tmp_path: Path,
) -> None:
    source = tmp_path / "executor"
    digest_a = _write_fake_binary(source, f"printf '%s' '{_doc('trusted-A')}'")

    identity = identify_executor_binary(source, expected_sha256=digest_a)
    _write_fake_binary(source, f"printf '%s' '{_doc('edited-B')}'")

    caps = run_capabilities_handshake(identity)

    assert caps.executor_version == "trusted-A"


def test_deleting_the_source_after_identity_does_not_break_the_handshake(
    tmp_path: Path,
) -> None:
    """The verified artifact is ours; the deployment path is only input."""
    source = tmp_path / "executor"
    digest_a = _write_fake_binary(source, f"printf '%s' '{_doc('trusted-A')}'")

    identity = identify_executor_binary(source, expected_sha256=digest_a)
    source.unlink()

    caps = run_capabilities_handshake(identity)

    assert caps.executor_version == "trusted-A"


# --- handshake: bounded subprocess, stripped environment ---------------------


def test_a_well_behaved_binary_answers_the_handshake(tmp_path: Path) -> None:
    path, digest = _fake_ok(tmp_path)
    identity = identify_executor_binary(path, expected_sha256=digest)

    caps = run_capabilities_handshake(identity)

    assert caps.executor_version == "1.2.3"
    assert caps.ssh.private_key is True


def test_the_handshake_passes_the_capabilities_argument(tmp_path: Path) -> None:
    path = tmp_path / "executor"
    digest = _write_fake_binary(
        path,
        f'if [ "$1" = "capabilities" ]; then printf \'%s\' \'{_VALID_DOC}\'; else exit 7; fi',
    )
    identity = identify_executor_binary(path, expected_sha256=digest)

    caps = run_capabilities_handshake(identity)

    assert caps.format_version == 1


def test_garbage_output_is_a_handshake_error(tmp_path: Path) -> None:
    path = tmp_path / "executor"
    digest = _write_fake_binary(path, "printf 'not json at all'")
    identity = identify_executor_binary(path, expected_sha256=digest)

    with pytest.raises(ExecutorHandshakeError):
        run_capabilities_handshake(identity)


def test_a_nonzero_exit_is_refused_without_echoing_stderr(tmp_path: Path) -> None:
    path = tmp_path / "executor"
    digest = _write_fake_binary(path, "echo 'stderr-sentinel-0xTEA' >&2; exit 3")
    identity = identify_executor_binary(path, expected_sha256=digest)

    with pytest.raises(ExecutorHandshakeError) as excinfo:
        run_capabilities_handshake(identity)

    text = str(excinfo.value)
    assert "3" in text
    # stderr is attacker-influenced text; it must not ride into exceptions
    # (pytest serializes failure text into CI's JUnit artifact).
    assert "stderr-sentinel-0xTEA" not in text


def test_a_hanging_binary_is_killed_at_the_timeout(tmp_path: Path) -> None:
    path = tmp_path / "executor"
    digest = _write_fake_binary(path, "sleep 30")
    identity = identify_executor_binary(path, expected_sha256=digest)

    with pytest.raises(ExecutorHandshakeError):
        run_capabilities_handshake(identity, timeout_seconds=0.5)


def test_an_oversized_answer_is_refused(tmp_path: Path) -> None:
    path = tmp_path / "executor"
    digest = _write_fake_binary(
        path,
        f"head -c {MAX_CAPABILITIES_BYTES + 1} /dev/zero | tr '\\0' 'a'",
    )
    identity = identify_executor_binary(path, expected_sha256=digest)

    with pytest.raises(ExecutorHandshakeError):
        run_capabilities_handshake(identity)


def test_the_handshake_environment_is_stripped(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A worker env can carry resolvable secrets (env:// refs are real
    ``*_CPANEL_*`` variables). None of it may reach the binary."""
    monkeypatch.setenv("SOURCE_CPANEL_SSH_PASSWORD", "env-sentinel-0xBEEF")
    prefix, suffix = _VALID_DOC.split("1.2.3")
    body = (
        'v="${SOURCE_CPANEL_SSH_PASSWORD:-clean}"\n'
        f"printf '%s' '{prefix}'\"$v\"'{suffix}'"
    )
    path = tmp_path / "executor"
    digest = _write_fake_binary(path, body)
    identity = identify_executor_binary(path, expected_sha256=digest)

    caps = run_capabilities_handshake(identity)

    assert caps.executor_version == "clean"


def test_the_golden_corpus_document_parses_into_the_handshake_model() -> None:
    """The same bytes the Go emitter produces (shared corpus golden) are what
    ensure_compatible consumes: the chain binary -> document -> decision is
    closed without inventing a second parser."""
    root = Path(__file__).resolve()
    for parent in root.parents:
        if (parent / "go.mod").is_file():
            golden = parent / "testdata" / "execution-contract" / "valid" / "capabilities-emitted.json"
            break
    else:  # pragma: no cover
        pytest.fail("repository root not found")

    caps = parse_capabilities(golden.read_bytes())

    ensure_compatible(
        caps,
        require_password=True,
        require_private_key=True,
        require_encrypted_private_key=True,
    )


# --- compatibility: a typed, fail-closed decision ----------------------------


def _caps(**ssh_overrides: bool):
    doc = json.loads(_VALID_DOC)
    doc["ssh"].update(ssh_overrides)
    return parse_capabilities(json.dumps(doc))


def test_a_compatible_executor_passes() -> None:
    ensure_compatible(_caps(), require_password=True)


def test_a_missing_contract_version_refuses_the_launch() -> None:
    doc = json.loads(_VALID_DOC)
    doc["contract"]["spec"] = [2]

    with pytest.raises(ExecutorCompatibilityError):
        ensure_compatible(parse_capabilities(json.dumps(doc)))


def test_strict_host_config_is_always_required() -> None:
    """The workspace writes host.yaml assuming unknown fields are hard errors;
    a lenient parser would silently ignore what the operator believes holds."""
    with pytest.raises(ExecutorCompatibilityError):
        ensure_compatible(_caps(strict_host_config=False))


def test_known_hosts_via_home_is_always_required() -> None:
    """Pinned trust exists only because the engine reads HOME/.ssh/known_hosts;
    without it the workspace's known_hosts is never consulted."""
    with pytest.raises(ExecutorCompatibilityError):
        ensure_compatible(_caps(known_hosts_via_home=False))


def test_a_password_run_requires_password_support() -> None:
    with pytest.raises(ExecutorCompatibilityError):
        ensure_compatible(_caps(password=False), require_password=True)

    ensure_compatible(_caps(password=False))  # not needed: not required


def test_a_key_run_requires_private_key_support() -> None:
    with pytest.raises(ExecutorCompatibilityError):
        ensure_compatible(_caps(private_key=False), require_private_key=True)


def test_a_passphrase_run_requires_encrypted_key_support() -> None:
    with pytest.raises(ExecutorCompatibilityError):
        ensure_compatible(
            _caps(encrypted_private_key=False), require_encrypted_private_key=True
        )


def test_the_compatibility_error_names_the_missing_capability() -> None:
    with pytest.raises(ExecutorCompatibilityError) as excinfo:
        ensure_compatible(_caps(private_key=False), require_private_key=True)

    assert "private_key" in str(excinfo.value)


# --- hygiene ------------------------------------------------------------------


def test_identity_is_computed_from_real_bytes_not_from_the_mode(
    tmp_path: Path,
) -> None:
    """chmod alone must not change the digest: content is the anchor."""
    path, digest = _fake_ok(tmp_path)
    os.chmod(path, stat.S_IRWXU)

    identity = identify_executor_binary(path, expected_sha256=digest)

    assert identity.sha256 == digest
