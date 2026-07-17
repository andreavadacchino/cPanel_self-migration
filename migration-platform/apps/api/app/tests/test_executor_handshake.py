"""Stage a verified artifact, ask it, decide — fail-closed at every step.

Three separations, each proven here:

* **artifact** — the deployment path is *input*, not identity. The source is
  opened once, copied into a private root while being hashed, and only that
  copy is ever executed. What happens to the source afterwards — replaced,
  rewritten, deleted — cannot change what runs, because nothing reads it again.
* **handshake** — the artifact is asked ``capabilities`` under real bounds: no
  shell, no PATH, stdin closed, a stripped environment (a worker env carries
  ``*_CPANEL_*`` secrets), a timeout, and stdout limited while it is read.
* **compatibility** — a typed decision over the parsed document: the contract
  versions the platform speaks must appear in every list, and the SSH facts a
  run needs must be present. Anything else refuses the launch.

The fake binaries here are small scripts: the cross-language proof that the
REAL binary emits a valid document is the shared corpus golden
(``testdata/execution-contract/valid/capabilities-emitted.json``), asserted by
both ``internal/executioncontract`` and the domain tests. The real binary is
exercised end-to-end in ``test_executor_handshake_real_binary.py``.
"""

from __future__ import annotations

import hashlib
import json
import os
import stat
from pathlib import Path

