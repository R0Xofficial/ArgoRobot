"""
Welcome and goodbye messages for the bot's staff, written here (modules/greetings.py sends them).

Somebody with a role (Support, Sudo, Developer, Owner) gets the message of their role instead of the
chat's own welcome or goodbye, in every chat, and is never asked for a captcha. The texts are written
like the chat's own: HTML formatting and fillings such as {mention}, {first} and {chatname}.
"""

from core.roles import Role

WELCOME: dict[Role, str] = {
    Role.SUPPORT: "🛟 Support member {mention} has arrived in {chatname}. Help is on the way.",
    Role.SUDO: "🛡 Sudo user {mention} has joined {chatname}. Behave, everyone.",
    Role.DEVELOPER: "🛠 Developer {mention} has entered {chatname}. The bot is on its best behaviour.",
    Role.OWNER: "👑 My owner {mention} has joined {chatname}. Everybody stand up.",
}
GOODBYE: dict[Role, str] = {
    Role.SUPPORT: "🛟 Support member {first} has left. Hope it was a good shift.",
    Role.SUDO: "🛡 Sudo user {first} has left the chat. The chat is a bit less safe.",
    Role.DEVELOPER: "🛠 Developer {first} has left. Please don't break anything while away.",
    Role.OWNER: "👑 My owner {first} has left. Until next time.",
}


def get(role: Role, kind: str) -> str:
    """The text for "welcome" or "goodbye" of a role."""
    return (WELCOME if kind == "welcome" else GOODBYE)[role]
