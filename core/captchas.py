"""
Captchas for new members (modules/captcha.py has the commands).

A chat that turned captchas on (/captcha on) mutes everybody who joins until they pass a
test. The test is one of four kinds (/captchamode):

  button  press one button
  math    pick the answer to a small sum from six buttons
  text    the code is written in the message; its characters sit scrambled on buttons and
          must be pressed in the order of the code
  text2   the same, but the code is a picture

When the chat uses join requests, the test is sent to the person privately and the request
is approved once they pass it. Optionally (/captcharules) they must then accept the rules.

Who still has to solve a captcha is kept in the database (table captcha_pending), so a
restart loses nobody. A background sweep (start_sweeper) kicks people whose time ran out
(/captchakick, /captchakicktime), unmutes the ones whose mute time ended (/captchamutetime)
and lets go of everybody when the chat or the whole bot turns captchas off.

Welcome messages wait for the captcha: modules/greetings.py asks is_held() and registers
on_released() to greet people once they are through.
"""

import asyncio
import io
import logging
import random
import time
from contextlib import suppress
from datetime import datetime, timedelta, timezone
from typing import Any, Awaitable, Callable

from aiogram import Bot
from aiogram.exceptions import TelegramAPIError
from aiogram.types import BufferedInputFile, ChatPermissions, InlineKeyboardButton, InlineKeyboardMarkup, User

from core import approvals, database, disabled, immunity, markup, registry, roles, rulestore
from core import greetings as welcome_store
from core.text import duration_text, esc, user_link

log = logging.getLogger(__name__)

MODES = ("button", "math", "text", "text2")
DEFAULTS: dict[str, Any] = {
    "enabled": False,
    "mode": "button",
    "rules": False,
    "mute_seconds": None,  # None: muted until the captcha is solved
    "kick": False,
    "kick_seconds": 300,
    "button_text": None,
}
DEFAULT_BUTTON = "I'm not a bot"
ACCEPT_BUTTON = "I accept the rules"

CODE_LENGTH = 5
ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"  # nothing that looks like something else
REQUEST_LIFETIME = timedelta(hours=24)  # a join request that is not kicked is forgotten after this
RECENT_SECONDS = 300  # someone who just passed is not tested again for this long
SWEEP_EVERY = 20
WELCOME_GRACE = 30  # seconds a welcome message has to carry the button before a captcha message is sent instead
WELCOME_LIMIT = 5  # people greeted for one "joined" message (modules/greetings.py sets the real value)
MAX_RULES_HTML = 2500

MUTED = ChatPermissions(can_send_messages=False)

PREFIX = "cap"  # cap:<id>:<button>, a button of a captcha message
RULES_PREFIX = "capr"
WELCOME_PREFIX = "capw"  # capw:<chat id>, the button under a welcome message: the same for everybody

Hook = Callable[[Bot, int, User, "int | None"], Awaitable[None]]
_hooks: list[Hook] = []
_recent: dict[tuple[int, int], float] = {}
_locks: dict[int, asyncio.Lock] = {}
_cache: dict[int, dict[str, Any]] = {}


# --- Settings ------------------------------------------------------------------------------------

async def settings(chat_id: int) -> dict[str, Any]:
    cached = _cache.get(chat_id)
    if cached is not None:
        return cached
    row = await database.fetch_one("SELECT * FROM captcha_settings WHERE chat_id = $1", chat_id)
    value = {**DEFAULTS, **{key: row[key] for key in DEFAULTS if row is not None and key in row}}
    if len(_cache) >= 5000:
        _cache.clear()
    _cache[chat_id] = value
    return value


async def update(chat_id: int, **columns: Any) -> None:
    """Change some settings of a chat; the others keep their value."""
    unknown = set(columns) - set(DEFAULTS)
    if unknown or not columns:
        raise ValueError(f"unknown captcha settings: {sorted(unknown)}")
    names = list(columns)
    marks = ", ".join(f"${i + 2}" for i in range(len(names)))
    sets = ", ".join(f"{name} = EXCLUDED.{name}" for name in names)
    await database.execute(
        f"INSERT INTO captcha_settings (chat_id, {', '.join(names)}) VALUES ($1, {marks}) "
        f"ON CONFLICT (chat_id) DO UPDATE SET {sets}",
        chat_id,
        *columns.values(),
    )
    _cache.pop(chat_id, None)