import pytest
from adapters.executor_artifact import (
    ARTIFACT_NAME,
    ExecutorArtifactCleanupError,
    ExecutorArtifactError,
    ExecutorDeployment,
    prepare_verified_executor,
)
from adapters.executor_handshake import (
    MAX_CAPABILITIES_BYTES,
    ExecutorCompatibilityError,
    ExecutorHandshakeError,
    ensure_compatible,
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


def _doc(version: str) -> str:
    return _VALID_DOC.replace('"1.2.3"', f'"{version}"')


def _write_fake_binary(path: Path, body: str) -> str:
    """Create an executable script and return its content digest."""
    script = "#!/bin/sh\n" + body + "\n"
    path.write_text(script)
    path.chmod(0o700)
    return hashlib.sha256(script.encode()).hexdigest()


def _emit(doc: str) -> str:
    return f"printf '%s' '{doc}'"


@pytest.fixture()
def runtime_root(tmp_path: Path) -> Path:
    root = tmp_path / "runtime"
    root.mkdir(mode=0o700)
    return root


def _deployment(source: Path, digest: str, runtime_root: Path) -> ExecutorDeployment:
    return ExecutorDeployment(
        source_path=source, expected_sha256=digest, runtime_root=runtime_root
    )


def _staged(tmp_path: Path, runtime_root: Path, doc: str = _VALID_DOC):
    source = tmp_path / "executor"
    digest = _write_fake_binary(source, _emit(doc))
    return prepare_verified_executor(_deployment(source, digest, runtime_root))


def _mode(path: Path) -> int:
    return stat.S_IMODE(os.stat(path).st_mode)


# --- what is hashed is what runs --------------------------------------------
#
# The defect this design removes: hashing a file and then handing its PATH to
# subprocess makes the kernel resolve that path a second time, so the verified
# bytes and the executed bytes are two different questions. Every test below
# attacks the source AFTER staging; none of them may change the answer.


def test_replacing_the_source_after_staging_does_not_change_what_runs(
    tmp_path: Path, runtime_root: Path
) -> None:
    """The load-bearing test: A was verified, so B must never answer."""
    source = tmp_path / "executor"
    digest_a = _write_fake_binary(source, _emit(_doc("trusted-A")))
    replacement = tmp_path / "replacement"
    _write_fake_binary(replacement, _emit(_doc("replaced-B")))

    with prepare_verified_executor(
        _deployment(source, digest_a, runtime_root)
    ) as artifact:
        os.replace(replacement, source)  # atomic: same path, different inode

        caps = run_capabilities_handshake(artifact)

    assert caps.executor_version == "trusted-A"


def test_symlinking_the_source_away_after_staging_does_not_change_what_runs(
    tmp_path: Path, runtime_root: Path
) -> None:
    source = tmp_path / "executor"
    digest_a = _write_fake_binary(source, _emit(_doc("trusted-A")))
    evil = tmp_path / "evil"
    _write_fake_binary(evil, _emit(_doc("linked-B")))

    with prepare_verified_executor(
        _deployment(source, digest_a, runtime_root)
    ) as artifact:
        source.unlink()
        source.symlink_to(evil)

        caps = run_capabilities_handshake(artifact)

    assert caps.executor_version == "trusted-A"


def test_editing_the_source_in_place_after_staging_does_not_change_what_runs(
    tmp_path: Path, runtime_root: Path
) -> None:
    source = tmp_path / "executor"
    digest_a = _write_fake_binary(source, _emit(_doc("trusted-A")))

    with prepare_verified_executor(
        _deployment(source, digest_a, runtime_root)
    ) as artifact:
        _write_fake_binary(source, _emit(_doc("edited-B")))

        caps = run_capabilities_handshake(artifact)

    assert caps.executor_version == "trusted-A"


def test_deleting_the_source_after_staging_does_not_break_the_handshake(
    tmp_path: Path, runtime_root: Path
) -> None:
    """The artifact is ours; the deployment path was only ever input."""
    source = tmp_path / "executor"
    digest_a = _write_fake_binary(source, _emit(_doc("trusted-A")))

    with prepare_verified_executor(
        _deployment(source, digest_a, runtime_root)
    ) as artifact:
        source.unlink()

        caps = run_capabilities_handshake(artifact)

    assert caps.executor_version == "trusted-A"


def test_the_handshake_runs_the_artifact_not_the_source(
    tmp_path: Path, runtime_root: Path
) -> None:
    """Asserted on the argv the runner is actually given."""
    import adapters.executor_handshake as mod

    seen: list[str] = []
    real = mod.run_bounded_process

    def spy(argv, **kwargs):
        seen.append(argv[0])
        return real(argv, **kwargs)

    source = tmp_path / "executor"
    digest = _write_fake_binary(source, _emit(_VALID_DOC))
    mod.run_bounded_process = spy  # type: ignore[assignment]
    try:
        with prepare_verified_executor(
            _deployment(source, digest, runtime_root)
        ) as artifact:
            run_capabilities_handshake(artifact)
            assert seen == [str(artifact.executable_path)]
            assert str(source) not in seen
    finally:
        mod.run_bounded_process = real  # type: ignore[assignment]


def test_the_artifact_is_byte_identical_to_what_was_hashed(
    tmp_path: Path, runtime_root: Path
) -> None:
    source = tmp_path / "executor"
    digest = _write_fake_binary(source, _emit(_VALID_DOC))
    original = source.read_bytes()

    with prepare_verified_executor(
        _deployment(source, digest, runtime_root)
    ) as artifact:
        assert artifact.executable_path.read_bytes() == original
        assert hashlib.sha256(artifact.executable_path.read_bytes()).hexdigest() == digest
        assert artifact.sha256 == digest


# --- staging: refusals ------------------------------------------------------


def test_a_digest_mismatch_is_refused_and_leaves_nothing_behind(
    tmp_path: Path, runtime_root: Path
) -> None:
    source = tmp_path / "executor"
    _write_fake_binary(source, _emit(_VALID_DOC))
    wrong = "0" * 64

    with pytest.raises(ExecutorArtifactError) as excinfo:
        with prepare_verified_executor(_deployment(source, wrong, runtime_root)):
            pass

    assert list(runtime_root.iterdir()) == [], "a rejected artifact must not persist"
    text = str(excinfo.value)
    assert wrong not in text  # neither digest belongs in the message
    assert str(source) in text


@pytest.mark.parametrize("bad", ["", "zz" * 32, "abc123", "0" * 63, "0" * 65])
def test_a_malformed_digest_is_refused(
    tmp_path: Path, runtime_root: Path, bad: str
) -> None:
    source = tmp_path / "executor"
    _write_fake_binary(source, _emit(_VALID_DOC))

    with pytest.raises(ExecutorArtifactError):
        with prepare_verified_executor(_deployment(source, bad, runtime_root)):
            pass

    assert list(runtime_root.iterdir()) == []


def test_a_missing_source_is_refused(tmp_path: Path, runtime_root: Path) -> None:
    with pytest.raises(ExecutorArtifactError):
        with prepare_verified_executor(
            _deployment(tmp_path / "absent", "0" * 64, runtime_root)
        ):
            pass

    assert list(runtime_root.iterdir()) == []


def test_a_symlinked_source_is_refused_even_when_its_target_matches(
    tmp_path: Path, runtime_root: Path
) -> None:
    """Only refusing to FOLLOW the link can reject this: the digest is right."""
    real = tmp_path / "real"
    digest = _write_fake_binary(real, _emit(_VALID_DOC))
    link = tmp_path / "link"
    link.symlink_to(real)

    with pytest.raises(ExecutorArtifactError):
        with prepare_verified_executor(_deployment(link, digest, runtime_root)):
            pass

    assert list(runtime_root.iterdir()) == []


def test_a_directory_source_is_refused(tmp_path: Path, runtime_root: Path) -> None:
    with pytest.raises(ExecutorArtifactError):
        with prepare_verified_executor(_deployment(tmp_path, "0" * 64, runtime_root)):
            pass


def test_a_non_executable_source_is_refused(
    tmp_path: Path, runtime_root: Path
) -> None:
    source = tmp_path / "executor"
    source.write_text("#!/bin/sh\n")
    source.chmod(0o600)
    digest = hashlib.sha256(source.read_bytes()).hexdigest()

    with pytest.raises(ExecutorArtifactError):
        with prepare_verified_executor(_deployment(source, digest, runtime_root)):
            pass

    assert list(runtime_root.iterdir()) == []


def test_a_symlinked_runtime_root_is_refused(tmp_path: Path) -> None:
    real = tmp_path / "real"
    real.mkdir(mode=0o700)
    link = tmp_path / "link"
    link.symlink_to(real, target_is_directory=True)
    source = tmp_path / "executor"
    digest = _write_fake_binary(source, _emit(_VALID_DOC))

    with pytest.raises(ExecutorArtifactError):
        with prepare_verified_executor(_deployment(source, digest, link)):
            pass


def test_a_world_writable_runtime_root_without_sticky_is_refused(
    tmp_path: Path,
) -> None:
    root = tmp_path / "open"
    root.mkdir()
    os.chmod(root, 0o777)
    source = tmp_path / "executor"
    digest = _write_fake_binary(source, _emit(_VALID_DOC))

    with pytest.raises(ExecutorArtifactError):
        with prepare_verified_executor(_deployment(source, digest, root)):
            pass


def test_a_read_error_during_the_copy_leaves_nothing_behind(
    tmp_path: Path, runtime_root: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import adapters.executor_artifact as mod

    source = tmp_path / "executor"
    digest = _write_fake_binary(source, _emit(_VALID_DOC))

    def explode(fd: int, n: int) -> bytes:
        raise OSError("input/output error")

    monkeypatch.setattr(mod.os, "read", explode)

    with pytest.raises(OSError):
        with prepare_verified_executor(_deployment(source, digest, runtime_root)):
            pass

    monkeypatch.undo()
    assert list(runtime_root.iterdir()) == []


def test_a_write_error_during_the_copy_leaves_nothing_behind(
    tmp_path: Path, runtime_root: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import adapters.executor_artifact as mod

    source = tmp_path / "executor"
    digest = _write_fake_binary(source, _emit(_VALID_DOC))

    def explode(fd: int, data: bytes) -> int:
        raise OSError("no space left on device")

    monkeypatch.setattr(mod.os, "write", explode)

    with pytest.raises(OSError):
        with prepare_verified_executor(_deployment(source, digest, runtime_root)):
            pass

    monkeypatch.undo()
    assert list(runtime_root.iterdir()) == []


def test_a_short_write_is_completed_not_truncated(
    tmp_path: Path, runtime_root: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """os.write may write fewer bytes than asked. Ignoring that would put a
    truncated artifact on disk under a digest of the full content."""
    import adapters.executor_artifact as mod

    source = tmp_path / "executor"
    digest = _write_fake_binary(source, _emit(_VALID_DOC))
    original = source.read_bytes()
    real_write = os.write

    def dribble(fd: int, data: bytes) -> int:
        return real_write(fd, data[:1])  # one byte at a time

    monkeypatch.setattr(mod.os, "write", dribble)

    with prepare_verified_executor(
        _deployment(source, digest, runtime_root)
    ) as artifact:
        assert artifact.executable_path.read_bytes() == original


# --- the artifact's shape ---------------------------------------------------


def test_the_artifact_root_is_private_and_the_file_is_read_execute(
    tmp_path: Path, runtime_root: Path
) -> None:
    with _staged(tmp_path, runtime_root) as artifact:
        assert _mode(artifact.root) == 0o700
        assert _mode(artifact.executable_path) == 0o500, (
            "a writable artifact is one that can stop being what was verified"
        )


def test_the_artifact_modes_do_not_depend_on_a_permissive_umask(
    tmp_path: Path, runtime_root: Path
) -> None:
    old = os.umask(0o000)
    try:
        with _staged(tmp_path, runtime_root) as artifact:
            assert _mode(artifact.root) == 0o700
            assert _mode(artifact.executable_path) == 0o500
    finally:
        os.umask(old)


def test_the_artifact_name_is_constant_and_the_root_is_unpredictable(
    tmp_path: Path, runtime_root: Path
) -> None:
    source = tmp_path / "executor"
    digest = _write_fake_binary(source, _emit(_VALID_DOC))

    with prepare_verified_executor(_deployment(source, digest, runtime_root)) as first:
        with prepare_verified_executor(
            _deployment(source, digest, runtime_root)
        ) as second:
            assert first.root != second.root  # system randomness, not a hash
            for artifact in (first, second):
                assert artifact.executable_path.name == ARTIFACT_NAME
                assert artifact.executable_path.parent == artifact.root
                # The name must leak nothing: not the digest, not the source.
                assert digest not in artifact.root.name
                assert "executor-" in artifact.root.name


def test_the_artifact_repr_carries_no_secret(tmp_path: Path, runtime_root: Path) -> None:
    with _staged(tmp_path, runtime_root) as artifact:
        text = repr(artifact)

    assert "PRIVATE KEY" not in text
    assert str(artifact.executable_path) in text  # paths are administrative


# --- lifecycle --------------------------------------------------------------


def test_the_artifact_is_gone_after_a_normal_exit(
    tmp_path: Path, runtime_root: Path
) -> None:
    with _staged(tmp_path, runtime_root) as artifact:
        root = artifact.root
        assert root.exists()

    assert not root.exists()
    assert list(runtime_root.iterdir()) == []


def test_the_artifact_is_gone_after_an_exception_inside_the_block(
    tmp_path: Path, runtime_root: Path
) -> None:
    root = None
    with pytest.raises(RuntimeError):
        with _staged(tmp_path, runtime_root) as artifact:
            root = artifact.root
            raise RuntimeError("boom")

    assert root is not None and not root.exists()
    assert list(runtime_root.iterdir()) == []


def test_a_cleanup_failure_on_a_normal_exit_is_raised(
    tmp_path: Path, runtime_root: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import adapters.executor_artifact as mod

    def denied(path: object, *args: object, **kwargs: object) -> None:
        raise PermissionError(13, "Permission denied", str(path))

    with pytest.raises(ExecutorArtifactCleanupError):
        with _staged(tmp_path, runtime_root):
            monkeypatch.setattr(mod.shutil, "rmtree", denied)

    monkeypatch.undo()


def test_a_body_error_and_a_cleanup_failure_are_both_observable(
    tmp_path: Path, runtime_root: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import adapters.executor_artifact as mod

    def denied(path: object, *args: object, **kwargs: object) -> None:
        raise PermissionError(13, "Permission denied", str(path))

    with pytest.raises(ExceptionGroup) as excinfo:
        with _staged(tmp_path, runtime_root):
            monkeypatch.setattr(mod.shutil, "rmtree", denied)
            raise RuntimeError("boom")

    monkeypatch.undo()
    assert excinfo.group_contains(RuntimeError, match="boom")
    assert excinfo.group_contains(mod.ExecutorArtifactCleanupError)


def test_an_artifact_root_replaced_by_a_symlink_is_not_followed(
    tmp_path: Path, runtime_root: Path
) -> None:
    import shutil as _shutil

    target = tmp_path / "victim"
    target.mkdir()
    canary = target / "canary"
    canary.write_text("intact")

    with pytest.raises(ExecutorArtifactCleanupError):
        with _staged(tmp_path, runtime_root) as artifact:
            _shutil.rmtree(artifact.root)
            os.symlink(target, artifact.root)

    assert canary.read_text() == "intact"


# --- handshake --------------------------------------------------------------


def test_a_well_behaved_artifact_answers_the_handshake(
    tmp_path: Path, runtime_root: Path
) -> None:
    with _staged(tmp_path, runtime_root) as artifact:
        caps = run_capabilities_handshake(artifact)

    assert caps.executor_version == "1.2.3"
    assert caps.ssh.private_key is True


def test_the_handshake_passes_the_capabilities_argument(
    tmp_path: Path, runtime_root: Path
) -> None:
    source = tmp_path / "executor"
    digest = _write_fake_binary(
        source,
        f'if [ "$1" = "capabilities" ]; then printf \'%s\' \'{_VALID_DOC}\'; else exit 7; fi',
    )

    with prepare_verified_executor(
        _deployment(source, digest, runtime_root)
    ) as artifact:
        caps = run_capabilities_handshake(artifact)

    assert caps.format_version == 1


def test_garbage_output_is_a_handshake_error(
    tmp_path: Path, runtime_root: Path
) -> None:
    source = tmp_path / "executor"
    digest = _write_fake_binary(source, "printf 'not json at all'")

    with prepare_verified_executor(
        _deployment(source, digest, runtime_root)
    ) as artifact:
        with pytest.raises(ExecutorHandshakeError):
            run_capabilities_handshake(artifact)


def test_a_nonzero_exit_is_refused_without_echoing_stderr(
    tmp_path: Path, runtime_root: Path
) -> None:
    source = tmp_path / "executor"
    digest = _write_fake_binary(source, "echo 'stderr-sentinel-0xTEA' >&2; exit 3")

    with prepare_verified_executor(
        _deployment(source, digest, runtime_root)
    ) as artifact:
        with pytest.raises(ExecutorHandshakeError) as excinfo:
            run_capabilities_handshake(artifact)

    text = str(excinfo.value)
    assert "3" in text
    assert "stderr-sentinel-0xTEA" not in text


def test_a_hanging_artifact_is_killed_at_the_timeout(
    tmp_path: Path, runtime_root: Path
) -> None:
    source = tmp_path / "executor"
    digest = _write_fake_binary(source, "sleep 30")

    with prepare_verified_executor(
        _deployment(source, digest, runtime_root)
    ) as artifact:
        with pytest.raises(ExecutorHandshakeError):
            run_capabilities_handshake(artifact, timeout_seconds=0.5)


def test_an_oversized_answer_is_refused_while_it_is_read(
    tmp_path: Path, runtime_root: Path
) -> None:
    """`yes` never stops: only a reader that halts AT the limit returns here."""
    source = tmp_path / "executor"
    digest = _write_fake_binary(source, "yes AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

    with prepare_verified_executor(
        _deployment(source, digest, runtime_root)
    ) as artifact:
        with pytest.raises(ExecutorHandshakeError) as excinfo:
            run_capabilities_handshake(artifact, timeout_seconds=30)

    assert str(MAX_CAPABILITIES_BYTES) in str(excinfo.value)


def test_the_handshake_environment_is_stripped(
    tmp_path: Path, runtime_root: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A worker env carries resolvable secrets (env:// refs are real
    ``*_CPANEL_*`` variables). None of it may reach the child."""
    monkeypatch.setenv("SOURCE_CPANEL_SSH_PASSWORD", "env-sentinel-0xBEEF")
    prefix, suffix = _VALID_DOC.split("1.2.3")
    body = (
        'v="${SOURCE_CPANEL_SSH_PASSWORD:-clean}"\n'
        f"printf '%s' '{prefix}'\"$v\"'{suffix}'"
    )
    source = tmp_path / "executor"
    digest = _write_fake_binary(source, body)

    with prepare_verified_executor(
        _deployment(source, digest, runtime_root)
    ) as artifact:
        caps = run_capabilities_handshake(artifact)

    assert caps.executor_version == "clean"


def test_the_golden_corpus_document_parses_into_the_handshake_model() -> None:
    """The same bytes the Go emitter produces (shared corpus golden) are what
    ensure_compatible consumes: binary -> document -> decision, closed without
    inventing a second parser."""
    root = Path(__file__).resolve()
    for parent in root.parents:
        if (parent / "go.mod").is_file():
            golden = (
                parent
                / "testdata"
                / "execution-contract"
                / "valid"
                / "capabilities-emitted.json"
            )
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
