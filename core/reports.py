"""
Report alerts: which admins want to be told about reports in their private chat with
the bot (modules/reporting.py). Everyone does until they turn it off, so only the
opt-outs are stored.
"""

from collections.abc import Iterable

from core import database


async def opted_out(user_ids: Iterable[int]) -> set[int]:
    """Those of `user_ids` who turned alerts off."""
    ids = list(user_ids)
    if not ids:
        return set()
    rows = await database.fetch_all("SELECT user_id FROM report_optout WHERE user_id = ANY($1::bigint[])", ids)
    return {row["user_id"] for row in rows}


async def alerts_on(user_id: int) -> bool:
    return user_id not in await opted_out([user_id])


async def set_alerts(user_id: int, enabled: bool) -> None:
    if enabled:
        await database.execute("DELETE FROM report_optout WHERE user_id = $1", user_id)
    else:
        await database.execute("INSERT INTO report_optout (user_id) VALUES ($1) ON CONFLICT DO NOTHING", user_id)
