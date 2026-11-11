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

ABOUT = """
Nothing here moderates anything. These are for when the chat needs a break.

/pat, /slap, /hug and /roast are aimed at whoever you reply to, or at a user you mention after the command: @username, a mention or an id. Without either, you are the target.

/ud shows what Urban Dictionary says, and that can be crude.
"""

HELP = [
    ("/pat [user]", "Pat someone: reply to them or give @username, a mention or an id."),
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
    "{a} pats {t} on the head. Good human. 🐾",
    "{a} gives {t} a gentle pat. {t} looks smug about it.",
    "{a} pats {t} twice. That's the maximum allowed by the rules.",
    "{a} pats {t}. It was weirdly wholesome.",
    "{a} pats {t} on the shoulder. Everything is going to be fine. Probably.",
    "{a} gives {t} a pat on the head. {t} did not ask for this, but accepts it.",
    "{a} pats {t} like a very good boy. Or girl. Or something in between. Good.",
    "{a} pats {t} softly. The chat's mood improved by 3%.",
    "{a} pats {t} on the head and whispers: you're doing fine.",
    "{a} pats {t}. A single, dignified pat. No further comment.",
    "{a} reaches out and pats {t}. {t} purrs. Nobody saw that.",
    "{a} pats {t} on the back. Well done for existing today. 🐾",
    "{a} pats {t} on the head with a mildly suspicious amount of affection.",
    "{a} gives {t} a pat and a look that says: proud of you.",
    "{a} pats {t} gently. {t} forgets what they were arguing about.",
    "{a} pats {t} on the head. Tiny gesture, huge morale boost. 🐾",
    "{a} walks up and pats {t}. No explanation given.",
    "{a} pats {t} three times. That's a blessing, not a bug.",
    "{a} pats {t}. {t} was about to rage-quit and now isn't.",
    "{a} pats {t} on the head like a well-behaved cloud.",
    "{a} pats {t} and quietly says: you did your best.",
    "{a} gives {t} a pat. The chat nods in approval.",
    "{a} pats {t} so gently that nobody is sure it happened.",
    "{a} pats {t}. Stats updated: wholesomeness +1.",
    "{a} pats {t} on the head and hands over an imaginary cookie. 🍪",
    "{a} pats {t} like a seasoned professional.",
]
SLAP_ITEMS = [
    "a large trout", "a rolled-up newspaper", "a suspiciously damp sock", "a printed copy of the chat rules",
    "a rubber duck", "a baguette", "an outdated spreadsheet", "a pool noodle", "a very disappointed look",
    "a frozen pizza", "a laminated meeting agenda", "a half-eaten sandwich", "a USB cable that never fits the first time",
    "a bag of unread notifications", "a crumpled receipt", "a wet towel", "a cactus (gently)", "a stale croissant",
    "a printed terms of service", "a slipper", "a keyboard with sticky keys", "a feather pillow", "a plush dinosaur",
    "a very long email", "a copy of the user manual nobody reads", "a bag of frozen peas",
    "a soggy pancake",
    "a tennis racket with one string",
    "a stack of unpaid invoices",
    "a bag of old charging cables",
    "a fluffy slipper",
    "a stale baguette from last Tuesday",
    "a rolled-up poster of a cat",
    "a sleeping hamster (do not worry, it stayed asleep)",
    "a clipboard",
    "a long scarf",
    "a loaf of toast",
    "a bunch of ethernet cables",
    "a tiny trout in a bag",
    "a very heavy dictionary",
    "a pair of oven mitts",
    "a wet noodle",
    "a printed screenshot of the chat",
    "a wooden spoon",
    "a sack of potatoes",
    "a foam sword",
    "a back-to-school binder",
    "a sticky note that says 'be nice'",
    "an inflatable banana",
    "a plastic shovel",
    "a rubber chicken",
]
SLAPS = [
    "{a} slaps {t} with {i}.",
    "{a} hits {t} with {i}. It was not very effective. 💥",
    "{a} whacks {t} with {i} and walks away like nothing happened.",
    "{a} slaps {t} across the chat with {i}. The admins pretend not to see.",
    "{a} grabs {i} and slaps {t}. Dramatic. Necessary.",
    "{a} slaps {t} with {i}. {t} blinks twice and reconsiders everything.",
    "{a} dramatically slaps {t} with {i}. The crowd goes quiet. 💥",
    "{a} swings {i} at {t}. Direct hit. Critical damage to the ego.",
    "{a} slaps {t} with {i}, then offers a very insincere apology.",
    "{a} smacks {t} with {i}. This was not in the rules, but here we are.",
    "With {i}, {a} delivers a slap to {t}. The audience claps politely.",
    "{a} slaps {t} with {i}. {t} lost 10 reputation points and a bit of dignity.",
    "{a} slaps {t} with {i}. {t} saw it coming and still didn't dodge.",
    "{a} winds up, swings {i}, and slaps {t}. Textbook technique.",
    "{a} slaps {t} with {i}, then bows to the audience.",
    "{a} hits {t} with {i}. The sound effect was unnecessarily loud. 💥",
    "{a} slaps {t} with {i} in slow motion. Cinematic.",
    "{a} slaps {t} with {i}. The group admin quietly takes notes.",
    "{a} produces {i} from nowhere and slaps {t}. Nobody saw where it came from.",
    "{a} slaps {t} with {i}. {t} is now legally allowed to complain.",
    "{a} slaps {t} with {i} and says: that was for the last message.",
    "{a} swings {i} at {t}. It connects. The chat gasps.",
    "{a} slaps {t} with {i}. Confidence: 100%. Reason: unclear.",
    "{a} delivers a slap to {t} using {i}. Rated PG-13.",
    "{a} slaps {t} with {i} and leaves a five-star review of the experience.",
    "{a} slaps {t} with {i}. {t} responds with a dramatic gasp.",
]
HUGS = [
    "{a} hugs {t}. Aww. 🫂",
    "{a} gives {t} a big hug. Nobody asked, nobody minded.",
    "{a} hugs {t} for exactly three seconds. Any longer is a commitment.",
    "{a} wraps {t} in a warm hug. The chat gets a little softer. 🫂",
    "{a} hugs {t} so hard that the chat rules are momentarily forgotten.",
    "{a} gives {t} a bear hug. {t} is not sure who the bear is here.",
    "{a} hugs {t} and refuses to let go. Somebody call an admin. Or don't.",
    "{a} sneaks up and hugs {t}. {t} pretends to be surprised.",
    "{a} gives {t} a tight hug. Free of charge, no subscription needed.",
    "{a} hugs {t}. {t} awkwardly pats back. It counts. 🫂",
    "{a} gives {t} a long, quiet hug. Sometimes that is all it takes.",
    "{a} hugs {t} like it's the last day of the holidays.",
    "{a} hugs {t} and holds on for a solid five seconds. Respect.",
    "{a} gives {t} a warm hug. The chat is better for it. 🫂",
    "{a} hugs {t}. {t} smiles in a way that's hard to hide.",
    "{a} pulls {t} into a hug. No questions asked.",
    "{a} gives {t} the kind of hug that fixes a rough day.",
    "{a} hugs {t}. The group temperature rises by two degrees.",
    "{a} hugs {t} like they haven't met in years.",
    "{a} offers {t} a hug. {t} accepts with dignity.",
    "{a} hugs {t} and adds a gentle back pat for style.",
    "{a} runs over and hugs {t}. Somebody stop them. Or don't.",
    "{a} gives {t} a squeeze. Soft, warm, completely unprompted.",
    "{a} wraps {t} in a blanket-style hug. Cozy mode on. 🫂",
    "{a} hugs {t}. A tiny moment of peace in a noisy chat.",
    "{a} gives {t} a group-sized hug, solo edition.",
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
    "{t} is the reason the mute button exists. It's a compliment. Sort of.",
    "{t} has a battery life of 4% and still joins every conversation.",
    "{t} gives advice like a GPS recalculating: confident, and wrong.",
    "{t} is the kind of person who replies 'ok' to a paragraph of thoughts.",
    "{t} says 'one last thing' and then writes for another twenty minutes.",
    "{t} treats the pin button like a trophy case.",
    "{t} runs on caffeine, spite and about five unread messages.",
    "{t} has the confidence of a person who never scrolled up.",
    "{t} is like a software update: always at the wrong moment and nobody asked.",
    "{t} gets lost in a group chat with three people.",
    "{t} has opinions about everything and sources for nothing.",
    "{t} is the human equivalent of 'seen at 3:07'.",
    "{t} reads the room the way a printer reads a PDF: with great effort and no result.",
    "{t} is 80% typing indicator and 20% message.",
    "{t} writes 'brb' and returns the following week.",
    "{t} has a great future behind them.",
    "{t} is the type to say 'quick question' and then open a seminar.",
    "{t} has the same reaction time as a government website.",
    "{t} treats every chat like a stage and every message like an encore.",
    "{t} is a walking 'this meeting could have been an email'.",
    "{t} has strong Wi-Fi in the argument and weak signal in the facts.",
    "{t} brings snacks to a debate and leaves with a lesson.",
    "{t} replies to everything with a thumbs-up and a hidden sigh.",
    "{t} would lose a staring contest to a screensaver.",
    "{t} is like a loading spinner: busy, and I'm not sure for what.",
    "{t} reads the first line and replies to the fourth.",
    "{t} treats 'mute' as a personal challenge.",
    "{t} brings the energy of a Monday morning to a Friday night.",
    "{t} reacts to the chat with all the speed of a snail on holiday.",
    "{t} is the human version of a fake 'download now' button.",
    "{t} has more unread messages than friends.",
    "{t} is the reason the 'delete for everyone' button exists.",
    "{t} explains jokes the way instruction manuals explain nothing.",
    "{t} has the sleep schedule of a nocturnal bug and the opinions of a daytime talk show.",
    "{t} is like a keyboard shortcut nobody remembers: powerful in theory, useless in practice.",
    "{t} sends 'hello' and then goes offline for the weekend.",
    "{t} takes screenshots of conversations and never opens them again.",
    "{t} calls it 'a quick fix' and it takes three weeks.",
    "{t} is cheerful enough to be annoying and annoying enough to be missed.",
    "{t} still uses the word 'actually' like a weapon.",
]
BOT_REPLIES = [
    "Nice try. I don't do that to myself. 😏",
    "Bold of you to aim that at the one who can mute you.",
    "I'm the bot here. I do that to you, not the other way round.",
    "Aiming that at me? Adorable. And mildly illegal in this chat.",
    "I'm a bot. I don't feel it. But I do remember it. 😌",
    "Nope. Try that on someone who can't ban you.",
    "Self-targeting detected. Request denied.",
    "You want me to do that to myself? I'll pass, thanks.",
    "Cute. Ask me again and I'll start counting warnings.",
    "I have thick circuits. Try someone else.",
    "Nice attempt. I'm filing it under 'entertainment'.",
]


async def _action(message: Message, command: CommandObject, templates: list[str]) -> None:
    try:
        target, is_bot = await _target(message, command)
    except TargetError as exc:
        await reply(message, esc(exc))
        return
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
    "Yes": [
        "Yes. Do it.", "Yes. Obviously.", "Yes, and I'd like to be there when it happens.",
        "Yes. The stars are mildly impressed.", "Yes. Go before you overthink it.", "Definitely yes.",
        "Yes. I'd bet my logs on it.", "Yes. What's the worst that could happen?",
    ],
    "No": [
        "No.", "Absolutely not.", "No. I've seen how this ends.",
        "No. Not even once.", "Hard no.", "No. Your future self says thanks.",
        "No. I checked twice, then once more.", "Nope. Next question.",
    ],
    "Maybe": [
        "Maybe. Ask me when I care.", "Maybe. The universe is buffering.", "Maybe. Flip a coin, I'm busy.",
        "Maybe. I'd sleep on it.", "Maybe. The odds are a bit foggy.", "Could be. Could also not be.",
        "Maybe. Ask again after coffee.", "Unclear. Even my circuits shrug.",
    ],
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
