"""
The lookups behind /tr, /dict, /time, /weather and /cash.

Each function asks a free public service (no API key) and returns the finished
HTML text of the answer. Problems raise web.WebError (or web.NotFound) with a
sentence meant for people. The network goes through web.get_json only, which is
what the tests replace.
"""

import math
from datetime import datetime
from functools import lru_cache
from urllib.parse import quote
from zoneinfo import ZoneInfo, available_timezones

from core import web
from core.text import esc

MAX_TRANSLATE = 1500

# code -> (name, code Google expects). Codes are what people type after /tr.
LANGUAGES = {
    "af": "Afrikaans", "sq": "Albanian", "ar": "Arabic", "hy": "Armenian", "az": "Azerbaijani",
    "eu": "Basque", "be": "Belarusian", "bn": "Bengali", "bs": "Bosnian", "bg": "Bulgarian",
    "ca": "Catalan", "zh": "Chinese", "zh-cn": "Chinese (Simplified)", "zh-tw": "Chinese (Traditional)",
    "hr": "Croatian", "cs": "Czech", "da": "Danish", "nl": "Dutch", "en": "English",
    "eo": "Esperanto", "et": "Estonian", "fi": "Finnish", "fr": "French", "gl": "Galician",
    "ka": "Georgian", "de": "German", "el": "Greek", "gu": "Gujarati", "ht": "Haitian Creole",
    "he": "Hebrew", "hi": "Hindi", "hu": "Hungarian", "is": "Icelandic", "id": "Indonesian",
    "ga": "Irish", "it": "Italian", "ja": "Japanese", "kn": "Kannada", "kk": "Kazakh",
    "ko": "Korean", "la": "Latin", "lv": "Latvian", "lt": "Lithuanian", "mk": "Macedonian",
    "ms": "Malay", "mt": "Maltese", "mr": "Marathi", "mn": "Mongolian", "ne": "Nepali",
    "no": "Norwegian", "fa": "Persian", "pl": "Polish", "pt": "Portuguese", "pa": "Punjabi",
    "ro": "Romanian", "ru": "Russian", "sr": "Serbian", "sk": "Slovak", "sl": "Slovenian",
    "es": "Spanish", "sw": "Swahili", "sv": "Swedish", "tl": "Filipino", "ta": "Tamil",
    "te": "Telugu", "th": "Thai", "tr": "Turkish", "uk": "Ukrainian", "ur": "Urdu",
    "uz": "Uzbek", "vi": "Vietnamese", "cy": "Welsh", "yi": "Yiddish", "zu": "Zulu",
}
_GOOGLE_CODES = {"zh-cn": "zh-CN", "zh-tw": "zh-TW", "he": "iw"}


def language_name(code: str) -> str:
    code = code.lower()
    return LANGUAGES.get(code) or LANGUAGES.get(code.split("-")[0]) or code


def is_language(word: str) -> bool:
    return word.lower() in LANGUAGES


# --- /tr ------------------------------------------------------------------------

async def translate(text: str, target: str = "en") -> str:
    text = text.strip()
    data = await web.get_json(
        "https://translate.googleapis.com/translate_a/single",
        {"client": "gtx", "sl": "auto", "tl": _GOOGLE_CODES.get(target.lower(), target), "dt": "t", "q": text[:MAX_TRANSLATE]},
    )
    try:
        if not isinstance(data, list) or not isinstance(data[0], list):
            raise TypeError
        translated = "".join(part[0] for part in data[0] if isinstance(part, list) and part and part[0])
        source = data[2] if len(data) > 2 and isinstance(data[2], str) else "auto"
    except (TypeError, IndexError, KeyError):
        raise web.WebError(web.UNAVAILABLE) from None
    if not translated:
        raise web.NotFound("I couldn't translate that.")
    source_name = "Unknown" if source == "auto" else language_name("he" if source == "iw" else source)
    return (
        "<b>Translation</b>\n"
        f"• <b>From:</b> <code>{esc(source_name)}</code>\n"
        f"• <b>To:</b> <code>{esc(language_name(target))}</code>\n\n"
        f"{esc(translated)}"
    )


# --- /dict ----------------------------------------------------------------------

MAX_MEANINGS = 3
MAX_DEFINITIONS = 2