def forget(chat_id: int) -> None:
    _cache.pop(chat_id, None)


async def active(chat_id: int) -> dict[str, Any] | None:
    """The chat's settings when captchas are in force there, else None. Never raises."""
    try:
        info = registry.get("captcha")
        if info is None or not info.enabled or await disabled.module_off(chat_id, "captcha"):
            return None
        config = await settings(chat_id)
        return config if config["enabled"] else None
    except Exception:
        log.exception("Looking up the captcha settings failed")
        return None


def on_released(hook: Hook) -> None:
    """Call hook(bot, chat_id, user, join_message_id) for everybody who is let into a chat."""
    _hooks.append(hook)


# --- The tests -------------------------------------------------------------------------------------

def make(mode: str, rng: Any = random) -> dict[str, str]:
    """A new test: its question, the right answer and the order of the buttons."""
    if mode == "math":
        kind = rng.choice("+-x")
        if kind == "+":
            a, b = rng.randint(2, 20), rng.randint(2, 20)
            result = a + b
        elif kind == "-":
            a = rng.randint(10, 30)
            b = rng.randint(2, a - 1)
            result = a - b
        else:
            a, b = rng.randint(2, 9), rng.randint(2, 9)
            result = a * b
        options = {result}
        while len(options) < 6:
            wrong = result + rng.choice((-1, 1)) * rng.randint(1, 10)
            if wrong >= 0:
                options.add(wrong)
        layout = list(options)
        rng.shuffle(layout)
        symbol = "×" if kind == "x" else kind
        return {"question": f"{a} {symbol} {b} = ?", "answer": str(result), "layout": ",".join(str(o) for o in layout)}
    if mode in ("text", "text2"):
        code = "".join(rng.sample(ALPHABET, CODE_LENGTH))
        letters = list(code)
        while True:
            rng.shuffle(letters)
            if "".join(letters) != code:
                break
        return {"question": "", "answer": code, "layout": "".join(letters)}
    return {"question": "", "answer": "", "layout": ""}


