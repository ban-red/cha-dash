"""Polls Cha Dash's summary every 30 seconds."""

from __future__ import annotations

import logging
from datetime import timedelta

import aiohttp
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import ConfigEntryAuthFailed
from homeassistant.helpers.aiohttp_client import async_get_clientsession
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator, UpdateFailed

from .const import CONF_TOKEN, CONF_URL, CONF_VERIFY_SSL, DOMAIN, SCAN_INTERVAL_SECONDS, SUMMARY_PATH
from .summary import Summary, SummaryError, parse

_LOGGER = logging.getLogger(__name__)


async def fetch_summary(session: aiohttp.ClientSession, url: str, token: str, verify_ssl: bool) -> Summary:
    """One GET /api/summary. Raises ConfigEntryAuthFailed on 401 or 403."""
    async with session.get(
        url.rstrip("/") + SUMMARY_PATH,
        headers={"Authorization": f"Bearer {token}", "Accept": "application/json"},
        ssl=None if verify_ssl else False,
        timeout=aiohttp.ClientTimeout(total=10),
        # A redirect could carry the token somewhere else: the hub never
        # sends one for the summary, so one is an error.
        allow_redirects=False,
    ) as resp:
        if resp.status in (401, 403):
            raise ConfigEntryAuthFailed("Cha Dash refused the API token")
        if 300 <= resp.status < 400:
            raise SummaryError("Cha Dash's address redirects; give the address it redirects to")
        resp.raise_for_status()
        return parse(await resp.json())


class ChaDashCoordinator(DataUpdateCoordinator[Summary]):
    """Keeps the latest summary for the entities."""

    def __init__(self, hass: HomeAssistant, entry: ConfigEntry) -> None:
        super().__init__(hass, _LOGGER, name=DOMAIN, update_interval=timedelta(seconds=SCAN_INTERVAL_SECONDS))
        self.entry = entry
        self._session = async_get_clientsession(hass, verify_ssl=entry.data.get(CONF_VERIFY_SSL, True))

    async def _async_update_data(self) -> Summary:
        data = self.entry.data
        try:
            return await fetch_summary(self._session, data[CONF_URL], data[CONF_TOKEN], data.get(CONF_VERIFY_SSL, True))
        except ConfigEntryAuthFailed:
            raise
        except (aiohttp.ClientError, TimeoutError, SummaryError) as err:
            # Never the token: only what went wrong.
            raise UpdateFailed(f"Couldn't read Cha Dash: {err.__class__.__name__}") from err
