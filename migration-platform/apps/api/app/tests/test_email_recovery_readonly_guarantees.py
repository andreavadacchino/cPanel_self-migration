"""R2-c4b0 — structural AND behavioral proof that the read-only shadow/recovery surface writes
nothing and is not wired into any runtime.

Structural: the pure shadow modules + the capability-policy gate reference no write/recovery/claim
primitive (AST identifier inspection, not a text grep). Behavioral: a spy gateway that explodes on
any write proves the probe never issues one, and the overwrite (routing) path — which decrypts a
durable backup — mutates zero DB rows. Wiring: no production module imports the shadow/policy/live
modules, and the policy module is config-free so no default-on feature flag can enable it.
"""
from __future__ import annotations

import ast
import pathlib

from sqlalchemy import func, select

from app.modules.executions import email_shadow_classify as sc
from app.modules.executions.email_shadow_probe import shadow_probe_run
from app.modules.executions.models import EmailWriteJournal, ExecutionRun
from app.tests.test_email_phase_registry import _rt_contract
from app.tests.test_email_shadow_probe import _build_run, _journal
from app.tests.test_email_journal_crash import mk, pg  # noqa: F401

_EXEC = pathlib.Path(__file__).resolve().parents[1] / "modules" / "executions"
_PURE_READONLY_MODULES = ("email_shadow_classify.py", "email_shadow_probe.py",
                          "email_recovery_capability_policy.py")


def _used_identifiers(path: pathlib.Path) -> set[str]:
    tree = ast.parse(path.read_text())
    used: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Attribute):
            used.add(node.attr)
        elif isinstance(node, ast.Name):
            used.add(node.id)
    return used


# -- structural: the pure modules touch no write/recovery/claim primitive -----

def test_pure_modules_reference_no_write_primitive():
    forbidden = {"apply_retry", "recovery_transition", "acquire", "mark_started", "mark_applied",
                 "mark_reconciliation_required", "create", "destination_write", "add_forwarder_op",
                 "store_filter_op", "setmxcheck_op", "add_auto_responder_op", "set_default_address_op",
                 "recover_email_run", "commit", "flush", "persist_email_backup", "open_intent",
                 "add_forwarder", "store_filter", "setmxcheck", "add_auto_responder"}
    for name in _PURE_READONLY_MODULES:
        used = _used_identifiers(_EXEC / name)
        assert not (forbidden & used), f"{name} references {forbidden & used}"


def test_pure_modules_contain_no_sql_mutation():
    for node_name in ("update", "insert", "delete"):
        for name in _PURE_READONLY_MODULES:
            used = _used_identifiers(_EXEC / name)
            assert node_name not in used, f"{name} references sqlalchemy {node_name}"


def test_policy_module_is_config_free():
    """No default-on feature flag can enable recovery: the policy gate never reads settings."""
    used = _used_identifiers(_EXEC / "email_recovery_capability_policy.py")
    assert "settings" not in used and "Settings" not in used


# -- behavioral: a spy gateway that explodes on any write --------------------

class _WriteSpyGateway:
    """Read-only reads; ANY write attempt fails immediately and loudly."""

    def __init__(self, live):
        self._live = live
        self.writes = 0

    def read_live(self):
        return self._live

    def _boom(self, *a, **k):
        self.writes += 1
        raise AssertionError("shadow probe attempted a WRITE")

    create = add_forwarder = store_filter = setmxcheck = add_auto_responder = _boom
    set_default_address = _boom


def test_probe_never_calls_a_write_method(mk):
    s = mk()
    from app.tests.test_email_shadow_probe import _FPay, _FStep
    from app.tests.test_email_phase_registry import _fwd_snapshot
    env = _build_run(s, category="email_forwarders", step_id=_FStep,
                     src_data=_fwd_snapshot([("a@x.test", "b@y.test")]),
                     dst_data=_fwd_snapshot([("a@x.test", "b@y.test")]))
    _journal(s, env, category="email_forwarders", step_id=_FStep, payload=_FPay,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    spy = _WriteSpyGateway([{"dest": "a@x.test", "forward": "b@y.test"}])
    out = shadow_probe_run(s2, env.run_id, gateway_factory=lambda category: spy)
    s2.close()
    assert spy.writes == 0
    assert out.results[0].code == sc.CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN


# -- behavioral: the overwrite/backup-load path mutates zero DB rows ----------

def test_overwrite_probe_mutates_no_db_row(mk):
    from app.tests.test_email_shadow_probe import _gwf
    s = mk()
    rec = [{"domain": "a.test", "raw": "local", "class": "local",
            "completeness": "complete", "issue": None}]
    data = {"email_routing_contract": _rt_contract(records=rec)}
    env = _build_run(s, category="email_routing", step_id="email_routing:a.test",
                     src_data=data, dst_data=data)
    ref = _journal(s, env, category="email_routing", step_id="email_routing:a.test",
                   payload={"domain": "a.test", "source_routing": "local"},
                   operation_type="overwrite", backup_ref="ebk_rt")
    before_status = s.get(EmailWriteJournal, ref.id).status
    before_count = s.scalar(select(func.count()).select_from(EmailWriteJournal))
    before_updated = s.get(EmailWriteJournal, ref.id).updated_at
    s.close()
    s2 = mk()
    live = [{"domain": "a.test", "mxcheck": "local"}]
    shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live),
                     backup_loader=lambda *a, **k: {"raw": "remote"})
    s2.close()
    s3 = mk()
    row = s3.get(EmailWriteJournal, ref.id)
    assert row.status == before_status and row.updated_at == before_updated
    assert s3.scalar(select(func.count()).select_from(EmailWriteJournal)) == before_count
    s3.close()


# -- wiring: no production module imports the shadow/policy/live surface -------

def test_shadow_surface_not_imported_by_any_runtime_module():
    api_app = pathlib.Path(__file__).resolve().parents[1]           # .../apps/api/app
    worker_pkg = api_app.parents[2] / "worker" / "worker"           # .../apps/worker/worker
    shadow_basenames = {"email_shadow_classify", "email_shadow_probe",
                        "email_recovery_capability_policy", "forwarder_live_characterization"}
    offenders: list[str] = []
    for root in (api_app, worker_pkg):
        if not root.exists():
            continue
        for py in root.rglob("*.py"):
            if "tests" in py.parts or py.stem in shadow_basenames:
                continue
            tree = ast.parse(py.read_text())
            for node in ast.walk(tree):
                mods: list[str] = []
                if isinstance(node, ast.ImportFrom) and node.module:
                    mods.append(node.module)
                elif isinstance(node, ast.Import):
                    mods.extend(a.name for a in node.names)
                for m in mods:
                    if m.rsplit(".", 1)[-1] in shadow_basenames:
                        offenders.append(f"{py.name} imports {m}")
    assert not offenders, f"runtime import of shadow surface: {offenders}"
