"""Lighter commands to break up moderation duty"""

import hashlib
import random
import re
import time

from aiogram import Router
from aiogram.filters import CommandObject
from aiogram.types import LinkPreviewOptions, Message

from core import lookup, web
from core.filters import cmd
from core.helpers import reply
from core.targets import TargetError, replied_message, resolve_target
from core.text import esc, user_link
from core.i18n import tr

ABOUT = "fun.about"

HELP = [
    ("/pat [user]", "fun.help.pat_someone_reply_to_them"),
    ("/slap [user]", "fun.help.slap_someone_with_something_unreasonable"),
    ("/hug [user]", "fun.help.hug_someone"),
    ("/roast [user]", "fun.help.roast_someone_gently_and_without"),
    ("/decide [question]", "fun.help.let_me_decide_for_you"),
    ("/flip [text]", "fun.help.flip_a_coin_give_some"),
    ("/roll [NdM]", "fun.help.roll_dice_n_dice_with"),
    ("/rate <thing>", "fun.help.rate_anything_from_0_to"),
    ("/ud <term>", "fun.help.look_a_term_up_on"),
]

HELP_NOTES = [
    (
        "common.examples",
        "fun.note.slap_slaps_whoever_you_reply",
    ),
]

router = Router(name="fun")

NO_PREVIEW = LinkPreviewOptions(is_disabled=True)
MAX_NAME = 50
UD_COOLDOWN = 3.0
MAX_DICE = 20
MAX_SIDES = 1000
MAX_FLIP = 100
_last_ud: dict[int, float] = {}


# --- Who is it about ---------------------------------------------------------------

def _actor(message: Message) -> str:
    user = message.from_user
    sender_chat = getattr(message, "sender_chat", None)
    if sender_chat is not None:
        return f"<b>{esc(sender_chat.title or tr("fun.someone"))}</b>"
    return user_link(user.id, user.full_name) if user else tr("fun.someone_2")


async def _target(message: Message, command: CommandObject) -> tuple[str, bool]:
    """
    (who it is aimed at, whether that is the bot itself): a real user, given as a
    mention, @username, id or by replying; the sender when nothing is given.
    Raises TargetError when what was typed is not a known user.
    """
    replied = replied_message(message)
    if replied is None and not (command.args or "").strip():
        return _actor(message), False
    if replied is not None and getattr(replied, "sender_chat", None) is not None and replied.from_user is None:
        return f"<b>{esc(replied.sender_chat.title or 'someone')}</b>", False
    target = await resolve_target(message, command, allow_bots=True, allow_chats=True)
    if target.is_chat:
        return f"<b>{esc(target.title or target.name or 'someone')}</b>", False
    name = target.name or target.first_name or (f"@{target.username}" if target.username else str(target.user_id))
    return user_link(target.user_id, name), target.user_id == message.bot.id


# --- Pat, slap, hug, roast ---------------------------------------------------------------

