"""Typed contract for the SSH adapter boundary.

This module holds the typed models (endpoint, credentials, timeouts, output
limits, retry policy, session role), the typed command builder, the secret
redaction helper, the bounded command result, and the redacted audit record. It
separates *reads* from *writes* at the type level and never lets an unvalidated
string reach the wire as a shell command: a command is built from a typed program
name plus arguments and quoted with :func:`shlex.join`, so no metacharacter in an
argument can ever inject an extra command.
"""

from __future__ import annotations

import enum
import re
import shlex
from dataclasses import dataclass, field
from typing import Literal

from pydantic import BaseModel, Field, model_validator

from adapters.ssh.errors import SshCommandRejectedError

# A program name is a single executable path: letters, digits, and the handful of
# path/name characters. This blocks whitespace, shell metacharacters, and NUL so a
# program token can never smuggle a second command even before shlex quoting.
_PROGRAM = re.compile(r"^[A-Za-z0-9_./-]+$")

# Parameter keys whose *values* must never appear in an error, log, or audit
# record. The adapter never puts secrets on the command line, but redaction masks
# any accidental ``password=...`` style leak in free text defensively.
_SENSITIVE_KEYS = frozenset({"password", "passwd", "passphrase", "secret", "token"})

StreamName = Literal["stdout", "stderr"]


class SessionRole(enum.Enum):
    """The single, immutable capability of a session.

    A source session is structurally read-only; only a destination-write session
    may ever reach a write/stdin primitive (and only when explicitly enabled).
    """

    SOURCE_READ = "source_read"
    DESTINATION_READ = "destination_read"
    DESTINATION_WRITE = "destination_write"

    @property
    def is_source(self) -> bool:
        return self is SessionRole.SOURCE_READ

    @property
    def can_write(self) -> bool:
        return self is SessionRole.DESTINATION_WRITE


class SshEndpoint(BaseModel):
    """Where to connect. Never carries a secret."""

    host: str
    username: str
    port: int = Field(default=22, ge=1, le=65535)

    @model_validator(mode="after")
    def _reject_unsafe(self) -> "SshEndpoint":
        # Defence in depth: the adapter is the security boundary, so it never
        # trusts an upstream host/username blindly. Reject userinfo, whitespace,
        # CRLF, and scheme/path separators.
        for label, value in (("host", self.host), ("username", self.username)):
            if not value or any(ch in value for ch in "@/\\ \t\r\n") or "://" in value:
                raise ValueError(f"Unsafe SSH {label}")
        return self


class SshCredentials(BaseModel):
    """Password and/or private key. Secrets are excluded from every ``repr``.

    ``repr=False`` keeps the value out of any incidental log/traceback line; the
    values are still available programmatically for the transport but are returned
    to redaction via :meth:`secret_values` so they are scrubbed from audit text.
    """

    password: str | None = Field(default=None, repr=False)
    private_key_pem: str | None = Field(default=None, repr=False)
    private_key_passphrase: str | None = Field(default=None, repr=False)

    @model_validator(mode="after")
    def _require_one(self) -> "SshCredentials":
        if not self.password and not self.private_key_pem:
            raise ValueError("SSH credentials need a password or a private key")
        return self

    @property
    def auth_method(self) -> str:
        # Reported (never a secret) so an audit records how a host was reached.
        if self.private_key_pem and self.password:
            return "key+password"
        return "key" if self.private_key_pem else "password"

    def secret_values(self) -> tuple[str, ...]:
        return tuple(
            s
            for s in (self.password, self.private_key_pem, self.private_key_passphrase)
            if s
        )


@dataclass(frozen=True)
class SshTimeouts:
    """Explicit timeouts in seconds. ``None`` disables that limit."""

    connect: float = 10.0
    command: float = 30.0
    idle: float = 15.0


@dataclass(frozen=True)
class OutputLimits:
    """Per-stream byte caps so a runaway command cannot exhaust memory."""

    max_stdout_bytes: int = 1_048_576
    max_stderr_bytes: int = 262_144
    # How large a chunk to pull from the channel at a time.
    read_chunk_bytes: int = 32_768


@dataclass(frozen=True)
class SshRetryPolicy:
    """Bounded backoff for the *connect phase only*.

    A command is never retried through this policy; a partially started command or
    stream must not be replayed.
    """

    max_attempts: int = 3
    base_delay: float = 0.2
    max_delay: float = 5.0
    multiplier: float = 2.0
    jitter_ratio: float = 0.25

    def delay_for(self, attempt: int, jitter_unit: float) -> float:
        """Deterministic backoff for ``attempt`` (1-based) given a ``[0,1)`` unit."""
        raw = self.base_delay * (self.multiplier ** (attempt - 1))
        capped = min(raw, self.max_delay)
        return capped + capped * self.jitter_ratio * jitter_unit


