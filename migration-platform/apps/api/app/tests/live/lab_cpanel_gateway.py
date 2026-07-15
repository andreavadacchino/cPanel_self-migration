"""R2-c4-LAB-WIRING — TEST-ONLY read/write cPanel gateways + opaque write-authorization context.

Lives under ``app/tests/live`` (no runtime module imports it). It wraps the REAL ``CpanelClient``
and the REAL op builders and splits the restricted surface into two gateways so the read path can
NEVER reach a write:

* ``LabCpanelReadGateway`` exposes ONLY ``list_domains`` + ``list_forwarders`` on a client that is
  constructed with ``allow_destination_writes=False``; it has no ``add_forwarder``/``write``.
* ``LabCpanelWriteGateway`` exposes ONLY ``add_forwarder(source, destination, authorization)`` on a
  write-ENABLED client; it never retries (``add_forwarder_op`` is ``idempotent=False``), never
  exposes the client, and refuses any write whose success is not provable (reported indeterminate).

A live write cannot be reached with just ``(source, destination)``: it requires a fresh, one-shot
``AuthorizedDisposableLabContext`` DERIVED from a valid ``LabConnectionGateReceipt``. The context
binds the receipt's session nonce, the commit, the endpoint fingerprint, the disposable domain, the
EXACT operation (``Email::add_forwarder``) and the EXACT ``(source, destination)`` pair, carries a
TTL, and is consumed on first use. The gateway refuses a missing/expired/mismatched/reused/forged
context — every refusal issuing ZERO writes. The context holds no credentials and has a safe repr.
"""
from __future__ import annotations

import time
from collections.abc import Mapping

from adapters.cpanel.contract import safe_read
from adapters.cpanel.domains import parse_domains_data
from adapters.cpanel.errors import CpanelError
from app.modules.executions.forwarder_rules import add_forwarder_op, list_forwarders_op

OP_ADD_FORWARDER = "Email::add_forwarder"

# Module-private sentinel: the context constructor is unreachable without it, so no caller can
# fabricate a "valid" authorization by hand.
_AUTH_SENTINEL = object()


def _domains_op():
    return safe_read("DomainInfo", "domains_data")


class LabGatewayError(RuntimeError):
    """Fail-closed gateway error. Carries a safe message; never a credential or raw payload."""


class LabAuthorizationError(RuntimeError):
    """Raised when a write authorization is missing, invalid, or minted without all gates."""


class AuthorizedDisposableLabContext:
    """Opaque, one-shot write authorization for ONE ``(operation, source, destination)`` on ONE
    disposable run. Exists ONLY if minted from a valid receipt after every write gate passed. Holds
    no credentials; its repr never leaks the raw endpoint or a secret."""

    __slots__ = ("_session", "_commit", "_fingerprint", "_domain", "_operation", "_source",
                 "_destination", "_issued_at", "_expires_at", "_nonce", "_consumed")

    def __init__(self, sentinel, *, session_nonce: str, expected_commit: str,
                 endpoint_fingerprint: str, disposable_domain: str, operation: str, source: str,
                 destination: str, issued_at: float, expires_at: float, nonce: str):
        if sentinel is not _AUTH_SENTINEL:
            raise LabAuthorizationError(
                "authorization context constructor is private; use issue_write_authorization")
        if not (session_nonce and expected_commit and endpoint_fingerprint and disposable_domain
                and operation and source and destination and nonce):
            raise LabAuthorizationError("authorization refused: incomplete binding")
        if expires_at <= issued_at:
            raise LabAuthorizationError("authorization refused: non-positive ttl")
        self._session = session_nonce
        self._commit = expected_commit
        self._fingerprint = endpoint_fingerprint
        self._domain = disposable_domain.strip().lower()
        self._operation = operation
        self._source = source.strip().lower()
        self._destination = destination.strip().lower()
        self._issued_at = float(issued_at)
        self._expires_at = float(expires_at)
        self._nonce = nonce
        self._consumed = False

    def _matches(self, *, session: str, commit: str, fingerprint: str, domain: str, operation: str,
                 source: str, destination: str, now: float) -> bool:
        return (not self._consumed and now < self._expires_at
                and self._session == session and self._commit == commit
                and self._fingerprint == fingerprint and self._domain == domain.strip().lower()
                and self._operation == operation and self._source == source.strip().lower()
                and self._destination == destination.strip().lower())

    def _consume(self) -> None:
        if self._consumed:
            raise LabAuthorizationError("authorization already used (one-shot)")
        self._consumed = True

    def __repr__(self) -> str:
        return (f"AuthorizedDisposableLabContext(endpoint={self._fingerprint!r}, "
                f"domain={self._domain!r}, operation={self._operation!r}, "
                f"consumed={self._consumed})")


