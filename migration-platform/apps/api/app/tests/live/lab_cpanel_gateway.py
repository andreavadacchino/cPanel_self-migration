"""R2-c4-LAB-GATEWAY — TEST-ONLY restricted cPanel gateway + opaque write-authorization context.

Lives under ``app/tests/live`` (no runtime module imports it). It wraps the REAL ``CpanelClient``
and the REAL op builders and exposes ONLY the three methods the forwarder harness needs:
``list_domains``, ``list_forwarders`` and ``add_forwarder(source, destination, authorization)``.
It never exposes the generic client, ``execute``, arbitrary APIs, a ``__getattr__`` passthrough, or
any other cPanel write (delete/filter/autoresponder/routing/default-address). Parsing is fail-closed
(any unexpected shape, partial/ambiguous status, timeout or TLS error becomes a typed
``LabGatewayError``; a write whose success is not provable is reported indeterminate, never assumed
successful). Live writes are never auto-retried — the underlying ``add_forwarder_op`` is
``idempotent=False`` and ``CpanelClient`` does not retry non-idempotent writes.

A live write cannot be reached with just ``(source, destination)``: it requires an
``AuthorizedDisposableLabContext`` that can only be minted after ALL harness gates passed and that
binds the expected commit, endpoint fingerprint and disposable domain, carries a TTL + nonce, and is
one-shot. The gateway refuses a missing/expired/mismatched/reused/gateless context — every refusal
issuing ZERO writes. The context holds no credentials and has a safe repr.
"""
from __future__ import annotations

import time
from collections.abc import Mapping

from adapters.cpanel.errors import CpanelError
from app.modules.executions.forwarder_rules import add_forwarder_op, list_forwarders_op

# Real domain read builder + fail-closed parser (reused, not reimplemented).
try:  # adapters package layout
    from adapters.cpanel.domains import parse_domains_data
    from adapters.cpanel.contract import safe_read
    _DOMAINS_OP = lambda: safe_read("DomainInfo", "domains_data")
except Exception as _exc:  # pragma: no cover - import guard; surfaced by tests if it ever fires
    raise


class LabGatewayError(RuntimeError):
    """Fail-closed gateway error. Carries a safe message; never a credential or raw payload."""


class LabAuthorizationError(RuntimeError):
    """Raised when a write authorization is missing, invalid, or minted without all gates."""


class AuthorizedDisposableLabContext:
    """Opaque, one-shot write authorization. Exists ONLY if every gate passed. Binds the run to a
    specific commit + endpoint fingerprint + disposable domain, expires after a TTL, and is consumed
    on first use. Holds no credentials; its repr never leaks anything sensitive."""

    __slots__ = ("_commit", "_fingerprint", "_domain", "_issued_at", "_expires_at", "_nonce",
                 "_consumed")

    def __init__(self, *, expected_commit: str, endpoint_fingerprint: str, disposable_domain: str,
                 gates: Mapping[str, bool], issued_at: float, ttl_seconds: float, nonce: str):
        missing = sorted(k for k, v in dict(gates).items() if not v)
        if missing:
            raise LabAuthorizationError(f"authorization refused: gate(s) not satisfied: {missing}")
        if not (expected_commit and endpoint_fingerprint and disposable_domain and nonce):
            raise LabAuthorizationError("authorization refused: incomplete binding")
        if ttl_seconds <= 0:
            raise LabAuthorizationError("authorization refused: non-positive ttl")
        self._commit = expected_commit
        self._fingerprint = endpoint_fingerprint
        self._domain = disposable_domain.strip().lower()
        self._issued_at = float(issued_at)
        self._expires_at = float(issued_at) + float(ttl_seconds)
        self._nonce = nonce
        self._consumed = False

    def _matches(self, *, commit: str, fingerprint: str, domain: str, now: float) -> bool:
        return (not self._consumed and now < self._expires_at and self._commit == commit
                and self._fingerprint == fingerprint and self._domain == domain.strip().lower())

    def _consume(self) -> None:
        if self._consumed:
            raise LabAuthorizationError("authorization already used (one-shot)")
        self._consumed = True

    def __repr__(self) -> str:
        return (f"AuthorizedDisposableLabContext(endpoint={self._fingerprint!r}, "
                f"domain={self._domain!r}, consumed={self._consumed})")


