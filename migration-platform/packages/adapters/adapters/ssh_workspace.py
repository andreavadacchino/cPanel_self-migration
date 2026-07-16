"""Build the ephemeral, private workspace the Go engine will one day read.

This is the only place a resolved secret touches a disk. Everything here exists
to bound that: an unpredictable 0700 directory outside the repository, 0600 files
created with ``O_CREAT|O_EXCL|O_NOFOLLOW`` (so a planted symlink cannot redirect a
private key), constant file names never derived from a row, and removal on every
exit path — including the failing ones, because a half-built workspace is still a
workspace full of secrets.

Boundary: this module receives only validated DTOs and already-resolved secrets.
It opens no socket, runs no subprocess, reads no database, and never calls
``ssh-keyscan``. It builds files and deletes them.

**Two engine facts drive the layout, both verified in the Go source, not assumed:**

*``known_hosts`` has no configuration field.* ``internal/sshx/pool.go:49-58``
resolves it as ``os.UserHomeDir() + "/.ssh/known_hosts"`` and every production
call site passes an empty custom path. So the file is written to
``<root>/.ssh/known_hosts`` and the executor that eventually launches the binary
will point ``HOME`` at the workspace root. That is the whole reason the engine's
TOFU (``AcceptNewHostKey``) cannot degrade into "trust anything on first run":
the entry is already there, and a different key is then a hard mismatch. No Go
production code is changed to achieve this.

*Secrets in ``host.yaml`` are partly unavoidable.* ``internal/config`` accepts a
private key only as a path (``ssh_key_path``), which is the better transport and
is what we use — the material goes in a 0600 file of its own. But a password
(``ssh_pass``) and a passphrase (``ssh_key_passphrase``) are plain string fields:
the parser has no file-reference, env-var or secret-file form for them. So they
are written literally into ``host.yaml``, which is 0600, lives only inside the
workspace, is never logged, never copied into an artifact, and is deleted at
cleanup. Widening that transport means changing the Go parser — a separate,
deliberate increment, not something to smuggle in here.

Python strings cannot be reliably zeroized. The strong boundary this module
offers is the filesystem one: private, ephemeral, deterministic removal.
"""

from __future__ import annotations

import contextlib
import ipaddress
import os
import re
import shutil
import stat
import tempfile
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path

import yaml

from adapters.ssh_runtime import AUTH_METHOD_PRIVATE_KEY, SshRuntimeSnapshot

__all__ = [
    "DEFAULT_TIMEOUT",
    "HOST_CONFIG_NAME",
    "KNOWN_HOSTS_NAME",
    "SSH_DIR_NAME",
    "SshWorkspace",
    "WorkspaceBuildError",
    "WorkspaceSecurityError",
    "build_ssh_workspace",
    "known_hosts_line",
    "render_host_config",
]

# Constant names: never derived from host, username, label or any row value, so a
# hostile column cannot steer a write. Paired with O_EXCL|O_NOFOLLOW below.
HOST_CONFIG_NAME = "host.yaml"
KNOWN_HOSTS_NAME = "known_hosts"
SSH_DIR_NAME = ".ssh"
SOURCE_KEY_NAME = "source_key"
DEST_KEY_NAME = "dest_key"

_WORKSPACE_PREFIX = "migration-ssh-"

# A known_hosts record is whitespace-delimited, and knownhosts.parseLine treats
# everything after the key blob as a comment. So a host carrying a space does not
# corrupt the file — it silently appends a SECOND, well-formed record whose key is
# the attacker's, and the real key degrades into that record's comment. Either
# record then satisfies the lookup, which inverts the whole point of pinning.
#
# The public key earns its safety from parse_host_key; the host has no such
# authority — the API's _clean_host strips scheme/userinfo/path/port but never
# constrains the charset, so a hostile host reaches here intact. This grammar is
# that authority: a hostname label set, or an IP literal, and nothing else.
_HOSTNAME = re.compile(
    r"^(?!-)[A-Za-z0-9-]{1,63}(?<!-)(\.(?!-)[A-Za-z0-9-]{1,63}(?<!-))*\.?$"
)
_MAX_HOST = 255

# internal/config applies NO defaults and rejects timeout <= 0, so the field must
# always be emitted. The value is the engine's dial timeout, not a policy this PR
# owns; it is a constant until an operator-facing setting genuinely needs it.
DEFAULT_TIMEOUT = "30s"

_STANDARD_SSH_PORT = 22


