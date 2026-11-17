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
from core.i18n import tr

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
        raise web.WebError(tr(web.UNAVAILABLE)) from None
    if not translated:
        raise web.NotFound(tr("lookup.i_couldn_t_translate_that"))
    source_name = tr("common.unknown") if source == "auto" else language_name("he" if source == "iw" else source)
    return (
        tr("lookup.translation_from_to", source_name=esc(source_name), language_name=esc(language_name(target)), translated=esc(translated))
    )


# --- /dict ----------------------------------------------------------------------

MAX_MEANINGS = 3
MAX_DEFINITIONS = 2


async def dictionary(word: str) -> str:
    word = word.strip()
    try:
        data = await web.get_json(f"https://api.dictionaryapi.dev/api/v2/entries/en/{quote(word, safe='')}")
    except web.NotFound:
        raise web.NotFound(tr("lookup.i_couldn_t_find_that_word")) from None
    try:
        entry = data[0]
        lines = [tr("lookup.dictionary"), tr("lookup.word", v=esc(entry.get('word') or word))]
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
        raise web.WebError(tr(web.UNAVAILABLE)) from None
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
        raise web.NotFound(tr("lookup.i_couldn_t_find_that_place"))
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
            raise web.NotFound(tr("lookup.i_couldn_t_find_the_timezone"))
    try:
        now = datetime.now(ZoneInfo(zone_name))
    except Exception:
        raise web.NotFound(tr("lookup.i_couldn_t_find_the_timezone")) from None
    return (
        tr("lookup.time_in_time_date_timezone", title=esc(title), now=now, zone_name=esc(zone_name), offset_text=_offset_text(now))
    )


WEATHER_CODES = {
    0: "lookup.weather_codes.0", 1: "lookup.weather_codes.1", 2: "lookup.weather_codes.2", 3: "lookup.weather_codes.3", 45: "lookup.weather_codes.45", 48: "lookup.weather_codes.48",
    51: "lookup.weather_codes.51", 53: "lookup.weather_codes.53", 55: "lookup.weather_codes.55", 56: "lookup.weather_codes.56", 57: "lookup.weather_codes.57",
    61: "lookup.weather_codes.61", 63: "lookup.weather_codes.63", 65: "lookup.weather_codes.65", 66: "lookup.weather_codes.66", 67: "lookup.weather_codes.67",
    71: "lookup.weather_codes.71", 73: "lookup.weather_codes.73", 75: "lookup.weather_codes.75", 77: "lookup.weather_codes.77",
    80: "lookup.weather_codes.80", 81: "lookup.weather_codes.81", 82: "lookup.weather_codes.82", 85: "lookup.weather_codes.85", 86: "lookup.weather_codes.86",
    95: "lookup.weather_codes.95", 96: "lookup.weather_codes.96", 99: "lookup.weather_codes.99",
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
            tr("lookup.weather_in", place_title=esc(_place_title(place))),
            tr("lookup.conditions", get=tr(WEATHER_CODES.get(int(now['weather_code']), 'Unknown'))),
            tr("lookup.temperature_c_f", temp=temp, fahrenheit=_fahrenheit(temp)),
            tr("lookup.feels_like_c_f", feels=feels, fahrenheit=_fahrenheit(feels)),
            tr("lookup.humidity", float=float(now['relative_humidity_2m'])),
            tr("lookup.wind_km_h_mph_from", wind=wind, v=wind / 1.609344, direction=direction),
            tr("lookup.precipitation_mm", float=float(now['precipitation'])),
        ]
    except (TypeError, KeyError, ValueError):
        raise web.WebError(tr(web.UNAVAILABLE)) from None
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
            raise web.NotFound(tr("lookup.i_don_t_know_the_currency", source=esc(source)))
        raise web.WebError(tr(web.UNAVAILABLE))
    rate = (data.get("rates") or {}).get(target)
    if not isinstance(rate, (int, float)):
        raise web.NotFound(tr("lookup.i_don_t_know_the_currency_2", target=esc(target)))
    updated = (data.get("time_last_update_utc") or "")[:16]
    lines = [
        tr("lookup.currency"),
        tr("lookup.amount", money=_money(amount), source=source),
        tr("lookup.result", money=_money(amount * rate), target=target),
        tr("lookup.rate_1", source=source, rate=rate, target=target),
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
        raise web.NotFound(tr("lookup.urban_dictionary_doesn_t_know_that"))
    try:
        best = max((e for e in entries if isinstance(e, dict)), key=lambda e: int(e.get("thumbs_up") or 0))
        lines = [
            tr("lookup.urban_dictionary"),
            tr("lookup.term", v=esc(best.get('word') or term)),
            tr("lookup.votes_up_down", v=int(best.get('thumbs_up') or 0), v2=int(best.get('thumbs_down') or 0)),
            "",
            esc(_clean_ud(best["definition"])),
        ]
        if best.get("example"):
            lines += ["", f"<i>{esc(_clean_ud(best['example']))}</i>"]
    except (KeyError, ValueError, TypeError):
        raise web.WebError(tr(web.UNAVAILABLE)) from None
    return "\n".join(lines)
