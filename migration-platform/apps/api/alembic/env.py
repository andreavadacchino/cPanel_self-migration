"""Alembic migration environment."""

from __future__ import annotations

from logging.config import fileConfig

from alembic import context
from sqlalchemy import engine_from_config, pool

from app.core.config import settings
from app.db.base import Base

# Import model modules so every table is registered on Base.metadata.
from app.modules.jobs import models as _jobs_models  # noqa: F401
from app.modules.endpoints import models as _endpoint_models  # noqa: F401
from app.modules.inventory import models as _inventory_models  # noqa: F401
from app.modules.comparison import models as _comparison_models  # noqa: F401
from app.modules.plans import models as _plan_models  # noqa: F401
from app.modules.executions import models as _execution_models  # noqa: F401
from app.modules.readiness import models as _readiness_models  # noqa: F401
from app.modules.migrations import models as _migrations_models  # noqa: F401

config = context.config
config.set_main_option("sqlalchemy.url", settings.database_url)

if config.config_file_name is not None:
    fileConfig(config.config_file_name)

target_metadata = Base.metadata


def run_migrations_offline() -> None:
    context.configure(
        url=settings.database_url,
        target_metadata=target_metadata,
        literal_binds=True,
        dialect_opts={"paramstyle": "named"},
        compare_type=True,
    )
    with context.begin_transaction():
        context.run_migrations()


def run_migrations_online() -> None:
    connectable = engine_from_config(
        config.get_section(config.config_ini_section, {}),
        prefix="sqlalchemy.",
        poolclass=pool.NullPool,
    )
    with connectable.connect() as connection:
        context.configure(
            connection=connection,
            target_metadata=target_metadata,
            compare_type=True,
        )
        with context.begin_transaction():
            context.run_migrations()


if context.is_offline_mode():
    run_migrations_offline()
else:
    run_migrations_online()