class WorkspaceBuildError(Exception):
    """The workspace could not be assembled from the given inputs.

    Names no file content.
    """


class WorkspaceSecurityError(Exception):
    """The runtime root is not a safe place to materialize secrets.

    Names the path (administrative, already known to the operator) and never any
    file content.
    """


@dataclass(frozen=True)
class SshWorkspace:
    """Paths into a built workspace. Carries no secret, by construction.

    ``repr`` is safe because there is nothing sensitive to hide: only paths, an
    id and the host coordinates. The secrets are in the files, which is the
    point — and ``cleanup`` is what makes that acceptable.
    """

    root: Path
    host_config_path: Path
    known_hosts_path: Path
    endpoint_id: int
    host: str
    port: int
    fingerprint_sha256: str
    source_key_path: Path | None = None
    dest_key_path: Path | None = None

    def cleanup(self) -> None:
        """Remove the whole workspace. Idempotent, and safe to call twice.

        ``ignore_errors`` covers the already-gone case (a second call, or a test
        that removed it). ``shutil.rmtree`` cannot escape the workspace here: the
        root is a real directory we created ourselves under a verified root, and
        rmtree does not follow a symlinked *root* (it raises instead).
        """
        shutil.rmtree(self.root, ignore_errors=True)


def _validated_host(host: str) -> str:
    """Prove ``host`` is a bare hostname or IP literal, or refuse.

    Fail-closed and deliberately strict: a known_hosts record is
    whitespace-delimited, so anything looser is an injection primitive (see
    _HOSTNAME). An IPv6 literal is returned unbracketed, which is the form both
    Normalize and this module's host.yaml use.
    """
    if not host or len(host) > _MAX_HOST:
        raise WorkspaceSecurityError("endpoint host is empty or too long")
    try:
        # Accepts v4 and v6; rejects anything with a space, a slash or a newline.
        return str(ipaddress.ip_address(host))
    except ValueError:
        pass
    if not _HOSTNAME.match(host):
        raise WorkspaceSecurityError(
            "endpoint host is not a bare hostname or IP literal and cannot be "
            "pinned safely"
        )
    return host


def _known_hosts_address(host: str, port: int) -> str:
    """The one authority on the address field a ``known_hosts`` record carries.

    Mirrors ``golang.org/x/crypto/ssh/knownhosts.Normalize``, which the engine
    applies to what it writes and agrees with on lookup: the port is dropped at
    22 and the host is bracketed otherwise. Getting this wrong does not fail
    loudly — it silently misses the entry and hands the connection back to TOFU.

    Every consumer — the emitted line, the collision check, the deduplication —
    goes through here, so "same address" always means the exact string the file
    would carry: ``_validated_host`` canonicalizes an IP literal (two textual
    spellings of one IPv6 address become one literal), and the port keeps two
    services on one host distinct. No DNS, no aliasing between hostnames.
    """
    if not 1 <= port <= 65535:
        raise WorkspaceSecurityError("endpoint port is outside 1-65535")
    bare = _validated_host(host)
    return bare if port == _STANDARD_SSH_PORT else f"[{bare}]:{port}"


def known_hosts_line(host: str, port: int, public_key: str) -> str:
    """One OpenSSH ``known_hosts`` entry, in the engine's own dialect.

    ``host`` is validated (through ``_known_hosts_address``), not trusted: it
    comes from a mutable column with no charset constraint, and a space in it
    would append a second, attacker-keyed record rather than corrupt the file.
    ``public_key`` needs no such check — it is the canonical two-token line
    parse_host_key already proved: the key itself, never the fingerprint, never
    re-assembled from parts.
    """
    return f"{_known_hosts_address(host, port)} {public_key}"


def _planned_known_hosts_entries(
    source: SshRuntimeSnapshot,
    destination: SshRuntimeSnapshot | None,
) -> tuple[str, ...]:
    """Decide every trust entry before anything exists on disk. Pure.

    At most one trust identity per normalized address: the same address backed
    by the same canonical key is deduplicated to a single entry; the same
    address backed by two different keys is refused. Writing both records would
    not fail — ``knownhosts.checkAddr`` accepts *either* key for the address
    (proven against the real parser in
    ``internal/sshx/knownhosts_multikey_test.go``), silently widening one
    endpoint's allowlist with the other's pin.

    Runs before ``mkdtemp``: an impossible trust configuration must not
    materialize a private key only to clean it up. The error names the address
    (administrative), never a key or a fingerprint.
    """
    pinned: dict[str, str] = {}
    entries: list[str] = []
    snapshots = (source,) if destination is None else (source, destination)
    for snapshot in snapshots:
        address = _known_hosts_address(snapshot.host, snapshot.port)
        key = snapshot.host_key.public_key
        known = pinned.get(address)
        if known is None:
            pinned[address] = key
            entries.append(f"{address} {key}")
        elif known != key:
            raise WorkspaceSecurityError(
                f"conflicting host keys for the same SSH address: {address}"
            )
    return tuple(entries)


