"""SSH adapter (stub) — no real connections in Sprint 0."""

from __future__ import annotations

_NOT_IMPLEMENTED = "SSH adapter is a Sprint 0 stub and has no real logic yet."


class SshClient:
    def __init__(self, host: str, username: str, port: int = 22) -> None:
        self.host = host
        self.username = username
        self.port = port

    def run(self, command: str) -> None:
        raise NotImplementedError(_NOT_IMPLEMENTED)


__all__ = ["SshClient"]
