"""The domain error types must not drag in a web framework.

A non-web caller (the Dramatiq worker) needs to raise and catch ConflictError /
NotFoundError / UnprocessableError. It can only do so cleanly if importing
``app.core.errors`` does not import ``fastapi``. This is checked in a *fresh*
interpreter: the pytest process already has fastapi loaded via conftest, so an
in-process ``'fastapi' in sys.modules`` check would prove nothing.
"""

from __future__ import annotations

import subprocess
import sys


def test_importing_domain_errors_does_not_import_fastapi() -> None:
    code = (
        "import sys; import app.core.errors; "
        "leaked = sorted(m for m in sys.modules if m == 'fastapi' or m.startswith('fastapi.')); "
        "assert not leaked, leaked"
    )
    result = subprocess.run(
        [sys.executable, "-c", code], capture_output=True, text=True
    )
    assert result.returncode == 0, result.stderr


def test_domain_errors_are_still_exported() -> None:
    from app.core.errors import ConflictError, NotFoundError, UnprocessableError

    assert issubclass(ConflictError, Exception)
    assert issubclass(NotFoundError, Exception)
    assert issubclass(UnprocessableError, Exception)


def test_error_handlers_module_owns_the_http_translation() -> None:
    # The FastAPI-facing half still exists and reuses the domain classes.
    from app.core.error_handlers import register_error_handlers

    assert callable(register_error_handlers)