def _host_block(
    snapshot: SshRuntimeSnapshot, key_path: Path | None
) -> dict[str, object]:
    """One ``src``/``dest`` block, carrying only fields internal/config knows.

    KnownFields(true) makes an unknown key a hard parse error, and the parser has
    no defaults, so ``port`` and ``timeout`` are always emitted and nothing else
    ever is. A None is never emitted: the parser would read a null timeout as 0
    and reject it.
    """
    block: dict[str, object] = {
        # The same validated form known_hosts uses: the engine dials
        # net.JoinHostPort(ip, port) and looks the result up in that file, so any
        # divergence between the two is a lookup miss, i.e. a silent TOFU.
        "ip": _validated_host(snapshot.host),
        "port": snapshot.port,
        "ssh_user": snapshot.username,
    }
    creds = snapshot.credentials
    if creds.auth_method == AUTH_METHOD_PRIVATE_KEY:
        # The better transport: the material is in its own 0600 file. Absolute,
        # because a relative path would resolve against host.yaml's directory and
        # the engine does no ~ expansion.
        if key_path is None:
            # Not an assert: this function is public, and under -O an assert would
            # vanish and emit `ssh_key_path: None` — which the Go parser accepts
            # as a literal path, deferring the failure to dial time.
            raise WorkspaceBuildError(
                "a private-key config needs the path of the materialized key"
            )
        block["ssh_key_path"] = str(key_path)
        if creds.passphrase:
            block["ssh_key_passphrase"] = creds.passphrase
    else:
        # Unavoidable: internal/config has no non-literal form for a password.
        block["ssh_pass"] = creds.password
    block["timeout"] = DEFAULT_TIMEOUT
    return block


def render_host_config(
    source: SshRuntimeSnapshot,
    source_key_path: Path | None,
    destination: SshRuntimeSnapshot | None = None,
    destination_key_path: Path | None = None,
) -> str:
    """Serialize the engine's ``host.yaml``. Pure: no filesystem, no secrets kept.

    ``dest`` is all-or-nothing on the engine side — ``destIntended()`` treats *any*
    populated dest field as intent and then demands the whole block — so it is
    emitted complete or omitted entirely (a valid source-only analysis).

    ``safe_dump`` with ``default_flow_style=False`` and ``sort_keys=False``: no
    Python tags, and a single document (the loader rejects a second one).
    """
    doc: dict[str, object] = {"src": _host_block(source, source_key_path)}
    if destination is not None:
        doc["dest"] = _host_block(destination, destination_key_path)
    return yaml.safe_dump(doc, default_flow_style=False, sort_keys=False)


def _check_runtime_root(root: Path) -> None:
    """Refuse a root that cannot hold secrets safely, before creating anything."""
    if root.is_symlink():
        raise WorkspaceSecurityError(f"runtime root is a symlink: {root}")
    try:
        st = os.stat(root, follow_symlinks=False)
    except OSError as exc:
        raise WorkspaceSecurityError(f"runtime root is unusable: {root}") from exc
    if not stat.S_ISDIR(st.st_mode):
        raise WorkspaceSecurityError(f"runtime root is not a directory: {root}")
    mode = stat.S_IMODE(st.st_mode)
    # Writable by anyone but the owner is only acceptable with the sticky bit:
    # that is /tmp's own 1777, where another user cannot rename or delete our
    # directory. Without it, someone could swap the tree under us between
    # creation and write — and a root replaced by a symlink is followed, and
    # defeats cleanup, stranding a private key on disk.
    if mode & (stat.S_IWOTH | stat.S_IWGRP) and not mode & stat.S_ISVTX:
        raise WorkspaceSecurityError(
            f"runtime root is writable beyond its owner without the sticky bit: {root}"
        )
    if not os.access(root, os.W_OK | os.X_OK):
        raise WorkspaceSecurityError(f"runtime root is not writable: {root}")


