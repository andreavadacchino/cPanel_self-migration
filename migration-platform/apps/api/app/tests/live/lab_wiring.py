"""R2-c4-LAB-WIRING — TEST-ONLY end-to-end wiring of the live add_forwarder harness.

Lives under ``app/tests/live`` (no runtime module imports it). It composes the REAL pieces in the
REAL order so the single live pytest exercises exactly this path (only the client/transport factory
is swapped in non-live tests — never the wiring itself):

    static safety gate (env-only, fail-closed)
      -> LabConnectionGateReceipt (opaque; replaces any boolean)
      -> receipt-gated credential loader (token file opened ONLY here)
      -> read-only CpanelClient (allow_destination_writes=False) + LabCpanelReadGateway
      -> domain ownership + empty baseline (the real characterization runner, via reads)
      -> operation+pair-specific AuthorizedDisposableLabContext, one per add
      -> write-ENABLED CpanelClient (allow_destination_writes=True), built LAZILY on first add
      -> LabCpanelWriteGateway -> characterization matrix -> sanitized report

Client lifecycle is guaranteed: the read client and (if built) the write client are always closed;
the primary exception is preserved over any close failure, and a close failure never turns into a
false success. No credential ever appears in a log, repr, error, or the returned report.
"""
from __future__ import annotations

import secrets as _secrets
from collections.abc import Mapping

from adapters.cpanel.client import CpanelClient
from app.tests.live import forwarder_live_characterization as lc
from app.tests.live import lab_cpanel_gateway as gw
from app.tests.live import lab_credentials as cred


class LabWiringError(RuntimeError):
    """Fail-closed wiring error. Carries a safe, fixed message; never a credential."""


def _default_read_client(credentials) -> CpanelClient:
    """The REAL read-only client: destination writes are DISABLED (a read can never mutate)."""
    return CpanelClient(credentials, allow_destination_writes=False)


def _default_write_client(credentials) -> CpanelClient:
    """The REAL write-enabled client: constructed ONLY after the read preflight has passed."""
    return CpanelClient(credentials, allow_destination_writes=True)


def _connection_gates(env: Mapping[str, str], *, endpoint: str, commit: str,
                      tree_clean: bool) -> dict[str, bool]:
    allow = [x.strip() for x in (env.get(lc.ENV_ENDPOINT_ALLOWLIST) or "").split(",") if x.strip()]
    deny = [x.strip() for x in (env.get(lc.ENV_PRODUCTION_ENDPOINTS) or "").split(",") if x.strip()]
    return {
        "run_destructive": env.get(lc.ENV_RUN_DESTRUCTIVE) == "1",
        "disposable": env.get(lc.ENV_ACCOUNT_DISPOSABLE) == "1",
        "reset_approved": env.get(lc.ENV_RESET_APPROVED) == "1",
        "endpoint_allowlisted": bool(endpoint) and endpoint in allow,
        "not_production": bool(endpoint) and endpoint not in deny,
        "domain_configured": bool((env.get(lc.ENV_DISPOSABLE_DOMAIN) or "").strip()),
        "working_tree_clean": tree_clean,
        "commit_present": bool(commit),
    }


class _WiredHarnessGateway:
    """Harness-facing facade. Routes reads to the read-only gateway; for each add it mints a FRESH
    one-shot, operation+pair-specific context from the receipt and lazily builds the write-enabled
    client on the FIRST add (so a preflight refusal never constructs a write client)."""

    __slots__ = ("_read", "_receipt", "_creds", "_wcf", "_gates", "_clock", "_ttl", "_nonce",
                 "_write_gw")

    def __init__(self, read_gw, *, receipt, credentials, write_client_factory,
                 gates: Mapping[str, bool], clock, ttl_seconds: float, nonce_factory):
        self._read = read_gw
        self._receipt = receipt
        self._creds = credentials
        self._wcf = write_client_factory
        self._gates = dict(gates)
        self._clock = clock
        self._ttl = ttl_seconds
        self._nonce = nonce_factory
        self._write_gw = None

    def list_domains(self):
        return self._read.list_domains()

    def list_forwarders(self):
        return self._read.list_forwarders()

    def add_forwarder(self, source: str, destination: str) -> dict:
        ctx = gw.issue_write_authorization(
            self._receipt, operation=gw.OP_ADD_FORWARDER, source=source, destination=destination,
            gates=self._gates, issued_at=self._clock(), ttl_seconds=self._ttl, nonce=self._nonce())
        write_gw = self._ensure_write_gateway()  # write-ENABLED client built ONLY now
        return write_gw.add_forwarder(source, destination, ctx)

    def _ensure_write_gateway(self):
        if self._write_gw is None:
            write_client = self._wcf(self._creds)
            self._write_gw = gw.LabCpanelWriteGateway(write_client, receipt=self._receipt,
                                                      clock=self._clock)
        return self._write_gw

    def close_write(self) -> None:
        if self._write_gw is not None:
            self._write_gw.close()

    @property
    def write_built(self) -> bool:
        return self._write_gw is not None


