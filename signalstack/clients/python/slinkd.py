"""
Slinkd Python client.

Usage:
    from slinkd import Slinkd

    ss = Slinkd()  # reads SIGNAL_API_KEY and SIGNAL_HOST from env
    ss.alert("Orderbook empty: depth=0", author="trading-bot")
    ss.send("deploys", type="deployment", text="v2 shipped", author="ci")
"""

import os
import time
import requests


class Slinkd:
    def __init__(self, host=None, api_key=None, default_channel="alerts", author=""):
        self.host = (host or os.environ.get("SIGNAL_HOST", "http://localhost:8080")).rstrip("/")
        self.api_key = api_key or os.environ.get("SIGNAL_API_KEY", "")
        self.default_channel = default_channel
        self.author = author
        self._last_alert = {}  # key -> timestamp, for rate limiting

    def send(self, channel, type, text, author=None, data=None):
        """Send an event to a channel."""
        payload = {
            "type": type,
            "text": text,
            "author": author or self.author,
        }
        if data:
            payload["data"] = data

        resp = requests.post(
            f"{self.host}/channels/{channel}/events",
            json=payload,
            headers={"Authorization": f"Bearer {self.api_key}"},
            timeout=5,
        )
        resp.raise_for_status()
        return resp.json()

    def alert(self, text, author=None, channel=None, cooldown=60):
        """
        Send an alert. Rate-limited by text: won't re-send the same
        alert within `cooldown` seconds.
        """
        now = time.time()
        if text in self._last_alert:
            if now - self._last_alert[text] < cooldown:
                return None  # suppressed

        self._last_alert[text] = now
        return self.send(
            channel=channel or self.default_channel,
            type="alert",
            text=text,
            author=author,
        )