def _write_private_file(root: Path, name: str, content: str) -> Path:
    """Create ``root/name`` 0600, refusing to follow or clobber anything.

    O_EXCL: the file must not already exist — no overwrite of a planted file.
    O_NOFOLLOW: if the name is a symlink, fail instead of writing through it,
    which is what would otherwise put a private key wherever the link points.

    The mode is passed to ``open`` and then re-applied with ``fchmod``. Not
    against a permissive umask — a umask only ever clears bits, so it cannot
    widen 0600 — but against a *restrictive* one: under ``umask(0o200)`` the
    open alone yields 0400, and the engine could not write. fchmod restores
    exactly 0600 either way.
    """
    path = root / name
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
    try:
        os.fchmod(fd, 0o600)
        handle = os.fdopen(fd, "w", closefd=True)
    except BaseException:
        # Still ours: fdopen never took it, or fchmod failed before it ran.
        os.close(fd)
        raise
    # From here the file object owns the descriptor. Closing `fd` again — as an
    # outer handler would — is not harmless: a sibling thread (dramatiq runs
    # eight) can be handed the same number in between, and we would close its
    # socket instead, silently.
    with handle:
        handle.write(content)
    return path


@contextlib.contextmanager
def build_ssh_workspace(
    source: SshRuntimeSnapshot,
    destination: SshRuntimeSnapshot | None = None,
    *,
    runtime_root: Path | str | None = None,
) -> Iterator[SshWorkspace]:
    """Materialize a private workspace for ``source`` (and ``destination``).

    A context manager on purpose: the workspace holds a decrypted private key and
    a plaintext password, so "forgot to clean up" must not be reachable through
    ordinary use. Every exit path — return, exception, a failure mid-build —
    removes the whole tree.

    ``runtime_root`` defaults to the process temp dir, and is refused if it is a
    symlink, not a directory, unwritable, or world-writable without the sticky
    bit. The workspace name comes from ``mkdtemp`` (system randomness) and carries
    nothing from the row.

    Builds, and does not start anything. The snapshot's coherence is a fact about
    the moment it was read; the future executor must re-validate before launching.
    """
    # Plan the trust entries first: pure, and the only step that can prove the
    # requested trust is impossible (same address, two pins). Failing here means
    # no directory, no key, no password ever touches the disk.
    known_hosts_entries = _planned_known_hosts_entries(source, destination)

    root_dir = Path(runtime_root) if runtime_root is not None else Path(tempfile.gettempdir())
    _check_runtime_root(root_dir)

    root = Path(tempfile.mkdtemp(prefix=_WORKSPACE_PREFIX, dir=root_dir))
    workspace: SshWorkspace | None = None
    try:
        os.chmod(root, 0o700)  # mkdtemp is already 0700; make the invariant explicit

        source_key_path: Path | None = None
        dest_key_path: Path | None = None
        if source.credentials.auth_method == AUTH_METHOD_PRIVATE_KEY:
            assert source.credentials.private_key is not None
            source_key_path = _write_private_file(
                root, SOURCE_KEY_NAME, source.credentials.private_key
            )
        if (
            destination is not None
            and destination.credentials.auth_method == AUTH_METHOD_PRIVATE_KEY
        ):
            assert destination.credentials.private_key is not None
            dest_key_path = _write_private_file(
                root, DEST_KEY_NAME, destination.credentials.private_key
            )

        # <root>/.ssh/known_hosts, because the engine derives the path from HOME.
        ssh_dir = root / SSH_DIR_NAME
        ssh_dir.mkdir(mode=0o700)
        known_hosts_path = _write_private_file(
            ssh_dir,
            KNOWN_HOSTS_NAME,
            "".join(f"{line}\n" for line in known_hosts_entries),
        )

        host_config_path = _write_private_file(
            root,
            HOST_CONFIG_NAME,
            render_host_config(source, source_key_path, destination, dest_key_path),
        )

        workspace = SshWorkspace(
            root=root,
            host_config_path=host_config_path,
            known_hosts_path=known_hosts_path,
            endpoint_id=source.endpoint_id,
            host=source.host,
            port=source.port,
            fingerprint_sha256=source.host_key.fingerprint_sha256,
            source_key_path=source_key_path,
            dest_key_path=dest_key_path,
        )
        yield workspace
    finally:
        # Not "on success": a build that died halfway has already written a key.
        if workspace is not None:
            workspace.cleanup()
        else:
            shutil.rmtree(root, ignore_errors=True)
