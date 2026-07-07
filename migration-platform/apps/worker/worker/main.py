"""Worker entrypoint.

Launch with:  ``dramatiq worker.main``

Importing this module wires the broker and registers every actor.
"""

from __future__ import annotations

from worker.broker import broker  # noqa: F401  # sets the global broker
from worker.actors import health  # noqa: F401  # registers actors
from worker.actors import preflight  # noqa: F401  # registers run_preflight

__all__ = ["broker", "health", "preflight"]