async def dictionary(word: str) -> str:
    word = word.strip()
    try:
        data = await web.get_json(f"https://api.dictionaryapi.dev/api/v2/entries/en/{quote(word, safe='')}")
    except web.NotFound:
        raise web.NotFound("I couldn't find that word. I only know English words.") from None
    try:
        entry = data[0]
        lines = ["<b>Dictionary</b>", f"• <b>Word:</b> <code>{esc(entry.get('word') or word)}</code>"]
        phonetic = entry.get("phonetic") or next((p["text"] for p in entry.get("phonetics", []) if p.get("text")), None)
        if phonetic:
            lines.append(f"• <b>Pronunciation:</b> <code>{esc(phonetic)}</code>")
        for meaning in entry.get("meanings", [])[:MAX_MEANINGS]:
            lines += ["", f"<b>{esc(meaning.get('partOfSpeech') or 'other')}</b>"]
            for number, item in enumerate(meaning.get("definitions", [])[:MAX_DEFINITIONS]):
                lines.append(f"• {esc(item['definition'])}")
                if number == 0 and item.get("example"):
                    lines.append(f"<i>{esc(item['example'])}</i>")
    except (TypeError, IndexError, KeyError, AttributeError):
        raise web.WebError(web.UNAVAILABLE) from None
    return "\n".join(lines)


# --- places: /time and /weather -------------------------------------------------

async def _place(query: str) -> dict:
    """The best match for a place name, with its coordinates and timezone."""
    name, _, rest = query.partition(",")
    data = await web.get_json(
        "https://geocoding-api.open-meteo.com/v1/search",
        {"name": name.strip(), "count": 10, "language": "en", "format": "json"},
    )
    found = (data or {}).get("results") or [] if isinstance(data, dict) else []
    if not found:
        raise web.NotFound("I couldn't find that place.")
    rest = rest.strip().lower()
    if rest:
        for item in found:
            if rest in " ".join(str(item.get(k, "")) for k in ("country", "admin1", "country_code")).lower():
                return item
    return found[0]


def _place_title(place: dict) -> str:
    parts = [place.get("name"), place.get("admin1") if place.get("admin1") != place.get("name") else None, place.get("country")]
    return ", ".join(p for p in parts if p)


@lru_cache(maxsize=1)
def _zones() -> dict[str, str]:
    return {name.lower(): name for name in available_timezones()}


