"""Pure domain models for the migration platform.

These models are intentionally infrastructure-free (no SQLAlchemy, no I/O).
They describe the *concepts* the platform manipulates and act as the shared
vocabulary for the vertical slice that follows Sprint 0:

    Setup -> Preflight -> Comparison -> Plan
"""

from domain.migration import Migration, MigrationStatus
from domain.endpoint import Endpoint, EndpointRole
from domain.job import Job, JobEvent, JobPhase, JobStatus, JobType
from domain.inventory import Inventory, InventoryItem, InventoryItemKind
from domain.comparison import ComparisonEntry, ComparisonReport, ComparisonState
from domain.plan import MigrationPlan, PlanStep, PlanStepKind
from domain.manual_task import ManualTask, ManualTaskStatus

__all__ = [
    "Migration",
    "MigrationStatus",
    "Endpoint",
    "EndpointRole",
    "Job",
    "JobEvent",
    "JobPhase",
    "JobStatus",
    "JobType",
    "Inventory",
    "InventoryItem",
    "InventoryItemKind",
    "ComparisonEntry",
    "ComparisonReport",
    "ComparisonState",
    "MigrationPlan",
    "PlanStep",
    "PlanStepKind",
    "ManualTask",
    "ManualTaskStatus",
]