def _close_all(read_gw, composite: _WiredHarnessGateway) -> bool:
    """Close the read gateway and (if built) the write gateway. Never re-raises the close error
    (so it can never mask a primary exception or leak a token); returns True if any close failed."""
    failed = False
    for closer in (read_gw.close, composite.close_write):
        try:
            closer()
        except Exception:  # noqa: BLE001 — close failures are captured as a flag, never surfaced raw
            failed = True
    return failed


def run_wired_live_characterization(*, env: Mapping[str, str], repo_root: str, now,
                                    status_provider, head_provider, timestamp: str | None = None,
                                    concurrency_runner=None,
                                    read_client_factory=_default_read_client,
                                    write_client_factory=_default_write_client,
                                    ttl_seconds: float = 30.0, session_nonce: str | None = None,
                                    nonce_factory=None) -> dict:
    """Run the full wired characterization. Fails closed BEFORE constructing any client if the
    static gate, clean-tree/committed-HEAD check, receipt issuance, or credential resolution
    refuses. Always closes every client it built."""
    # 1. static gate (env-only) — no receipt, no token, no client on refusal
    decision = lc.live_characterization_authorized(env)
    if not decision.authorized:
        raise LabWiringError(f"static gate refused: {decision.reason}")
    # 2. clean working tree at a committed HEAD
    status = status_provider()
    if status.strip():
        raise LabWiringError("static gate refused: working_tree_dirty")
    commit = head_provider()
    if not commit:
        raise LabWiringError("static gate refused: no committed HEAD")
    endpoint = (env.get(lc.ENV_ENDPOINT) or "").strip()
    domain = (env.get(lc.ENV_DISPOSABLE_DOMAIN) or "").strip().lower()
    # 3. mint the opaque connection receipt (all static gates bound)
    issued = now()
    receipt = cred.issue_connection_receipt(
        gates=_connection_gates(env, endpoint=endpoint, commit=commit, tree_clean=True),
        commit=commit, endpoint=endpoint, disposable_domain=domain, issued_at=issued,
        ttl_seconds=ttl_seconds, session_nonce=session_nonce)
    # 4. resolve the token via the receipt + build read credentials (token file opened only here)
    credentials = cred.build_lab_credentials(
        env, receipt, repo_root=repo_root, now=issued, expected_commit=commit,
        expected_endpoint=endpoint, expected_domain=domain)
    # 5. read-only client (writes DISABLED) + read gateway
    read_client = read_client_factory(credentials)
    read_gw = gw.LabCpanelReadGateway(read_client)
    composite = _WiredHarnessGateway(
        read_gw, receipt=receipt, credentials=credentials,
        write_client_factory=write_client_factory,
        gates=_connection_gates(env, endpoint=endpoint, commit=commit, tree_clean=True),
        clock=now, ttl_seconds=ttl_seconds,
        nonce_factory=nonce_factory or (lambda: _secrets.token_hex(8)))
    # 6. domain ownership + empty baseline + matrix, via the REAL characterization runner
    primary: BaseException | None = None
    report = None
    try:
        report = lc.run_live_characterization(
            composite, env=env, status_provider=status_provider, head_provider=head_provider,
            timestamp=timestamp, concurrency_runner=concurrency_runner)
    except BaseException as exc:  # noqa: BLE001 — re-raised after guaranteed close
        primary = exc
    close_failed = _close_all(read_gw, composite)
    if primary is not None:
        raise primary  # primary preserved over any close failure
    if close_failed:
        raise LabWiringError("client close failed after an otherwise-successful run")
    return report


__all__ = ["LabWiringError", "run_wired_live_characterization"]