def issue_lab_authorization(*, expected_commit: str, endpoint_fingerprint: str,
                            disposable_domain: str, gates: Mapping[str, bool], issued_at: float,
                            ttl_seconds: float, nonce: str) -> AuthorizedDisposableLabContext:
    """Mint a one-shot context; raises unless EVERY gate is satisfied."""
    return AuthorizedDisposableLabContext(
        expected_commit=expected_commit, endpoint_fingerprint=endpoint_fingerprint,
        disposable_domain=disposable_domain, gates=gates, issued_at=issued_at,
        ttl_seconds=ttl_seconds, nonce=nonce)


def _write_marker(result: object) -> dict:
    """Interpret a write result WITHOUT assuming success. Only a proven UAPI status==1 is ``ok``."""
    payload = getattr(result, "payload", None)
    if isinstance(payload, dict):
        res = payload.get("result")
        if isinstance(res, dict) and res.get("status") == 1:
            return {"ok": True, "status": "success"}
    return {"ok": False, "status": "indeterminate"}


class LabCpanelGateway:
    """The restricted gateway. Constructed with a real (or fake) ``CpanelClient`` bound to ONE
    disposable endpoint + domain + expected commit."""

    __slots__ = ("__client", "__fingerprint", "__domain", "__commit", "__clock")

    def __init__(self, client, *, endpoint_fingerprint: str, disposable_domain: str,
                 expected_commit: str, clock=time.time):
        self.__client = client
        self.__fingerprint = endpoint_fingerprint
        self.__domain = disposable_domain.strip().lower()
        self.__commit = expected_commit
        self.__clock = clock

    # NOTE: no __getattr__ — an unknown attribute must raise AttributeError, never pass through.

    def list_domains(self) -> list[str]:
        try:
            result = self.__client.read(_DOMAINS_OP())
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

    def add_forwarder(self, source: str, destination: str, authorization) -> dict:
        if not isinstance(authorization, AuthorizedDisposableLabContext):
            raise LabGatewayError("add_forwarder requires a valid authorization context")
        now = self.__clock()
        if not authorization._matches(commit=self.__commit, fingerprint=self.__fingerprint,
                                      domain=self.__domain, now=now):
            raise LabGatewayError("authorization does not bind this gateway/commit/domain "
                                  "or is expired/used")
        if "@" not in source or source.split("@", 1)[1].strip().lower() != self.__domain:
            raise LabGatewayError("source is not on the authorized disposable domain")
        authorization._consume()  # one-shot: consumed only after all binding checks pass
        try:
            result = self.__client.write(add_forwarder_op(source, destination))
        except CpanelError as exc:
            raise LabGatewayError(f"add_forwarder failed: {type(exc).__name__}") from None
        return _write_marker(result)

    # expose the binding (non-secret) so a harness binder can mint matching contexts
    @property
    def binding(self) -> tuple[str, str, str]:
        return (self.__commit, self.__fingerprint, self.__domain)


class _BoundLabGateway:
    """Harness-facing 2-arg adapter: presents the ``add_forwarder(source, destination)`` shape the
    committed harness calls, minting a FRESH one-shot context per add from held gate evidence."""

    __slots__ = ("__gw", "__gates", "__clock", "__ttl", "__nonce")

    def __init__(self, gateway: LabCpanelGateway, *, gates, clock, ttl_seconds, nonce_factory):
        self.__gw = gateway
        self.__gates = dict(gates)
        self.__clock = clock
        self.__ttl = ttl_seconds
        self.__nonce = nonce_factory

    def list_domains(self):
        return self.__gw.list_domains()

    def list_forwarders(self):
        return self.__gw.list_forwarders()

    def add_forwarder(self, source: str, destination: str) -> dict:
        commit, fingerprint, domain = self.__gw.binding
        ctx = issue_lab_authorization(
            expected_commit=commit, endpoint_fingerprint=fingerprint, disposable_domain=domain,
            gates=self.__gates, issued_at=self.__clock(), ttl_seconds=self.__ttl,
            nonce=self.__nonce())
        return self.__gw.add_forwarder(source, destination, ctx)


def bind_for_harness(gateway: LabCpanelGateway, *, gates, clock=time.time, ttl_seconds: float = 30.0,
                     nonce_factory=None) -> _BoundLabGateway:
    """Wrap ``gateway`` for the committed harness's 2-arg ``add_forwarder`` call."""
    if nonce_factory is None:
        import uuid
        nonce_factory = lambda: uuid.uuid4().hex
    return _BoundLabGateway(gateway, gates=gates, clock=clock, ttl_seconds=ttl_seconds,
                            nonce_factory=nonce_factory)


__all__ = ["LabGatewayError", "LabAuthorizationError", "AuthorizedDisposableLabContext",
           "issue_lab_authorization", "LabCpanelGateway", "bind_for_harness"]