PATS = [
    "fun.pats.00",
    "fun.pats.01",
    "fun.pats.02",
    "fun.pats.03",
    "fun.pats.04",
    "fun.pats.05",
    "fun.pats.06",
    "fun.pats.07",
    "fun.pats.08",
    "fun.pats.09",
    "fun.pats.10",
    "fun.pats.11",
    "fun.pats.12",
    "fun.pats.13",
    "fun.pats.14",
    "fun.pats.15",
    "fun.pats.16",
    "fun.pats.17",
    "fun.pats.18",
    "fun.pats.19",
    "fun.pats.20",
    "fun.pats.21",
    "fun.pats.22",
    "fun.pats.23",
    "fun.pats.24",
    "fun.pats.25",
]
SLAP_ITEMS = [
    "fun.slap_items.00", "fun.slap_items.01", "fun.slap_items.02", "fun.slap_items.03",
    "fun.slap_items.04", "fun.slap_items.05", "fun.slap_items.06", "fun.slap_items.07", "fun.slap_items.08",
    "fun.slap_items.09", "fun.slap_items.10", "fun.slap_items.11", "fun.slap_items.12",
    "fun.slap_items.13", "fun.slap_items.14", "fun.slap_items.15", "fun.slap_items.16", "fun.slap_items.17",
    "fun.slap_items.18", "fun.slap_items.19", "fun.slap_items.20", "fun.slap_items.21", "fun.slap_items.22",
    "fun.slap_items.23", "fun.slap_items.24", "fun.slap_items.25",
    "fun.slap_items.26",
    "fun.slap_items.27",
    "fun.slap_items.28",
    "fun.slap_items.29",
    "fun.slap_items.30",
    "fun.slap_items.31",
    "fun.slap_items.32",
    "fun.slap_items.33",
    "fun.slap_items.34",
    "fun.slap_items.35",
    "fun.slap_items.36",
    "fun.slap_items.37",
    "fun.slap_items.38",
    "fun.slap_items.39",
    "fun.slap_items.40",
    "fun.slap_items.41",
    "fun.slap_items.42",
    "fun.slap_items.43",
    "fun.slap_items.44",
    "fun.slap_items.45",
    "fun.slap_items.46",
    "fun.slap_items.47",
    "fun.slap_items.48",
    "fun.slap_items.49",
    "fun.slap_items.50",
]
SLAPS = [
    "fun.slaps.00",
    "fun.slaps.01",
    "fun.slaps.02",
    "fun.slaps.03",
    "fun.slaps.04",
    "fun.slaps.05",
    "fun.slaps.06",
    "fun.slaps.07",
    "fun.slaps.08",
    "fun.slaps.09",
    "fun.slaps.10",
    "fun.slaps.11",
    "fun.slaps.12",
    "fun.slaps.13",
    "fun.slaps.14",
    "fun.slaps.15",
    "fun.slaps.16",
    "fun.slaps.17",
    "fun.slaps.18",
    "fun.slaps.19",
    "fun.slaps.20",
    "fun.slaps.21",
    "fun.slaps.22",
    "fun.slaps.23",
    "fun.slaps.24",
    "fun.slaps.25",
]
HUGS = [
    "fun.hugs.00",
    "fun.hugs.01",
    "fun.hugs.02",
    "fun.hugs.03",
    "fun.hugs.04",
    "fun.hugs.05",
    "fun.hugs.06",
    "fun.hugs.07",
    "fun.hugs.08",
    "fun.hugs.09",
    "fun.hugs.10",
    "fun.hugs.11",
    "fun.hugs.12",
    "fun.hugs.13",
    "fun.hugs.14",
    "fun.hugs.15",
    "fun.hugs.16",
    "fun.hugs.17",
    "fun.hugs.18",
    "fun.hugs.19",
    "fun.hugs.20",
    "fun.hugs.21",
    "fun.hugs.22",
    "fun.hugs.23",
    "fun.hugs.24",
    "fun.hugs.25",
]
ROASTS = [
    "fun.roasts.00",
    "fun.roasts.01",
    "fun.roasts.02",
    "fun.roasts.03",
    "fun.roasts.04",
    "fun.roasts.05",
    "fun.roasts.06",
    "fun.roasts.07",
    "fun.roasts.08",
    "fun.roasts.09",
    "fun.roasts.10",
    "fun.roasts.11",
    "fun.roasts.12",
    "fun.roasts.13",
    "fun.roasts.14",
    "fun.roasts.15",
    "fun.roasts.16",
    "fun.roasts.17",
    "fun.roasts.18",
    "fun.roasts.19",
    "fun.roasts.20",
    "fun.roasts.21",
    "fun.roasts.22",
    "fun.roasts.23",
    "fun.roasts.24",
    "fun.roasts.25",
    "fun.roasts.26",
    "fun.roasts.27",
    "fun.roasts.28",
    "fun.roasts.29",
    "fun.roasts.30",
    "fun.roasts.31",
    "fun.roasts.32",
    "fun.roasts.33",
    "fun.roasts.34",
    "fun.roasts.35",
    "fun.roasts.36",
    "fun.roasts.37",
    "fun.roasts.38",
    "fun.roasts.39",
    "fun.roasts.40",
    "fun.roasts.41",
    "fun.roasts.42",
    "fun.roasts.43",
    "fun.roasts.44",
    "fun.roasts.45",
    "fun.roasts.46",
    "fun.roasts.47",
    "fun.roasts.48",
    "fun.roasts.49",
]
BOT_REPLIES = [
    "fun.bot_replies.00",
    "fun.bot_replies.01",
    "fun.bot_replies.02",
    "fun.bot_replies.03",
    "fun.bot_replies.04",
    "fun.bot_replies.05",
    "fun.bot_replies.06",
    "fun.bot_replies.07",
    "fun.bot_replies.08",
    "fun.bot_replies.09",
    "fun.bot_replies.10",
]


async def _action(message: Message, command: CommandObject, templates: list[str]) -> None:
    try:
        target, is_bot = await _target(message, command)
    except TargetError as exc:
        await reply(message, esc(exc))
        return
    if is_bot:
        await reply(message, tr(random.choice(BOT_REPLIES)))
        return
    text = tr(random.choice(templates), a=_actor(message), t=target, i=tr(random.choice(SLAP_ITEMS)))
    await reply(message, text, link_preview_options=NO_PREVIEW)


@router.message(cmd("pat"))
async def cmd_pat(message: Message, command: CommandObject) -> None:
    await _action(message, command, PATS)


@router.message(cmd("slap"))
async def cmd_slap(message: Message, command: CommandObject) -> None:
    await _action(message, command, SLAPS)


@router.message(cmd("hug"))
async def cmd_hug(message: Message, command: CommandObject) -> None:
    await _action(message, command, HUGS)


@router.message(cmd("roast"))
async def cmd_roast(message: Message, command: CommandObject) -> None:
    await _action(message, command, ROASTS)


# --- Decide, flip, roll, rate ------------------------------------------------------------

