"""Adapter stubs for external systems.

Sprint 0 deliberately ships *no* real integration. Each adapter raises
``NotImplementedError`` so that accidental use is loud and obvious. Real
implementations land in later sprints behind these same interfaces.
"""

__all__ = ["cpanel", "ssh", "imap"]
