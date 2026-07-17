"""The chain against the REAL Go binary, not a shell script pretending to be one.

Fake binaries prove the platform's logic; they cannot prove that the engine we
ship emits what the platform accepts. This module builds the actual binary,
stages it, attacks the source, and asserts the artifact still answers — and
that what it answers is byte-identical to the shared corpus golden.

Opt-in by necessity, not by preference: it needs a Go toolchain. CI has one
(the same workflow runs `go build ./...`), so it runs there; a machine without
Go skips rather than lying. It opens no network and runs no migration.
"""

from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import sys
from pathlib import Path

import pytest
from adapters.executor_artifact import ExecutorDeployment, prepare_verified_executor
from adapters.executor_handshake import ensure_compatible, run_capabilities_handshake

pytestmark = pytest.mark.skipif(
    shutil.which("go") is None, reason="the Go toolchain is required to build the executor"
)


def _repo_root() -> Path:
    here = Path(__file__).resolve()
    for parent in here.parents:
        if (parent / "go.mod").is_file():
            return parent
    raise RuntimeError("repository root not found: no go.mod above this file")


@pytest.fixture(scope="module")
def real_binary(tmp_path_factory: pytest.TempPathFactory) -> tuple[Path, str]:
    """Build the engine once and return (path, digest)."""
    root = _repo_root()
    out = tmp_path_factory.mktemp("engine") / "cpanel-self-migration"
    build = subprocess.run(  # noqa: S603 - fixed argv, repository toolchain
        ["go", "build", "-o", str(out), "./cmd/cpanel-self-migration"],
        cwd=root,
        capture_output=True,
        timeout=600,
        check=False,
    )
    if build.returncode != 0:
        pytest.fail(f"go build failed: {build.stderr.decode()[:2000]}")
    return out, hashlib.sha256(out.read_bytes()).hexdigest()


@pytest.fixture()
def runtime_root(tmp_path: Path) -> Path:
    root = tmp_path / "runtime"
    root.mkdir(mode=0o700)
    return root


def test_the_real_binary_completes_the_whole_chain(
    real_binary: tuple[Path, str], runtime_root: Path
) -> None:
    binary, digest = real_binary
    deployment = ExecutorDeployment(
        source_path=binary, expected_sha256=digest, runtime_root=runtime_root
    )

    with prepare_verified_executor(deployment) as artifact:
        capabilities = run_capabilities_handshake(artifact)
        ensure_compatible(
            capabilities,
            require_password=True,
            require_private_key=True,
            require_encrypted_private_key=True,
        )
        root = artifact.root

    # The whole increment stops here: no execute, no actor, no network.
    assert not root.exists(), "the artifact must not outlive the context"


def test_the_real_binary_answers_from_the_artifact_after_the_source_is_replaced(
    real_binary: tuple[Path, str], tmp_path: Path, runtime_root: Path
) -> None:
    """The defect, closed against the real engine: stage it, swap the source
    for something else entirely, and the verified artifact still answers."""
    binary, digest = real_binary
    source = tmp_path / "deployed-executor"
    shutil.copy2(binary, source)
    os.chmod(source, 0o700)
    deployment = ExecutorDeployment(
        source_path=source, expected_sha256=digest, runtime_root=runtime_root
    )

    with prepare_verified_executor(deployment) as artifact:
        impostor = tmp_path / "impostor"
        impostor.write_text("#!/bin/sh\nprintf 'not the engine'\n")
        impostor.chmod(0o700)
        os.replace(impostor, source)

        capabilities = run_capabilities_handshake(artifact)

    assert capabilities.contract.spec == (1,)
    assert capabilities.ssh.known_hosts_via_home is True


def test_the_real_binary_answers_from_the_artifact_after_the_source_is_deleted(
    real_binary: tuple[Path, str], tmp_path: Path, runtime_root: Path
) -> None:
    binary, digest = real_binary
    source = tmp_path / "deployed-executor"
    shutil.copy2(binary, source)
    os.chmod(source, 0o700)
    deployment = ExecutorDeployment(
        source_path=source, expected_sha256=digest, runtime_root=runtime_root
    )

    with prepare_verified_executor(deployment) as artifact:
        source.unlink()
        capabilities = run_capabilities_handshake(artifact)

    assert capabilities.executor_version  # a real build version, whatever it is


def test_the_artifact_emits_exactly_the_shared_golden(
    real_binary: tuple[Path, str], runtime_root: Path
) -> None:
    """A local build reports 0.0.0-dev, which is what the golden fixture pins.

    This is the cross-language chain closed at the byte level: the corpus
    golden is not a hand-written guess, it is what this binary prints — and the
    Python validator accepts it because the Go emitter produced it.
    """
    binary, digest = real_binary
    deployment = ExecutorDeployment(
        source_path=binary, expected_sha256=digest, runtime_root=runtime_root
    )
    golden = (
        _repo_root() / "testdata" / "execution-contract" / "valid" / "capabilities-emitted.json"
    )

    with prepare_verified_executor(deployment) as artifact:
        emitted = subprocess.run(  # noqa: S603 - the verified artifact, fixed argv
            [str(artifact.executable_path), "capabilities"],
            capture_output=True,
            timeout=60,
            check=True,
            env={},
        ).stdout

    assert emitted == golden.read_bytes(), (
        "the engine's capabilities output drifted from the shared corpus golden"
    )


def test_a_tampered_source_is_refused_against_the_real_digest(
    real_binary: tuple[Path, str], tmp_path: Path, runtime_root: Path
) -> None:
    binary, digest = real_binary
    tampered = tmp_path / "tampered"
    shutil.copy2(binary, tampered)
    with open(tampered, "ab") as handle:
        handle.write(b"\x00")  # one byte is enough
    os.chmod(tampered, 0o700)

    from adapters.executor_artifact import ExecutorArtifactError

    with pytest.raises(ExecutorArtifactError):
        with prepare_verified_executor(
            ExecutorDeployment(
                source_path=tampered, expected_sha256=digest, runtime_root=runtime_root
            )
        ):
            pass

    assert list(runtime_root.iterdir()) == []
