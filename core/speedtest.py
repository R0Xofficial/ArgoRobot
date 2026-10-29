"""
Speed test of the server the bot runs on (used by /speedtest in modules/root.py).

Measures against Cloudflare's public test endpoints: latency (median of five tiny
requests on a warm connection), download (four parallel streams of 25 MB) and
upload (four parallel posts of 10 MB). Takes about ten to twenty seconds.
"""

import asyncio
import statistics
import time

import aiohttp

BASE_URL = "https://speed.cloudflare.com"
STREAMS = 4
DOWNLOAD_BYTES = 25_000_000
UPLOAD_BYTES = 10_000_000
PING_ROUNDS = 5


async def run(base_url: str = BASE_URL) -> tuple[float, float, float]:
    """Returns (ping in ms, download in Mbps, upload in Mbps)."""
    timeout = aiohttp.ClientTimeout(total=60)
    async with aiohttp.ClientSession(timeout=timeout) as session:
        # --- ping ---
        async with session.get(f"{base_url}/__down?bytes=0") as response:
            await response.read()  # opens the connection, not measured
        pings = []
        for _ in range(PING_ROUNDS):
            started = time.perf_counter()
            async with session.get(f"{base_url}/__down?bytes=0") as response:
                await response.read()
            pings.append((time.perf_counter() - started) * 1000)
        ping = statistics.median(pings)

        # --- download ---
        async def download_one() -> int:
            total = 0
            async with session.get(f"{base_url}/__down?bytes={DOWNLOAD_BYTES}") as response:
                response.raise_for_status()
                async for chunk in response.content.iter_chunked(65536):
                    total += len(chunk)
            return total

        started = time.perf_counter()
        sizes = await asyncio.gather(*[download_one() for _ in range(STREAMS)])
        download = sum(sizes) * 8 / (time.perf_counter() - started) / 1_000_000

        # --- upload ---
        payload = bytes(UPLOAD_BYTES)

        async def upload_one() -> int:
            async with session.post(f"{base_url}/__up", data=payload) as response:
                response.raise_for_status()
                await response.read()
            return len(payload)

        started = time.perf_counter()
        sizes = await asyncio.gather(*[upload_one() for _ in range(STREAMS)])
        upload = sum(sizes) * 8 / (time.perf_counter() - started) / 1_000_000

    return ping, download, upload
