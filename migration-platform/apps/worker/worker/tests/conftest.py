"""Force the StubBroker so worker tests never need a live Redis."""

from __future__ import annotations

import os

# Must be set before any worker.* module is imported by the tests.
os.environ.setdefault("DRAMATIQ_TESTING", "1")
# A fixed dev Fernet key so endpoint-token decryption works under tests.
os.environ.setdefault(
    "PLATFORM_SECRET_KEY", "u1TjglXJeq9grsU-BA_BCAp8reG_XfT14fT_lHZ-PBA="
)
