"""
Welcome and goodbye messages of a chat (modules/greetings.py).

One row per chat holds both messages, their on/off switches and the other greeting
settings. A chat without a row, or a message that was never set, uses the defaults.
"""

from typing import Any

from core import database
from core.i18n import tr

DEFAULT_WELCOME = "greetings.default_welcome"
DEFAULT_GOODBYE = "greetings.default_goodbye"

DEFAULTS: dict[str, Any] = {
    "welcome_on": True,
    "welcome_content": None,
    "welcome_media_type": None,
    "welcome_media_file_id": None,
    "goodbye_on": True,
    "goodbye_content": None,
    "goodbye_media_type": None,
    "goodbye_media_file_id": None,
    "welcome_mute": False,
    "clean_welcome": False,
    "clean_service": False,
    "clean_service_types": "",
    "last_welcome_id": None,
}


# The service messages /cleanservice can delete, by kind, and the Message fields that make a message one of them.
CLEAN_TYPES = ("join", "leave", "other", "photo", "pin", "title", "videochat")
SERVICE_FIELDS: dict[str, tuple[str, ...]] = {
    "join": ("new_chat_members",),
    "leave": ("left_chat_member",),
    "photo": ("new_chat_photo", "delete_chat_photo", "chat_background_set"),
    "pin": ("pinned_message",),
    "title": ("new_chat_title", "forum_topic_created", "forum_topic_edited"),
    "videochat": ("video_chat_scheduled", "video_chat_started", "video_chat_ended", "video_chat_participants_invited"),
    "other": (
        "message_auto_delete_timer_changed", "boost_added", "successful_payment", "proximity_alert_triggered",
        "web_app_data", "forum_topic_closed", "forum_topic_reopened", "general_forum_topic_hidden",
        "general_forum_topic_unhidden", "checklist_tasks_done", "checklist_tasks_added", "giveaway_created",
        "giveaway_completed", "write_access_allowed", "paid_message_price_changed", "users_shared", "chat_shared",
    ),
}


def service_kind(message: Any) -> str | None:
    """Which kind of service message this is ("join", "pin"...), or None for an ordinary message."""
    for kind, fields in SERVICE_FIELDS.items():
        for name in fields:
            value = getattr(message, name, None)
            if value is not None and value is not False and value != []:
                return kind
    return None


def clean_types(settings: dict[str, Any]) -> frozenset[str]:
    """The kinds of service messages the chat has deleted. The old on/off switch meant join and leave."""
    kinds = frozenset(word for word in (settings.get("clean_service_types") or "").split(",") if word in CLEAN_TYPES)
    if not kinds and settings.get("clean_service"):
        return frozenset({"join", "leave"})
    return kinds


def describe_types(kinds: frozenset[str] | set[str]) -> str:
    """The kinds as the admins write them: "all", or a list in a fixed order."""
    if set(kinds) >= set(CLEAN_TYPES):
        return "all"
    return ", ".join(kind for kind in CLEAN_TYPES if kind in kinds)


async def set_clean_types(chat_id: int, kinds: frozenset[str] | set[str]) -> None:
    """Save the kinds; the old flag is cleared so that it can never bring join and leave back."""
    await update(
        chat_id,
        clean_service_types=",".join(kind for kind in CLEAN_TYPES if kind in kinds),
        clean_service=False,
    )


async def get(chat_id: int) -> dict[str, Any]:
    row = await database.fetch_one("SELECT * FROM greetings WHERE chat_id = $1", chat_id)
    return {**DEFAULTS, **{key: row[key] for key in DEFAULTS if row is not None and key in row}}


async def update(chat_id: int, **columns: Any) -> None:
    """Change some settings of a chat; the others keep their value."""
    unknown = set(columns) - set(DEFAULTS)
    if unknown or not columns:
        raise ValueError(tr("greetings.unknown_greeting_settings", unknown=sorted(unknown)))
    names = list(columns)
    placeholders = ", ".join(f"${i + 2}" for i in range(len(names)))
    assignments = ", ".join(f"{name} = EXCLUDED.{name}" for name in names)
    await database.execute(
        f"INSERT INTO greetings (chat_id, {', '.join(names)}) VALUES ($1, {placeholders}) "
        f"ON CONFLICT (chat_id) DO UPDATE SET {assignments}",
        chat_id,
        *columns.values(),
    )


def message_of(settings: dict[str, Any], kind: str) -> tuple[str, str | None, str | None]:
    """(text, media type, media file id) to send for "welcome" or "goodbye"."""
    content = settings[f"{kind}_content"]
    media_type = settings[f"{kind}_media_type"]
    if content is None and media_type is None:
        return (tr(DEFAULT_WELCOME) if kind == "welcome" else tr(DEFAULT_GOODBYE)), None, None
    return content or "", media_type, settings[f"{kind}_media_file_id"]