def _offset_text(moment: datetime) -> str:
    offset = moment.utcoffset()
    minutes = int(offset.total_seconds() // 60) if offset else 0
    sign = "+" if minutes >= 0 else "-"
    hours, minutes = divmod(abs(minutes), 60)
    return f"UTC{sign}{hours:02d}:{minutes:02d}"


async def time_in(query: str) -> str:
    zone = _zones().get(query.strip().replace(" ", "_").lower())
    if zone:
        title, zone_name = zone, zone
    else:
        place = await _place(query)
        title, zone_name = _place_title(place), place.get("timezone")
        if not zone_name:
            raise web.NotFound("I couldn't find the timezone of that place.")
    try:
        now = datetime.now(ZoneInfo(zone_name))
    except Exception:
        raise web.NotFound("I couldn't find the timezone of that place.") from None
    return (
        f"<b>Time in {esc(title)}</b>\n"
        f"• <b>Time:</b> <code>{now:%H:%M}</code>\n"
        f"• <b>Date:</b> <code>{now:%A, %d %B %Y}</code>\n"
        f"• <b>Timezone:</b> <code>{esc(zone_name)} ({_offset_text(now)})</code>"
    )


WEATHER_CODES = {
    0: "Clear sky", 1: "Mostly clear", 2: "Partly cloudy", 3: "Overcast", 45: "Fog", 48: "Rime fog",
    51: "Light drizzle", 53: "Drizzle", 55: "Heavy drizzle", 56: "Freezing drizzle", 57: "Heavy freezing drizzle",
    61: "Light rain", 63: "Rain", 65: "Heavy rain", 66: "Freezing rain", 67: "Heavy freezing rain",
    71: "Light snow", 73: "Snow", 75: "Heavy snow", 77: "Snow grains",
    80: "Light showers", 81: "Showers", 82: "Heavy showers", 85: "Snow showers", 86: "Heavy snow showers",
    95: "Thunderstorm", 96: "Thunderstorm with hail", 99: "Thunderstorm with heavy hail",
}
_COMPASS = ["N", "NE", "E", "SE", "S", "SW", "W", "NW"]


def _fahrenheit(celsius: float) -> float:
    return celsius * 9 / 5 + 32


async def weather(query: str) -> str:
    place = await _place(query)
    data = await web.get_json(
        "https://api.open-meteo.com/v1/forecast",
        {
            "latitude": place["latitude"],
            "longitude": place["longitude"],
            "current": "temperature_2m,apparent_temperature,relative_humidity_2m,precipitation,weather_code,wind_speed_10m,wind_direction_10m",
            "wind_speed_unit": "kmh",
            "timezone": "auto",
        },
    )
    try:
        now = data["current"]
        temp, feels = float(now["temperature_2m"]), float(now["apparent_temperature"])
        wind = float(now["wind_speed_10m"])
        direction = _COMPASS[int((float(now["wind_direction_10m"]) + 22.5) // 45) % 8]
        lines = [
            f"<b>Weather in {esc(_place_title(place))}</b>",
            f"• <b>Conditions:</b> <code>{WEATHER_CODES.get(int(now['weather_code']), 'Unknown')}</code>",
            f"• <b>Temperature:</b> <code>{temp:.1f}°C / {_fahrenheit(temp):.1f}°F</code>",
            f"• <b>Feels like:</b> <code>{feels:.1f}°C / {_fahrenheit(feels):.1f}°F</code>",
            f"• <b>Humidity:</b> <code>{float(now['relative_humidity_2m']):.0f}%</code>",
            f"• <b>Wind:</b> <code>{wind:.0f} km/h / {wind / 1.609344:.0f} mph from {direction}</code>",
            f"• <b>Precipitation:</b> <code>{float(now['precipitation']):.1f} mm</code>",
        ]
    except (TypeError, KeyError, ValueError):
        raise web.WebError(web.UNAVAILABLE) from None
    return "\n".join(lines)


# --- /cash ----------------------------------------------------------------------

MAX_AMOUNT = 1e15


def parse_amount(text: str) -> float | None:
    text = text.replace(",", ".") if text.count(",") == 1 and "." not in text else text.replace(",", "")
    try:
        value = float(text)
    except ValueError:
        return None
    return value if math.isfinite(value) and 0 < value <= MAX_AMOUNT else None


def _money(value: float) -> str:
    return f"{value:,.2f}" if value >= 1 else f"{value:.6g}"


async def currency(amount: float, source: str, target: str) -> str:
    source, target = source.upper(), target.upper()
    data = await web.get_json(f"https://open.er-api.com/v6/latest/{source}")
    if not isinstance(data, dict) or data.get("result") != "success":
        if isinstance(data, dict) and data.get("error-type") == "unsupported-code":
            raise web.NotFound(f"I don't know the currency {esc(source)}.")
        raise web.WebError(web.UNAVAILABLE)
    rate = (data.get("rates") or {}).get(target)
    if not isinstance(rate, (int, float)):
        raise web.NotFound(f"I don't know the currency {esc(target)}.")
    updated = (data.get("time_last_update_utc") or "")[:16]
    lines = [
        "<b>Currency</b>",
        f"• <b>Amount:</b> <code>{_money(amount)} {source}</code>",
        f"• <b>Result:</b> <code>{_money(amount * rate)} {target}</code>",
        f"• <b>Rate:</b> <code>1 {source} = {rate:.6g} {target}</code>",
    ]
    if updated:
        lines.append(f"• <b>Updated:</b> <code>{esc(updated)}</code>")
    return "\n".join(lines)


# --- /ud ------------------------------------------------------------------------

MAX_UD_TEXT = 600


def _clean_ud(text: str) -> str:
    """Urban Dictionary marks links with [brackets]; keep the words, drop the markup."""
    text = (text or "").replace("[", "").replace("]", "").replace("\r", "").strip()
    return text if len(text) <= MAX_UD_TEXT else text[: MAX_UD_TEXT - 1].rstrip() + "…"


async def urban(term: str) -> str:
    data = await web.get_json("https://api.urbandictionary.com/v0/define", {"term": term.strip()})
    entries = data.get("list") if isinstance(data, dict) else None
    if not isinstance(entries, list) or not entries:
        raise web.NotFound("Urban Dictionary doesn't know that one.")
    try:
        best = max((e for e in entries if isinstance(e, dict)), key=lambda e: int(e.get("thumbs_up") or 0))
        lines = [
            "<b>Urban Dictionary</b>",
            f"• <b>Term:</b> <code>{esc(best.get('word') or term)}</code>",
            f"• <b>Votes:</b> <code>{int(best.get('thumbs_up') or 0)} up, {int(best.get('thumbs_down') or 0)} down</code>",
            "",
            esc(_clean_ud(best["definition"])),
        ]
        if best.get("example"):
            lines += ["", f"<i>{esc(_clean_ud(best['example']))}</i>"]
    except (KeyError, ValueError, TypeError):
        raise web.WebError(web.UNAVAILABLE) from None
    return "\n".join(lines)
