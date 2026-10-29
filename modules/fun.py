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
from core.targets import replied_message
from core.text import esc, user_link

ABOUT = """
Nothing here moderates anything. These are for when the chat needs a break.

/pat, /slap, /hug and /roast are aimed at whoever you reply to, or at a name you type after the command. Without either, you are the target.

/ud shows what Urban Dictionary says, and that can be crude.
"""

HELP = [
    ("/pat [user]", "Pat someone: reply to them or type a name."),
    ("/slap [user]", "Slap someone with something unreasonable."),
    ("/hug [user]", "Hug someone."),
    ("/roast [user]", "Roast someone, gently and without touching anything personal."),
    ("/decide [question]", "Let me decide for you: yes, no or maybe."),
    ("/flip [text]", "Flip a coin. Give some text and I flip that instead, table and all."),
    ("/roll [NdM]", "Roll dice: N dice with M sides each, like 2d6. Default is one d6."),
    ("/rate <thing>", "Rate anything from 0 to 10. The same thing always gets the same grade."),
    ("/ud <term>", "Look a term up on Urban Dictionary."),
]

HELP_NOTES = [
    (
        "Examples",
        """
        /slap - slaps whoever you reply to.
        /roll 3d20 - three twenty-sided dice.
        /flip hello - hello, flipped.
        /rate pineapple pizza - a verdict.
        """,
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
        return f"<b>{esc(sender_chat.title or 'Someone')}</b>"
    return user_link(user.id, user.full_name) if user else "<b>Someone</b>"


def _target(message: Message, command: CommandObject) -> tuple[str, bool]:
    """
    (who it is aimed at, whether that is the bot itself): the person replied to, else
    the name typed after the command, else the sender.
    """
    replied = replied_message(message)
    if replied is not None:
        if getattr(replied, "sender_chat", None) is not None:
            return f"<b>{esc(replied.sender_chat.title or 'someone')}</b>", False
        if replied.from_user is not None:
            person = replied.from_user
            return user_link(person.id, person.full_name), person.id == message.bot.id
    name = " ".join((command.args or "").split())[:MAX_NAME]
    if name:
        return f"<b>{esc(name)}</b>", name.lstrip("@").lower() in {"argo", "argorobot"}
    return _actor(message), False


# --- Pat, slap, hug, roast ---------------------------------------------------------------

PATS = [
    "{a} pats {t} on the head. Good human. 🐾",
    "{a} gives {t} a gentle pat. {t} looks smug about it.",
    "{a} pats {t} twice. That's the maximum allowed by the rules.",
    "{a} pats {t}. It was weirdly wholesome.",
]
SLAP_ITEMS = [
    "a large trout", "a rolled-up newspaper", "a suspiciously damp sock", "a printed copy of the chat rules",
    "a rubber duck", "a baguette", "an outdated spreadsheet", "a pool noodle", "a very disappointed look",
]
SLAPS = [
    "{a} slaps {t} with {i}.",
    "{a} hits {t} with {i}. It was not very effective. 💥",
    "{a} whacks {t} with {i} and walks away like nothing happened.",
]
HUGS = [
    "{a} hugs {t}. Aww. 🫂",
    "{a} gives {t} a big hug. Nobody asked, nobody minded.",
    "{a} hugs {t} for exactly three seconds. Any longer is a commitment.",
]
ROASTS = [
    "{t} has the energy of a loading bar stuck at 99%.",
    "{t} is proof that confidence and competence are two different things.",
    "{t} types like the keyboard owes them money.",
    "If {t} were a spice, they'd be unsalted flour.",
    "{t} brings the same energy to every chat: it arrives late and leaves early.",
    "{t} sends voice messages the way other people send threats.",
    "{t} reads the rules the way cats read instruction manuals.",
    "{t} is the human version of a link that opens in the wrong app.",
    "{t} argues like a browser tab: loud, and nobody knows where it's coming from.",
    "{t} has main-character energy and side-quest results.",
]
BOT_REPLIES = [
    "Nice try. I don't do that to myself. 😏",
    "Bold of you to aim that at the one who can mute you.",
    "I'm the bot here. I do that to you, not the other way round.",
]


async def _action(message: Message, command: CommandObject, templates: list[str]) -> None:
    target, is_bot = _target(message, command)
    if is_bot:
        await reply(message, random.choice(BOT_REPLIES))
        return
    text = random.choice(templates).format(a=_actor(message), t=target, i=random.choice(SLAP_ITEMS))
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
    "Yes": ["Yes. Do it.", "Yes. Obviously.", "Yes, and I'd like to be there when it happens."],
    "No": ["No.", "Absolutely not.", "No. I've seen how this ends."],
    "Maybe": ["Maybe. Ask me when I care.", "Maybe. The universe is buffering.", "Maybe. Flip a coin, I'm busy."],
}


@router.message(cmd("decide"))
async def cmd_decide(message: Message, command: CommandObject) -> None:
    await reply(message, random.choice(random.choice(list(ANSWERS.values()))))


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
        await reply(message, f"<b>{random.choice(['Heads', 'Tails'])}</b>")
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
        await reply(message, f"Use /roll NdM, like /roll 2d6. Up to {MAX_DICE} dice with 2 to {MAX_SIDES} sides.")
        return
    rolls = [random.randint(1, sides) for _ in range(count)]
    lines = [f"<b>Roll {count}d{sides}</b>", f"• <b>Rolls:</b> <code>{', '.join(map(str, rolls))}</code>"]
    if count > 1:
        lines.append(f"• <b>Total:</b> <code>{sum(rolls)}</code>")
    await reply(message, "\n".join(lines))


VERDICTS = {
    range(0, 2): "A crime against taste.",
    range(2, 4): "I've seen better. I've seen worse. Mostly better.",
    range(4, 6): "Mediocre. Aggressively average.",
    range(6, 8): "Respectable. I'd allow it.",
    range(8, 10): "Excellent. I'm almost impressed.",
    range(10, 11): "Perfection. Don't let it go to your head.",
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
        await reply(message, "Use /rate &lt;thing&gt;, or reply to a person.")
        return
    grade = rate(thing)
    verdict = next(text for span, text in VERDICTS.items() if grade in span)
    await reply(message, f"<b>{esc(thing)}</b>: <code>{grade}/10</code>\n{verdict}")


# --- Urban Dictionary --------------------------------------------------------------------

@router.message(cmd("ud"))
async def cmd_ud(message: Message, command: CommandObject) -> None:
    term = " ".join((command.args or "").split())
    if not term:
        await reply(message, "Use /ud &lt;term&gt;.")
        return
    if len(term) > 80:
        await reply(message, "That is too long for a term.")
        return
    user = message.from_user
    if user is not None:
        now = time.monotonic()
        if now - _last_ud.get(user.id, 0.0) < UD_COOLDOWN:
            await reply(message, "Slow down a little and try again in a few seconds.")
            return
        if len(_last_ud) >= 5000:
            _last_ud.clear()
        _last_ud[user.id] = now
    try:
        text = await lookup.urban(term)
    except web.WebError as exc:
        text = str(exc)
    await reply(message, text, link_preview_options=NO_PREVIEW)
