"""Force the StubBroker so worker tests never need a live Redis."""

from __future__ import annotations

import os

# Must be set before any worker.* module is imported by the tests.
os.environ.setdefault("DRAMATIQ_TESTING", "1")
