"""
Immunity: accounts the bot must never take any action against.

The ids are written in by hand: Telegram's own accounts. Banning, muting or
warning them makes no sense.

The bot's own staff (Owner, Developers, Sudo, Support) is not immune: the admins
of a group decide who stays in their group. Staff are only kept from acting
against each other (see core/moderation.py).

Modules that act against a user (bans, mutes, warns, global bans ...) ask
`is_immune()` first. Immunity is fixed in the code, unlike the whitelist
(core/whitelist.py), which people with the right role can change.

Writing as a channel
--------------------
When someone writes as a channel, Telegram shows the message as sent by a
placeholder account (PLACEHOLDER_SENDERS) and puts the real sender in
message.sender_chat. Immunity protects only that placeholder account. It never
covers the channel behind the message, and a channel id is never immune:
scammers and impersonators write as channels too, so a moderation command aimed
at such a message has to act on `sender_chat` (resolve_target with
allow_chats=True returns it as the target). The one exception is an anonymous
admin: there sender_chat is the group itself, and the person behind it is hidden.
"""


# id -> what it is
SERVICE_ACCOUNTS: dict[int, str] = {
    777000: "Telegram",
    1087968824: "GroupAnonymousBot",
    136817688: "Channel_Bot",
    93372553: "BotFather",
}

# The accounts Telegram shows as the sender of messages written on behalf of a
# chat. The real sender is in message.sender_chat; these are never recorded as users.
PLACEHOLDER_SENDERS: frozenset[int] = frozenset({777000, 1087968824, 136817688})


def is_service_account(user_id: int) -> bool:
    return user_id in SERVICE_ACCOUNTS


def is_immune(user_id: int) -> bool:
    """Whether no action may ever be taken against this id (channels and groups never are)."""
    return user_id in SERVICE_ACCOUNTS