@dataclass(frozen=True)
class Command:
    """A typed, shell-safe command.

    ``argv`` is the program plus its arguments; ``wire`` is the shlex-quoted string
    actually sent to the channel. ``is_write`` marks a mutating command so a read
    session can refuse it structurally.
    """

    argv: tuple[str, ...]
    is_write: bool = False

    @property
    def program(self) -> str:
        return self.argv[0]

    @property
    def wire(self) -> str:
        # shlex.join quotes every token, so an argument such as ``$(rm -rf /)`` is
        # delivered literally and can never start a second command.
        return shlex.join(self.argv)

    def label(self) -> str:
        # The program name is safe to show (validated); arguments may carry
        # user-supplied values and are summarised by count, not content.
        extra = len(self.argv) - 1
        return f"{self.program} (+{extra} args)" if extra else self.program


def command(program: str, *args: str, is_write: bool = False) -> Command:
    """Build a typed command, rejecting anything that is not a safe argv.

    The program must match a strict pattern; every argument must be a string
    without a NUL byte (which ``shlex`` cannot quote). This is the only way to
    construct a command — there is no raw-string entry point — so an API/UI caller
    can never inject an arbitrary shell line.
    """
    if not isinstance(program, str) or not _PROGRAM.match(program):
        raise SshCommandRejectedError("Invalid SSH program name")
    clean: list[str] = [program]
    for arg in args:
        if not isinstance(arg, str):
            raise SshCommandRejectedError("SSH command arguments must be strings")
        if "\x00" in arg:
            raise SshCommandRejectedError("SSH command arguments must not contain NUL")
        clean.append(arg)
    return Command(argv=tuple(clean), is_write=is_write)


def redact(text: object, secrets: tuple[str, ...] = ()) -> str:
    """Return ``text`` as a string with every known secret replaced by ``***``."""
    result = str(text)
    for secret in secrets:
        if secret:
            result = result.replace(secret, "***")
    for key in _SENSITIVE_KEYS:
        result = re.sub(rf"(?i)({re.escape(key)}\s*[=:]\s*)[^\s,&;]+", r"\1***", result)
    return result


@dataclass
class SshCommandAudit:
    """Redacted, secret-free evidence of a single SSH command execution."""

    operation: str
    role: str
    host: str
    port: int
    outcome: Literal["succeeded", "failed"] = "failed"
    auth_method: str | None = None
    host_key_fingerprint: str | None = None
    host_key_status: str | None = None
    exit_status: int | None = None
    exit_signal: str | None = None
    attempts: int = 0
    stdout_bytes: int = 0
    stderr_bytes: int = 0
    stdout_truncated: bool = False
    stderr_truncated: bool = False
    error_type: str | None = None
    message: str | None = None

    def as_evidence(self) -> dict[str, object]:
        """A JSON-safe mapping suitable for persistence in an event/snapshot."""
        return {
            "operation": self.operation,
            "role": self.role,
            "host": self.host,
            "port": self.port,
            "outcome": self.outcome,
            "auth_method": self.auth_method,
            "host_key_fingerprint": self.host_key_fingerprint,
            "host_key_status": self.host_key_status,
            "exit_status": self.exit_status,
            "exit_signal": self.exit_signal,
            "attempts": self.attempts,
            "stdout_bytes": self.stdout_bytes,
            "stderr_bytes": self.stderr_bytes,
            "stdout_truncated": self.stdout_truncated,
            "stderr_truncated": self.stderr_truncated,
            "error_type": self.error_type,
            "message": self.message,
        }


@dataclass
class CommandResult:
    """A finished command's bounded output bound to its redacted audit."""

    stdout: bytes
    stderr: bytes
    exit_status: int | None
    exit_signal: str | None
    stdout_truncated: bool
    stderr_truncated: bool
    audit: SshCommandAudit = field(repr=False)

    @property
    def ok(self) -> bool:
        return self.exit_status == 0 and self.exit_signal is None

    def stdout_text(self) -> str:
        return self.stdout.decode("utf-8", "replace")

    def stderr_text(self) -> str:
        return self.stderr.decode("utf-8", "replace")


__all__ = [
    "SessionRole",
    "SshEndpoint",
    "SshCredentials",
    "SshTimeouts",
    "OutputLimits",
    "SshRetryPolicy",
    "Command",
    "command",
    "redact",
    "SshCommandAudit",
    "CommandResult",
]