ANSWERS = {
    "Yes": [
        "fun.answers.yes.00", "fun.answers.yes.01", "fun.answers.yes.02",
        "fun.answers.yes.03", "fun.answers.yes.04", "fun.answers.yes.05",
        "fun.answers.yes.06", "fun.answers.yes.07",
    ],
    "No": [
        "fun.answers.no.08", "fun.answers.no.09", "fun.answers.no.10",
        "fun.answers.no.11", "fun.answers.no.12", "fun.answers.no.13",
        "fun.answers.no.14", "fun.answers.no.15",
    ],
    "Maybe": [
        "fun.answers.maybe.16", "fun.answers.maybe.17", "fun.answers.maybe.18",
        "fun.answers.maybe.19", "fun.answers.maybe.20", "fun.answers.maybe.21",
        "fun.answers.maybe.22", "fun.answers.maybe.23",
    ],
}


@router.message(cmd("decide"))
async def cmd_decide(message: Message, command: CommandObject) -> None:
    await reply(message, tr(random.choice(random.choice(list(ANSWERS.values())))))


_FLIPPED = dict(zip(
    "abcdefghijklmnopqrstuvwxyz0123456789.,!?'_()",
    "ɐqɔpǝɟƃɥᴉɾʞlɯuodbɹsʇnʌʍxʎz0ƖᄅƐㄣϛ9ㄥ86˙'¡¿,‾)(",
))


def flip_text(text: str) -> str:
    """Upside down and back to front, unknown characters stay as they are."""
    return "".join(_FLIPPED.get(c, _FLIPPED.get(c.lower(), c)) for c in reversed(text))


@router.message(cmd("flip"))
async def cmd_flip(message: Message, command: CommandObject) -> None:
    text = " ".join((command.args or "").split())
    if not text:
        await reply(message, f"<b>{random.choice([tr("fun.heads"), tr("fun.tails")])}</b>")
        return
    await reply(message, f"(╯°□°)╯︵ {esc(flip_text(text[:MAX_FLIP]))}")


_DICE = re.compile(r"^(\d{0,2})d(\d{1,4})$", re.IGNORECASE)


@router.message(cmd("roll"))
async def cmd_roll(message: Message, command: CommandObject) -> None:
    argument = (command.args or "").strip() or "1d6"
    match = _DICE.match(argument)
    count = int(match.group(1) or 1) if match else 0
    sides = int(match.group(2)) if match else 0
    if not match or not 1 <= count <= MAX_DICE or not 2 <= sides <= MAX_SIDES:
        await reply(message, tr("fun.use_roll_ndm_like_roll_2d6", MAX_DICE=MAX_DICE, MAX_SIDES=MAX_SIDES))
        return
    rolls = [random.randint(1, sides) for _ in range(count)]
    lines = [tr("fun.roll_d", count=count, sides=sides), tr("fun.rolls", join=', '.join(map(str, rolls)))]
    if count > 1:
        lines.append(f"• <b>Total:</b> <code>{sum(rolls)}</code>")
    await reply(message, "\n".join(lines))


VERDICTS = {
    range(0, 2): "fun.verdicts.00",
    range(2, 4): "fun.verdicts.01",
    range(4, 6): "fun.verdicts.02",
    range(6, 8): "fun.verdicts.03",
    range(8, 10): "fun.verdicts.04",
    range(10, 11): "fun.verdicts.05",
}


def rate(thing: str) -> int:
    """A grade from 0 to 10 that only depends on the words, so it never changes its mind."""
    digest = hashlib.sha256(" ".join(thing.lower().split()).encode()).digest()
    return digest[0] % 11


@router.message(cmd("rate"))
async def cmd_rate(message: Message, command: CommandObject) -> None:
    thing = " ".join((command.args or "").split())[:MAX_NAME]
    replied = replied_message(message)
    if not thing and replied is not None and replied.from_user is not None:
        thing = replied.from_user.full_name
    if not thing:
        await reply(message, tr("fun.use_rate_lt_thing_gt_or"))
        return
    grade = rate(thing)
    verdict = next(text for span, text in VERDICTS.items() if grade in span)
    await reply(message, f"<b>{esc(thing)}</b>: <code>{grade}/10</code>\n{tr(verdict)}")


# --- Urban Dictionary --------------------------------------------------------------------

@router.message(cmd("ud"))
async def cmd_ud(message: Message, command: CommandObject) -> None:
    term = " ".join((command.args or "").split())
    if not term:
        await reply(message, tr("fun.use_ud_lt_term_gt"))
        return
    if len(term) > 80:
        await reply(message, tr("fun.that_is_too_long_for_a"))
        return
    user = message.from_user
    if user is not None:
        now = time.monotonic()
        if now - _last_ud.get(user.id, 0.0) < UD_COOLDOWN:
            await reply(message, tr("common.slow_down_a_little_and_try"))
            return
        if len(_last_ud) >= 5000:
            _last_ud.clear()
        _last_ud[user.id] = now
    try:
        text = await lookup.urban(term)
    except web.WebError as exc:
        text = str(exc)
    await reply(message, text, link_preview_options=NO_PREVIEW)
