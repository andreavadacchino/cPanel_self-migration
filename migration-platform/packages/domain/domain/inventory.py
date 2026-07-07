"""Inventory — what exists on an endpoint (reference only)."""

from __future__ import annotations

from enum import Enum

from pydantic import BaseModel, Field


class InventoryItemKind(str, Enum):
    DOMAIN = "domain"
    EMAIL_ACCOUNT = "email_account"
    DATABASE = "database"
    DNS_ZONE = "dns_zone"
    CRON_JOB = "cron_job"
    FTP_ACCOUNT = "ftp_account"


class InventoryItem(BaseModel):
    kind: InventoryItemKind
    identifier: str
    metadata: dict[str, str] = Field(default_factory=dict)


class Inventory(BaseModel):
    endpoint_host: str
    items: list[InventoryItem] = Field(default_factory=list)
