"""
config.py - central configuration.

Values are read from environment variables or from a .env file placed next
to this file. Secrets (bot token, API keys) must never be hardcoded here,
because this file is committed to the repository. Copy .env.example to .env
and fill it in.

Usage anywhere in the project:

    from config import settings
    settings.BOT_TOKEN
"""

import sys
from pathlib import Path

from pydantic import ValidationError, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict

BASE_DIR = Path(__file__).resolve().parent


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=BASE_DIR / ".env",
        env_file_encoding="utf-8",
        env_ignore_empty=True,  # "LOG_CHAT_ID=" in .env counts as "not set"
        extra="ignore",
    )

    # --- Required ---------------------------------------------------------
    BOT_TOKEN: str
    OWNER_ID: int

    # --- Logging ----------------------------------------------------------
    LOG_LEVEL: str = "INFO"
    LOG_CHAT_ID: int | None = None      # chat for service messages (falls back to OWNER_ID)
    LOG_THREAD_ID: int | None = None    # topic id, if LOG_CHAT_ID is a forum group

    # --- Storage ----------------------------------------------------------
    DATABASE_PATH: Path = BASE_DIR / "data" / "argo.db"

    # --- Optional external services ---------------------------------------
    # Add API keys here as modules need them, e.g.:
    # GIPHY_API_KEY: str | None = None

    @field_validator("LOG_LEVEL")
    @classmethod
    def _normalize_log_level(cls, value: str) -> str:
        return value.strip().upper()

    @field_validator("BOT_TOKEN")
    @classmethod
    def _check_token(cls, value: str) -> str:
        value = value.strip()
        if ":" not in value:
            raise ValueError("does not look like a Telegram bot token")
        return value


def _load() -> Settings:
    try:
        return Settings()
    except ValidationError as exc:
        # Print a readable message instead of a traceback.
        lines = ["Configuration error. Check your .env file:"]
        for err in exc.errors():
            name = ".".join(str(part) for part in err["loc"]) or "settings"
            lines.append(f"  - {name}: {err['msg']}")
        sys.exit("\n".join(lines))


settings = _load()
