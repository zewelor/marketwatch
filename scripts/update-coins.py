#!/usr/bin/env python3
"""Refresh the embedded CoinLore ID and symbol snapshot."""

import json
import os
from pathlib import Path
import tempfile
from urllib.error import HTTPError, URLError
from urllib.request import HTTPRedirectHandler, Request, build_opener


URL = "https://api.coinlore.net/api/assets/"
MAX_RESPONSE_BYTES = 4 * 1024 * 1024
TIMEOUT_SECONDS = 10
OUTPUT = Path(__file__).resolve().parents[1] / "internal" / "monitor" / "coins.json"


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, file, code, message, headers, new_url):
        return None


def fetch_coins():
    request = Request(URL, headers={"Accept": "application/json", "User-Agent": "marketwatch coin catalog updater"})
    opener = build_opener(NoRedirect())
    with opener.open(request, timeout=TIMEOUT_SECONDS) as response:
        raw = response.read(MAX_RESPONSE_BYTES + 1)
    return parse_coins(raw)


def parse_coins(raw):
    if len(raw) > MAX_RESPONSE_BYTES:
        raise ValueError(f"CoinLore response exceeds {MAX_RESPONSE_BYTES} bytes")

    payload = json.loads(raw)
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), list):
        raise ValueError("CoinLore response must contain a data array")

    coins = []
    seen_ids = set()
    for index, row in enumerate(payload["data"]):
        if not isinstance(row, dict):
            raise ValueError(f"CoinLore data row {index} must be an object")
        coin_id = row.get("id")
        symbol = row.get("symbol")
        if (
            not isinstance(coin_id, str)
            or not coin_id.isascii()
            or not coin_id.isdecimal()
            or int(coin_id) <= 0
        ):
            raise ValueError(f"CoinLore data row {index} has an invalid positive decimal string ID")
        canonical_id = str(int(coin_id))
        if canonical_id in seen_ids:
            raise ValueError(f"CoinLore data contains duplicate ID {canonical_id}")
        if not isinstance(symbol, str) or not symbol.strip():
            raise ValueError(f"CoinLore data row {index} has an empty or invalid symbol")
        seen_ids.add(canonical_id)
        coins.append({"id": canonical_id, "symbol": symbol})

    if not coins:
        raise ValueError("CoinLore data array is empty")
    coins.sort(key=lambda coin: int(coin["id"]))
    return coins


def write_atomically(coins):
    contents = json.dumps(coins, ensure_ascii=False, indent=2) + "\n"
    temporary_path = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w", encoding="utf-8", newline="\n", dir=OUTPUT.parent,
            prefix=f".{OUTPUT.name}.", suffix=".tmp", delete=False,
        ) as temporary:
            temporary_path = Path(temporary.name)
            temporary.write(contents)
            temporary.flush()
            os.fsync(temporary.fileno())
        os.replace(temporary_path, OUTPUT)
        directory_fd = os.open(OUTPUT.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    except Exception:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)
        raise


def main():
    try:
        coins = fetch_coins()
        write_atomically(coins)
    except (HTTPError, URLError, OSError, ValueError, json.JSONDecodeError) as error:
        raise SystemExit(f"update CoinLore catalog failed: {error}") from error
    print(f"wrote {len(coins)} CoinLore coins to {OUTPUT}")


if __name__ == "__main__":
    main()