def issue_write_authorization(receipt, *, operation: str, source: str, destination: str,
                              gates: Mapping[str, bool], issued_at: float,
                              ttl_seconds: float, nonce: str) -> AuthorizedDisposableLabContext:
    """Mint a one-shot, operation+pair-specific context DERIVED from a valid connection receipt.
    Raises unless EVERY write gate is satisfied, the operation is supported, the source is on the
    receipt's disposable domain, and the receipt is still valid at ``issued_at``."""
    missing = sorted(k for k, v in dict(gates).items() if not v)
    if missing:
        raise LabAuthorizationError(f"authorization refused: gate(s) not satisfied: {missing}")
    if ttl_seconds <= 0:
        raise LabAuthorizationError("authorization refused: non-positive ttl")
    if operation != OP_ADD_FORWARDER:
        raise LabAuthorizationError("authorization refused: unsupported operation")
    domain = receipt.disposable_domain
    if "@" not in source or source.split("@", 1)[1].strip().lower() != domain:
        raise LabAuthorizationError("authorization refused: source not on the receipt domain")
    if not receipt.valid_at(issued_at):
        raise LabAuthorizationError("authorization refused: connection receipt expired")
    return AuthorizedDisposableLabContext(
        _AUTH_SENTINEL, session_nonce=receipt.session_nonce, expected_commit=receipt.commit,
        endpoint_fingerprint=receipt.endpoint_fingerprint, disposable_domain=domain,
        operation=operation, source=source, destination=destination, issued_at=float(issued_at),
        expires_at=float(issued_at) + float(ttl_seconds), nonce=nonce)


def _write_marker(result: object) -> dict:
    """Interpret a write result WITHOUT assuming success. Only a proven UAPI status==1 is ``ok``."""
    payload = getattr(result, "payload", None)
    if isinstance(payload, dict):
        res = payload.get("result")
        if isinstance(res, dict) and res.get("status") == 1:
            return {"ok": True, "status": "success"}
    return {"ok": False, "status": "indeterminate"}


class LabCpanelReadGateway:
    """Read-only gateway. Wraps a client built with ``allow_destination_writes=False`` and exposes
    ONLY the two reads the harness needs. It has NO write path and never exposes the client."""

    __slots__ = ("__client",)

    def __init__(self, client):
        self.__client = client

    # NOTE: no __getattr__ — an unknown attribute must raise AttributeError, never pass through.

    def list_domains(self) -> list[str]:
        try:
            result = self.__client.read(_domains_op())
            records = parse_domains_data(result.data)
        except CpanelError as exc:
            raise LabGatewayError(f"list_domains failed: {type(exc).__name__}") from None
        return [r.name for r in records]

    def list_forwarders(self) -> list:
        try:
            result = self.__client.read(list_forwarders_op())
        except CpanelError as exc:
            raise LabGatewayError(f"list_forwarders failed: {type(exc).__name__}") from None
        data = result.data
        if not isinstance(data, list):
            raise LabGatewayError("list_forwarders returned an unexpected shape")
        for entry in data:
            if not isinstance(entry, dict):
                raise LabGatewayError("list_forwarders entry is not an object")
        return data

    def close(self) -> None:
        self.__client.close()


class LabCpanelWriteGateway:
    """Write gateway. Wraps a write-ENABLED client and exposes ONLY ``add_forwarder``. Each call
    requires an ``AuthorizedDisposableLabContext`` that must bind THIS receipt/commit/endpoint/
    domain and the EXACT operation + ``(source, destination)`` pair; any mismatch (or a missing,
    expired, reused or forged context) raises before any write. The client is never exposed."""

    __slots__ = ("__client", "__receipt", "__clock")

    def __init__(self, client, *, receipt, clock=time.time):
        self.__client = client
        self.__receipt = receipt
        self.__clock = clock

    def add_forwarder(self, source: str, destination: str, authorization) -> dict:
        if not isinstance(authorization, AuthorizedDisposableLabContext):
            raise LabGatewayError("add_forwarder requires a valid authorization context")
        r = self.__receipt
        if not authorization._matches(session=r.session_nonce, commit=r.commit,
                                      fingerprint=r.endpoint_fingerprint, domain=r.disposable_domain,
                                      operation=OP_ADD_FORWARDER, source=source,
                                      destination=destination, now=self.__clock()):
            raise LabGatewayError("authorization does not bind this receipt/operation/pair "
                                  "or is expired/used")
        authorization._consume()  # one-shot: consumed only after all binding checks pass
        try:
            result = self.__client.write(add_forwarder_op(source, destination))
        except CpanelError as exc:
            raise LabGatewayError(f"add_forwarder failed: {type(exc).__name__}") from None
        return _write_marker(result)

    def close(self) -> None:
        self.__client.close()


__all__ = ["OP_ADD_FORWARDER", "LabGatewayError", "LabAuthorizationError",
           "AuthorizedDisposableLabContext", "issue_write_authorization",
           "LabCpanelReadGateway", "LabCpanelWriteGateway"]
