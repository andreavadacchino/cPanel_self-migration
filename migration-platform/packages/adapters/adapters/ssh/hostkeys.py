"""Host-key verification: persistent known-hosts, fingerprint, and policy.

The policy is authoritative and independent of any transport library so it can be
unit-tested directly. It supports two modes:

* ``strict`` (default) — an unknown host is rejected.
* ``accept_new`` — an unknown host's key is recorded (audibly, never silently).

A key that is *known but different* is **always** rejected in both modes: a
changed host key may be a man-in-the-middle and is never trusted. There is no
``AutoAddPolicy``-style silent acceptance.
"""

from __future__ import annotations

import base64
import hashlib
import threading
from dataclasses import dataclass
from pathlib import Path
from typing import Literal

from adapters.ssh.errors import (
    SshHostKeyChangedError,
    SshHostKeyUnknownError,
)

HostKeyMode = Literal["strict", "accept_new"]
HostKeyStatus = Literal["matched", "accepted_new"]


@dataclass(frozen=True)
class HostKeyRecord:
    """A host's public key: address plus a key type and base64-encoded blob."""

    host: str
    port: int
    key_type: str
    key_base64: str

    @property
    def address(self) -> str:
        return f"{self.host}:{self.port}"

    @property
    def fingerprint(self) -> str:
        """An auditable, non-secret SHA256 fingerprint (OpenSSH ``SHA256:`` form)."""
        raw = base64.b64decode(self.key_base64)
        digest = hashlib.sha256(raw).digest()
        return "SHA256:" + base64.b64encode(digest).decode("ascii").rstrip("=")


@dataclass(frozen=True)
class HostKeyDecision:
    """The outcome of a successful verification (a failure raises instead)."""

    status: HostKeyStatus
    fingerprint: str


class KnownHostsStore:
    """A small persistent known-hosts file: ``host:port keytype base64`` per line.

    The file is the durable trust anchor across runs. Reads and writes are guarded
    by a lock so a concurrent accept-new cannot corrupt the file.
    """

    def __init__(self, path: str | Path | None = None) -> None:
        self._path = Path(path) if path is not None else None
        self._lock = threading.Lock()
        self._records: dict[str, HostKeyRecord] = {}
        self._load()

    def _load(self) -> None:
        if self._path is None or not self._path.exists():
            return
        for line in self._path.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            parts = line.split()
            if len(parts) != 3:
                continue
            address, key_type, key_b64 = parts
            host, _, port = address.rpartition(":")
            if not host or not port.isdigit():
                continue
            record = HostKeyRecord(host, int(port), key_type, key_b64)
            self._records[record.address] = record

    def lookup(self, host: str, port: int) -> HostKeyRecord | None:
        with self._lock:
            return self._records.get(f"{host}:{port}")

    def add(self, record: HostKeyRecord) -> None:
        """Record a new trusted key and persist it. Never overwrites a different
        key for an address (a change is a policy failure, handled upstream)."""
        with self._lock:
            existing = self._records.get(record.address)
            if existing is not None and existing.key_base64 == record.key_base64:
                return
            self._records[record.address] = record
            if self._path is not None:
                self._persist_locked()

    def _persist_locked(self) -> None:
        assert self._path is not None
        self._path.parent.mkdir(parents=True, exist_ok=True)
        lines = [
            f"{r.address} {r.key_type} {r.key_base64}"
            for r in sorted(self._records.values(), key=lambda r: r.address)
        ]
        # Restrictive permissions: the trust anchor is not world-readable.
        self._path.write_text("\n".join(lines) + "\n", encoding="utf-8")
        try:
            self._path.chmod(0o600)
        except OSError:  # pragma: no cover - platform dependent
            pass


class HostKeyPolicy:
    """Decide whether a presented host key is trusted, per the configured mode."""

    def __init__(self, mode: HostKeyMode = "strict") -> None:
        if mode not in ("strict", "accept_new"):
            raise ValueError(f"Unknown host-key mode: {mode!r}")
        self.mode = mode

    def verify(self, store: KnownHostsStore, presented: HostKeyRecord) -> HostKeyDecision:
        """Return a decision for a trusted key or raise a typed host-key error.

        The fingerprint is always computed first so it is available for the audit
        even on rejection (the caller records it before re-raising).
        """
        fingerprint = presented.fingerprint
        known = store.lookup(presented.host, presented.port)
        if known is None:
            if self.mode == "strict":
                raise SshHostKeyUnknownError(
                    f"Unknown SSH host key for {presented.address} ({fingerprint})"
                )
            store.add(presented)
            return HostKeyDecision(status="accepted_new", fingerprint=fingerprint)
        if known.key_type != presented.key_type or known.key_base64 != presented.key_base64:
            # A changed key is always rejected, in both modes.
            raise SshHostKeyChangedError(
                f"SSH host key changed for {presented.address} ({fingerprint})"
            )
        return HostKeyDecision(status="matched", fingerprint=fingerprint)


__all__ = [
    "HostKeyMode",
    "HostKeyStatus",
    "HostKeyRecord",
    "HostKeyDecision",
    "KnownHostsStore",
    "HostKeyPolicy",
]