def _glyph(char: str, size: int):
    """One character as a grey image, whatever fonts the machine has."""
    from PIL import Image, ImageDraw, ImageFont

    for name in ("DejaVuSans-Bold.ttf", "DejaVuSans.ttf", "Arial Bold.ttf", "arial.ttf", "LiberationSans-Bold.ttf"):
        try:
            font = ImageFont.truetype(name, size)
        except OSError:
            continue
        canvas = Image.new("L", (size * 2, size * 2), 0)
        ImageDraw.Draw(canvas).text((size // 2, size // 4), char, font=font, fill=255)
        return canvas.crop(canvas.getbbox() or (0, 0, size, size))
    # No scalable font: draw it small with the built-in one and enlarge it.
    small = Image.new("L", (16, 16), 0)
    ImageDraw.Draw(small).text((2, 2), char, font=ImageFont.load_default(), fill=255)
    small = small.crop(small.getbbox() or (0, 0, 8, 8))
    factor = max(1, size // max(small.size[1], 1))
    return small.resize((small.size[0] * factor, small.size[1] * factor), Image.BICUBIC)


def picture(code: str, rng: Any = random) -> bytes:
    """The code as a PNG: tilted letters on a noisy background."""
    from PIL import Image, ImageDraw, ImageFilter

    size = 46
    width, height = 40 + len(code) * (size + 8), size + 70
    canvas = Image.new("RGB", (width, height), (rng.randint(225, 245),) * 3)
    draw = ImageDraw.Draw(canvas)
    for _ in range(14):
        colour = tuple(rng.randint(120, 200) for _ in range(3))
        draw.line([(rng.randint(0, width), rng.randint(0, height)), (rng.randint(0, width), rng.randint(0, height))],
                  fill=colour, width=rng.randint(1, 3))
    x = 24
    for char in code:
        glyph = _glyph(char, size).rotate(rng.randint(-22, 22), expand=True, resample=Image.BICUBIC)
        colour = tuple(rng.randint(20, 110) for _ in range(3))
        layer = Image.new("RGB", glyph.size, colour)
        canvas.paste(layer, (x, rng.randint(14, height - glyph.size[1] - 14)), glyph)
        x += size + 8
    for _ in range(260):
        draw.point((rng.randint(0, width - 1), rng.randint(0, height - 1)), fill=(rng.randint(0, 160),) * 3)
    for _ in range(5):
        colour = tuple(rng.randint(60, 160) for _ in range(3))
        draw.line([(0, rng.randint(0, height)), (width, rng.randint(0, height))], fill=colour, width=1)
    canvas = canvas.filter(ImageFilter.SMOOTH)
    out = io.BytesIO()
    canvas.save(out, "PNG")
    return out.getvalue()


def picture_works() -> bool:
    try:
        return bool(picture("AB2"))
    except Exception:
        log.exception("The picture captcha can't be drawn")
        return False


# --- Messages -------------------------------------------------------------------------------------

def _taken(row: dict[str, Any]) -> list[int]:
    return [int(x) for x in (row["taken"] or "").split(",") if x]


def _pretty(seconds: float) -> str:
    return duration_text(max(1, int(round(seconds))))


def _rules_html(rules: dict[str, Any] | None, chat_title: str, name: str) -> str | None:
    """The rules ready to be shown inside the captcha message, or None when there are none worth showing."""
    if not rules or not rules.get("content"):
        return None
    try:
        parsed = markup.parse(markup.pick(rules["content"]), {
            "first": name, "last": "", "fullname": name, "username": "", "mention": name,
            "id": "", "chatname": chat_title, "count": "",
        })
    except Exception:
        return None
    return parsed.html if len(parsed.html) <= MAX_RULES_HTML else None


async def body(row: dict[str, Any]) -> str:
    """The text of the captcha message for the person's current state."""
    title = esc(row["title"] or "this chat")
    name = row["name"] or "there"
    if row["phase"] == "rules":
        rules = _rules_html(await rulestore.get(row["chat_id"]), row["title"] or "", name)
        shown = rules if rules is not None else "Read the rules of the chat with /rules there, then come back to accept them."
        lead = f"{user_link(row['user_id'], name)}, one last thing: read the rules of <b>{title}</b> and accept them." \
            if row["kind"] == "join" and row["message_chat_id"] == row["chat_id"] \
            else f"Last step: read the rules of <b>{title}</b> and accept them."
        return f"{lead}\n\n{shown}"

    if row["kind"] == "request":
        lead = f"Hi {esc(name)}! You sent a request to join <b>{title}</b>. Pass this captcha and I'll accept it right away."
    elif row["message_chat_id"] != row["chat_id"]:
        lead = f"Hi {esc(name)}! To speak in <b>{title}</b>, prove you're human first."
    else:
        lead = f"{user_link(row['user_id'], name)}, welcome to <b>{title}</b>! Prove you're human before you speak."

    mode = row["mode"]
    if mode == "button":
        task = "Press the button below."
    elif mode == "math":
        task = f"Solve this and press the answer:\n\n<b>{esc(row['question'])}</b>"
    else:
        typed = "".join(row["layout"][i] for i in _taken(row))
        progress = typed + "•" * (len(row["answer"]) - len(typed))
        if mode == "text":
            task = f"Press the characters in this order:\n\n<code>{esc(row['answer'])}</code>\n\nSo far: <code>{progress}</code>"
        else:
            task = f"Press the characters of the code in the picture, in order.\n\nSo far: <code>{progress}</code>"
    note = ""
    if row["kick_at"] is not None:
        note = f"\n\nYou have {_pretty((row['kick_at'] - row['created_at']).total_seconds())} to do this or you'll be removed."
    return f"{lead}\n\n{task}{note}"


def keyboard(row: dict[str, Any], button_text: str | None) -> InlineKeyboardMarkup:
    pid = row["id"]
    if row["phase"] == "rules":
        return InlineKeyboardMarkup(inline_keyboard=[[InlineKeyboardButton(text=ACCEPT_BUTTON, callback_data=f"{RULES_PREFIX}:{pid}")]])
    mode = row["mode"]
    if mode == "button":
        return InlineKeyboardMarkup(inline_keyboard=[
            [InlineKeyboardButton(text=button_text or DEFAULT_BUTTON, callback_data=f"{PREFIX}:{pid}:0")]
        ])
    if mode == "math":
        options = row["layout"].split(",")
        buttons = [InlineKeyboardButton(text=text, callback_data=f"{PREFIX}:{pid}:{i}") for i, text in enumerate(options)]
        per_row = 3
    else:
        taken = set(_taken(row))
        buttons = [
            InlineKeyboardButton(text=char, callback_data=f"{PREFIX}:{pid}:{i}")
            for i, char in enumerate(row["layout"]) if i not in taken
        ]
        per_row = 5
    return InlineKeyboardMarkup(inline_keyboard=[buttons[i:i + per_row] for i in range(0, len(buttons), per_row)])


async def _show(bot: Bot, row: dict[str, Any], button_text: str | None) -> None:
    """Bring the message up to date with the row."""
    text, markup_ = await body(row), keyboard(row, button_text)
    try:
        if row["is_photo"]:
            await bot.edit_message_caption(chat_id=row["message_chat_id"], message_id=row["message_id"], caption=text, reply_markup=markup_)
        else:
            await bot.edit_message_text(chat_id=row["message_chat_id"], message_id=row["message_id"], text=text, reply_markup=markup_)
    except TelegramAPIError as exc:
        if "not modified" not in str(exc).lower():
            log.info("Could not update captcha message %s: %s", row["message_id"], exc)


async def _remove_message(bot: Bot, row: dict[str, Any]) -> None:
    if row.get("message_id"):
        with suppress(TelegramAPIError):
            await bot.delete_message(row["message_chat_id"], row["message_id"])


# --- Rows -----------------------------------------------------------------------------------------

async def get(pending_id: int) -> dict[str, Any] | None:
    return await database.fetch_one("SELECT * FROM captcha_pending WHERE id = $1", pending_id)


async def find(chat_id: int, user_id: int) -> dict[str, Any] | None:
    return await database.fetch_one("SELECT * FROM captcha_pending WHERE chat_id = $1 AND user_id = $2", chat_id, user_id)


async def is_held(chat_id: int, user_id: int) -> bool:
    """Whether the person still has to solve a captcha in that chat."""
    return await find(chat_id, user_id) is not None


async def _delete(pending_id: int) -> None:
    await database.execute("DELETE FROM captcha_pending WHERE id = $1", pending_id)


def _passed(chat_id: int, user_id: int) -> None:
    now = time.monotonic()
    if len(_recent) > 2000:
        for key in [k for k, t in _recent.items() if now - t > RECENT_SECONDS]:
            del _recent[key]
    _recent[(chat_id, user_id)] = now


def _just_passed(chat_id: int, user_id: int) -> bool:
    started = _recent.get((chat_id, user_id))
    return started is not None and time.monotonic() - started < RECENT_SECONDS


async def _exempt(bot: Bot, chat_id: int, user: User) -> bool:
    if user.is_bot or user.id == bot.id or immunity.is_immune(user.id) or _just_passed(chat_id, user.id):
        return True
    return await roles.get_role(user.id) is not None or await approvals.is_approved(chat_id, user.id)


async def _insert(chat, user, kind: str, config: dict[str, Any], join_message_id: int | None, message_chat_id: int | None, attached: bool = False) -> dict[str, Any]:
    mode = config["mode"] if config["mode"] in MODES else "button"
    if mode == "text2" and not picture_works():
        mode = "text"
    test = make(mode)
    now = datetime.now(timezone.utc)
    kick_at = now + timedelta(seconds=config["kick_seconds"]) if config["kick"] else None
    unmute_at = now + timedelta(seconds=config["mute_seconds"]) if kind == "join" and config["mute_seconds"] else None
    expires_at = now + REQUEST_LIFETIME if kind == "request" and kick_at is None else None
    return await database.fetch_one(
        """
        INSERT INTO captcha_pending
            (chat_id, user_id, kind, mode, question, answer, layout, name, title,
             message_chat_id, join_message_id, created_at, unmute_at, kick_at, expires_at, attached)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
        RETURNING *
        """,
        chat.id, user.id, kind, mode, test["question"], test["answer"], test["layout"],
        user.full_name, chat.title, message_chat_id, join_message_id, now, unmute_at, kick_at, expires_at, attached,
    )


async def _present(bot: Bot, row: dict[str, Any], config: dict[str, Any]) -> bool:
    """Send the captcha message. False when it could not be sent."""
    text, markup_ = await body(row), keyboard(row, config["button_text"])
    try:
        if row["mode"] == "text2":
            sent = await bot.send_photo(
                chat_id=row["message_chat_id"], photo=BufferedInputFile(picture(row["answer"]), "captcha.png"),
                caption=text, reply_markup=markup_,
            )
        else:
            sent = await bot.send_message(chat_id=row["message_chat_id"], text=text, reply_markup=markup_)
    except TelegramAPIError as exc:
        log.info("Could not send a captcha to %s: %s", row["message_chat_id"], exc)
        return False
    await database.execute(
        "UPDATE captcha_pending SET message_id = $2, is_photo = $3 WHERE id = $1",
        row["id"], sent.message_id, row["mode"] == "text2",
    )
    row["message_id"], row["is_photo"] = sent.message_id, row["mode"] == "text2"
    return True


# --- Letting people in or out -----------------------------------------------------------------------

async def _default_permissions(bot: Bot, chat_id: int) -> ChatPermissions:
    try:
        permissions = (await bot.get_chat(chat_id)).permissions
    except TelegramAPIError:
        permissions = None
    return permissions or ChatPermissions(can_send_messages=True)


async def _status_of(bot: Bot, chat_id: int, user_id: int) -> tuple[str | None, Any]:
    try:
        member = await bot.get_chat_member(chat_id, user_id)
    except TelegramAPIError:
        return None, None
    return str(getattr(member.status, "value", member.status)), member


async def _unmute(bot: Bot, chat_id: int, user_id: int) -> bool:
    """Give the right to write back. False when the person is gone or was changed by someone else."""
    status, member = await _status_of(bot, chat_id, user_id)
    if status != "restricted" or getattr(member, "can_send_messages", False) is not False:
        return status in ("member", "administrator", "creator")
    try:
        await bot.restrict_chat_member(chat_id, user_id, permissions=await _default_permissions(bot, chat_id))
    except TelegramAPIError as exc:
        log.info("Could not unmute %s in %s: %s", user_id, chat_id, exc)
        return False
    return True


async def _welcome(bot: Bot, row: dict[str, Any], user: User | None) -> None:
    user = user or User(id=row["user_id"], is_bot=False, first_name=row["name"] or "User")
    for hook in list(_hooks):
        try:
            await hook(bot, row["chat_id"], user, row["join_message_id"])
        except Exception:
            log.exception("A captcha hook failed")


async def release(bot: Bot, row: dict[str, Any], user: User | None = None, *, greet: bool = True) -> None:
    """The person is through (or let go): unmute, tidy up, welcome."""
    _passed(row["chat_id"], row["user_id"])
    await _delete(row["id"])
    if row["message_id"] and row["message_chat_id"] != row["chat_id"] and row["kind"] == "join":
        await _say(bot, row, f"Done! You can speak in <b>{esc(row['title'] or 'the chat')}</b> now.")  # solved in a private chat
    else:
        await _remove_message(bot, row)
    if row["kind"] == "request":
        return
    # A welcome message that carried the button was sent when the person joined: no second one.
    if await _unmute(bot, row["chat_id"], row["user_id"]) and greet and not row["attached"]:
        await _welcome(bot, row, user)


async def _say(bot: Bot, row: dict[str, Any], text: str) -> None:
    """Replace the captcha message's text and take its buttons away."""
    with suppress(TelegramAPIError):
        if row["is_photo"]:
            await bot.edit_message_caption(chat_id=row["message_chat_id"], message_id=row["message_id"], caption=text, reply_markup=None)
        else:
            await bot.edit_message_text(chat_id=row["message_chat_id"], message_id=row["message_id"], text=text, reply_markup=None)


async def approve_request(bot: Bot, row: dict[str, Any]) -> None:
    _passed(row["chat_id"], row["user_id"])
    await _delete(row["id"])
    try:
        await bot.approve_chat_join_request(row["chat_id"], row["user_id"])
    except TelegramAPIError as exc:
        log.info("Could not approve the join request of %s in %s: %s", row["user_id"], row["chat_id"], exc)
        await _say(bot, row, "You passed, but I couldn't approve your request. Ask the admins to do it.")
        return
    await _say(bot, row, f"Done. Welcome to <b>{esc(row['title'] or 'the chat')}</b>!")


async def kick(bot: Bot, row: dict[str, Any]) -> None:
    """Time ran out: remove the person (they may try again) or dismiss their request."""
    await _delete(row["id"])
    if row["kind"] == "request":
        with suppress(TelegramAPIError):
            await bot.decline_chat_join_request(row["chat_id"], row["user_id"])
        await _say(bot, row, f"Time's up. Your request to join <b>{esc(row['title'] or 'the chat')}</b> was dismissed. You can ask again.")
        return
    await _remove_message(bot, row)
    status, _member = await _status_of(bot, row["chat_id"], row["user_id"])
    if status in ("left", "kicked"):
        return
    try:
        await bot.ban_chat_member(row["chat_id"], row["user_id"])
        await bot.unban_chat_member(row["chat_id"], row["user_id"], only_if_banned=True)
    except TelegramAPIError as exc:
        log.info("Could not remove %s from %s: %s", row["user_id"], row["chat_id"], exc)


async def drop(bot: Bot, row: dict[str, Any]) -> None:
    """Forget the captcha without touching the person (a request stays for the admins)."""
    await _delete(row["id"])
    await _remove_message(bot, row)


async def drop_user(bot: Bot, chat_id: int, user_id: int) -> None:
    """
    The person left (or was removed): their captcha message is pointless now. The row stays:
    a muted person who comes back is still muted, so they must be recognised and tested again.
    """
    row = await find(chat_id, user_id)
    if row is not None and row["kind"] == "join" and row["message_id"]:
        await _remove_message(bot, row)
        await database.execute("UPDATE captcha_pending SET message_id = NULL WHERE id = $1", row["id"])


async def release_chat(bot: Bot, chat_id: int) -> int:
    """Captchas were turned off in the chat: nobody stays muted. Returns how many people were let go."""
    rows = await database.fetch_all("SELECT * FROM captcha_pending WHERE chat_id = $1", chat_id)
    for row in rows:
        await _let_go(bot, row)
    return len(rows)


async def _let_go(bot: Bot, row: dict[str, Any]) -> None:
    if row["kind"] == "request":
        await drop(bot, row)
    else:
        await release(bot, row)


# --- Joining ---------------------------------------------------------------------------------------

async def welcome_available(chat_id: int) -> bool:
    """Whether the chat welcomes newcomers. Captchas ride on the welcome message, so they need it."""
    try:
        info = registry.get("greetings")
        if info is None or not info.enabled or await disabled.module_off(chat_id, "greetings"):
            return False
        return bool((await welcome_store.get(chat_id))["welcome_on"])
    except Exception:
        log.exception("Looking up the welcome settings failed")
        return False


async def hold(bot: Bot, chat, user: User, join_message_id: int | None, index: int = 0) -> bool:
    """
    Mute a newcomer and give them a captcha when the chat wants one. True when they are held.

    Captchas need the welcome message: it carries the button (welcome_for), which is the same
    for every newcomer and works for whoever still has a captcha. Without welcomes nobody is
    muted. A newcomer whose welcome fails to carry the button gets a captcha message of their
    own after a while (the sweep).
    """
    config = await active(chat.id)
    if config is None or not await welcome_available(chat.id) or await _exempt(bot, chat.id, user):
        return False
    old = await find(chat.id, user.id)
    status, _member = await _status_of(bot, chat.id, user.id)
    if status != "member" and not (status == "restricted" and old is not None):
        return False  # an admin, restricted by someone else or gone: nothing to test
    try:
        await bot.restrict_chat_member(chat.id, user.id, permissions=MUTED)
    except TelegramAPIError as exc:
        log.info("Could not mute %s for a captcha in %s: %s", user.id, chat.id, exc)
        return False
    if old is not None:
        await drop(bot, old)
    row = await _insert(chat, user, "join", config, join_message_id, chat.id, True)
    if index >= WELCOME_LIMIT:
        # No welcome of their own, but the button under the others works for everybody.
        await mark_welcomed(chat.id, user.id)
    return True


async def hold_request(bot: Bot, chat, user: User, user_chat_id: int) -> bool:
    """A join request: send the person a captcha privately. True when it was sent."""
    config = await active(chat.id)
    if config is None or await _exempt(bot, chat.id, user):
        return False
    old = await find(chat.id, user.id)
    if old is not None:
        await drop(bot, old)
    row = await _insert(chat, user, "request", config, None, user_chat_id)
    if not await _present(bot, row, config):
        await _delete(row["id"])
        return False
    return True


# --- The welcome message's button -----------------------------------------------------------------

async def welcome_button(bot: Bot, chat_id: int) -> InlineKeyboardButton:
    """The button under welcome messages. It is the same for everybody: whoever presses it is looked up."""
    config = await settings(chat_id)
    text = config["button_text"] or DEFAULT_BUTTON
    if config["mode"] == "button" and not (config["rules"] and _rules_exist(await rulestore.get(chat_id))):
        return InlineKeyboardButton(text=text, callback_data=f"{WELCOME_PREFIX}:{chat_id}")
    username = (await bot.me()).username
    return InlineKeyboardButton(text=text, url=f"https://t.me/{username}?start=cap_{chat_id}")


async def welcome_for(bot: Bot, chat_id: int, user_id: int) -> tuple[bool, InlineKeyboardButton | None]:
    """
    For modules/greetings.py: (skip, button) for a newcomer. skip: they have a captcha message
    of their own and are welcomed once through. button: the verify button for their welcome.
    """
    row = await find(chat_id, user_id)
    if row is None or row["kind"] != "join":
        return False, None
    if not row["attached"]:
        return True, None
    return False, await welcome_button(bot, chat_id)


async def welcome_pressed(bot: Bot, chat_id: int, user: User) -> tuple[str | None, bool, str | None]:
    """
    Somebody pressed the button under a welcome message. Returns (note, alert, url): what to
    tell them, whether as an alert, and a link to open (the private chat) instead.
    """
    row = await find(chat_id, user.id)
    if row is None or row["kind"] != "join":
        return "You're already verified.", True, None
    config = await settings(chat_id)
    simple = row["mode"] == "button" and row["attached"] and not (config["rules"] and _rules_exist(await rulestore.get(chat_id)))
    if simple:
        return await press(bot, row, 0, user), False, None
    username = (await bot.me()).username
    return None, False, f"https://t.me/{username}?start=cap_{chat_id}"


async def mark_welcomed(chat_id: int, user_id: int) -> None:
    await database.execute("UPDATE captcha_pending SET welcomed = TRUE WHERE chat_id = $1 AND user_id = $2", chat_id, user_id)


async def open_in_private(bot: Bot, user: User, chat_id: int, private_chat_id: int) -> str | None:
    """The person came to the private chat from the button under a welcome message. None, or what to tell them."""
    row = await find(chat_id, user.id)
    if row is None or row["kind"] != "join":
        return "You're already verified, or this captcha has expired."
    config = await active(row["chat_id"])
    if config is None:
        return "Captchas are switched off in that chat now."
    if row["message_id"]:
        await _remove_message(bot, row)
    await database.execute(
        "UPDATE captcha_pending SET message_chat_id = $2, message_id = NULL, is_photo = FALSE WHERE id = $1",
        row["id"], private_chat_id,
    )
    row.update(message_chat_id=private_chat_id, message_id=None, is_photo=False)
    if not await _present(bot, row, config):
        return "I couldn't send you the captcha. Try again."
    return None


# --- Pressing buttons --------------------------------------------------------------------------------

def _lock(pending_id: int) -> asyncio.Lock:
    if len(_locks) > 500:
        _locks.clear()
    return _locks.setdefault(pending_id, asyncio.Lock())


async def _after_test(bot: Bot, row: dict[str, Any], config: dict[str, Any], user: User) -> None:
    """The test is passed: the rules come next when the chat wants them accepted, else the person is in."""
    in_welcome = row["attached"] and row["message_chat_id"] == row["chat_id"]  # no message of ours to show the rules in
    if config["rules"] and not in_welcome and _rules_exist(await rulestore.get(row["chat_id"])):
        await database.execute("UPDATE captcha_pending SET phase = 'rules' WHERE id = $1", row["id"])
        row["phase"] = "rules"
        await _show(bot, row, config["button_text"])
        return
    await _finish(bot, row, user)


def _rules_exist(rules: dict[str, Any] | None) -> bool:
    return bool(rules and rules.get("content"))


async def _finish(bot: Bot, row: dict[str, Any], user: User) -> None:
    if row["kind"] == "request":
        await approve_request(bot, row)
    else:
        await release(bot, row, user)


async def press(bot: Bot, row: dict[str, Any], index: int, user: User) -> str | None:
    """
    A button of the test was pressed. Returns a short note to show the person, or None.
    The caller has checked that the button is theirs.
    """
    async with _lock(row["id"]):
        row = await get(row["id"])
        if row is None or row["phase"] != "captcha":
            return "This captcha is no longer valid."
        config = await settings(row["chat_id"])
        mode = row["mode"]

        if mode == "button":
            from_welcome = row["attached"] and row["message_chat_id"] == row["chat_id"]
            await _after_test(bot, row, config, user)
            return "You're verified. Welcome!" if from_welcome else None

        if mode == "math":
            options = row["layout"].split(",")
            if 0 <= index < len(options) and options[index] == row["answer"]:
                await _after_test(bot, row, config, user)
                return None
            fresh = make("math")
            await database.execute(
                "UPDATE captcha_pending SET question = $2, answer = $3, layout = $4 WHERE id = $1",
                row["id"], fresh["question"], fresh["answer"], fresh["layout"],
            )
            row.update(question=fresh["question"], answer=fresh["answer"], layout=fresh["layout"])
            await _show(bot, row, config["button_text"])
            return "Wrong. Here's a new one."

        # text and text2
        taken = _taken(row)
        if not 0 <= index < len(row["layout"]) or index in taken:
            return None
        if row["layout"][index] != row["answer"][len(taken)]:
            await database.execute("UPDATE captcha_pending SET taken = '' WHERE id = $1", row["id"])
            row["taken"] = ""
            await _show(bot, row, config["button_text"])
            return "Wrong order. Start again."
        taken.append(index)
        row["taken"] = ",".join(str(i) for i in taken)
        await database.execute("UPDATE captcha_pending SET taken = $2 WHERE id = $1", row["id"], row["taken"])
        if len(taken) == len(row["answer"]):
            await _after_test(bot, row, config, user)
        else:
            await _show(bot, row, config["button_text"])
        return None


async def accept_rules(bot: Bot, row: dict[str, Any], user: User) -> str | None:
    async with _lock(row["id"]):
        row = await get(row["id"])
        if row is None or row["phase"] != "rules":
            return "This is no longer valid."
        await _finish(bot, row, user)
        return None


# --- The sweep ---------------------------------------------------------------------------------------

async def _handle(bot: Bot, row: dict[str, Any], now: datetime) -> None:
    if await active(row["chat_id"]) is None:
        await _let_go(bot, row)  # captchas were turned off while the person waited
    elif (row["attached"] and not row["welcomed"] and row["message_id"] is None and row["message_chat_id"] == row["chat_id"]
          and (now - row["created_at"]).total_seconds() > WELCOME_GRACE):
        # The welcome message never carried the button (it failed, or was never sent): send a captcha of our own.
        await database.execute("UPDATE captcha_pending SET attached = FALSE WHERE id = $1", row["id"])
        row["attached"] = False
        await _present(bot, row, await settings(row["chat_id"]))
    elif row["kick_at"] is not None and row["kick_at"] <= now:
        await kick(bot, row)
    elif row["unmute_at"] is not None and row["unmute_at"] <= now:
        await release(bot, row)
    elif row["expires_at"] is not None and row["expires_at"] <= now:
        await drop(bot, row)


async def sweep(bot: Bot) -> None:
    rows = await database.fetch_all("SELECT * FROM captcha_pending ORDER BY id LIMIT 500")
    now = datetime.now(timezone.utc)
    for row in rows:
        try:
            await _handle(bot, row, now)
        except asyncio.CancelledError:
            raise
        except Exception:
            log.exception("Handling a captcha of %s in %s failed", row["user_id"], row["chat_id"])


async def _sweeper(bot: Bot) -> None:
    while True:
        try:
            await sweep(bot)
        except asyncio.CancelledError:
            raise
        except Exception:
            log.exception("Captcha sweep failed")
        await asyncio.sleep(SWEEP_EVERY)


_task: asyncio.Task | None = None


def start_sweeper(bot: Bot) -> None:
    global _task
    if _task is None:
        _task = asyncio.get_running_loop().create_task(_sweeper(bot), name="captcha-sweeper")


async def stop_sweeper() -> None:
    global _task
    if _task is None:
        return
    _task.cancel()
    try:
        await _task
    except asyncio.CancelledError:
        pass
    _task = None
