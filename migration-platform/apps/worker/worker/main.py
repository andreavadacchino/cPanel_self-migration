"""Worker entrypoint.

Launch with:  ``dramatiq worker.main``

Importing this module wires the broker and registers every actor.
"""

from __future__ import annotations

from worker.broker import broker  # noqa: F401  # sets the global broker
from worker.actors import health  # noqa: F401  # registers actors
from worker.actors import preflight  # noqa: F401  # registers actors
from worker.actors import domain_writer  # noqa: F401  # registers actors
from worker.actors import database_writer  # noqa: F401  # registers actors
from worker.actors import mysql_user_writer  # noqa: F401  # registers actors
from worker.actors import forwarder_writer  # noqa: F401  # registers actors
from worker.actors import cron_writer  # noqa: F401  # registers actors
from worker.actors import ftp_writer  # noqa: F401  # registers actors
from worker.actors import mailing_list_writer  # noqa: F401  # registers actors
from worker.actors import dns_writer  # noqa: F401  # registers actors
from worker.actors import autoresponder_writer  # noqa: F401  # registers actors
from worker.actors import mock_orchestrator  # noqa: F401  # registers actors

__all__ = ["broker", "health", "preflight", "domain_writer", "database_writer", "mysql_user_writer", "forwarder_writer", "cron_writer", "ftp_writer", "mailing_list_writer", "dns_writer", "autoresponder_writer", "mock_orchestrator"]
