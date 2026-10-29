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
    DATABASE_URL: str  # postgresql://user:password@host:5432/dbname

    # --- Logging ----------------------------------------------------------
    LOG_LEVEL: str = "INFO"
    # Developer log: bot start/stop and error reports (tracebacks). Falls back to OWNER_ID.
    DEV_LOG_CHAT: int | None = None
    DEV_LOG_THREAD_ID: int | None = None   # topic id, if DEV_LOG_CHAT is a forum group
    # User log: promotions and demotions (#SUDO, #UNSUDO, ...) and other public
    # information. Falls back to DEV_LOG_CHAT.
    LOG_CHAT_ID: int | None = None
    LOG_THREAD_ID: int | None = None       # topic id, if LOG_CHAT_ID is a forum group

    # --- Housekeeping -----------------------------------------------------
    # How often the bot checks which stored chats it has been removed from, in
    # hours. 0 turns the scheduled check off (/cleanchats still works).
    CHAT_CLEANUP_HOURS: float = 24

    # --- Commands and links -----------------------------------------------
    COMMAND_PREFIXES: str = "/!"        # characters that can start a command
    GITHUB_URL: str | None = "https://github.com/R0Xofficial/ArgoRobot"  # button in /start (private chat)

    # --- Optional external services ---------------------------------------
    # Add API keys here as modules need them, e.g.:
    # GIPHY_API_KEY: str | None = None

    @field_validator("LOG_LEVEL")
    @classmethod
    def _normalize_log_level(cls, value: str) -> str:
        return value.strip().upper()

    @field_validator("DATABASE_URL")
    @classmethod
    def _check_database_url(cls, value: str) -> str:
        value = value.strip()
        if not value.startswith(("postgresql://", "postgres://")):
            raise ValueError("must start with postgresql://")
        return value

    @field_validator("COMMAND_PREFIXES")
    @classmethod
    def _check_prefixes(cls, value: str) -> str:
        value = value.strip()
        if not value or any(ch.isalnum() or ch.isspace() for ch in value):
            raise ValueError("must contain only symbol characters, e.g. /!")
        return value

    @field_validator("GITHUB_URL")
    @classmethod
    def _check_github_url(cls, value: str | None) -> str | None:
        if value is None:
            return None
        value = value.strip()
        if value and not value.startswith(("https://", "http://")):
            raise ValueError("must start with https://")
        return value or None

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
