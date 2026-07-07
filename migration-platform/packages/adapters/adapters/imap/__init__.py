"""IMAP adapter (stub) — no real connections in Sprint 0."""

from __future__ import annotations

_NOT_IMPLEMENTED = "IMAP adapter is a Sprint 0 stub and has no real logic yet."


class ImapClient:
    def __init__(self, host: str, username: str, port: int = 993) -> None:
        self.host = host
        self.username = username
        self.port = port

    def list_mailboxes(self) -> None:
        raise NotImplementedError(_NOT_IMPLEMENTED)


__all__ = ["ImapClient"]
