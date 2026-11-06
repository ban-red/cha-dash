"""Set up Cha Dash with its address and an API token."""

from __future__ import annotations

from typing import Any

import aiohttp
import voluptuous as vol
from homeassistant.config_entries import ConfigFlow, ConfigFlowResult
from homeassistant.exceptions import ConfigEntryAuthFailed
from homeassistant.helpers.aiohttp_client import async_get_clientsession

from .const import CONF_TOKEN, CONF_URL, CONF_VERIFY_SSL, DOMAIN
from .coordinator import fetch_summary
from .summary import SummaryError

SCHEMA = vol.Schema(
    {
        vol.Required(CONF_URL): str,
        vol.Required(CONF_TOKEN): str,
        vol.Optional(CONF_VERIFY_SSL, default=True): bool,
    }
)


class ChaDashConfigFlow(ConfigFlow, domain=DOMAIN):
    VERSION = 1

    async def async_step_user(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        errors: dict[str, str] = {}
        if user_input is not None:
            url = user_input[CONF_URL].strip().rstrip("/")
            if not url.startswith(("http://", "https://")):
                errors[CONF_URL] = "invalid_url"
            else:
                await self.async_set_unique_id(url)
                self._abort_if_unique_id_configured()
                session = async_get_clientsession(self.hass, verify_ssl=user_input[CONF_VERIFY_SSL])
                try:
                    await fetch_summary(session, url, user_input[CONF_TOKEN].strip(), user_input[CONF_VERIFY_SSL])
                except ConfigEntryAuthFailed:
                    errors["base"] = "invalid_auth"
                except (aiohttp.ClientError, TimeoutError, SummaryError):
                    errors["base"] = "cannot_connect"
                else:
                    return self.async_create_entry(
                        title="Cha Dash",
                        data={CONF_URL: url, CONF_TOKEN: user_input[CONF_TOKEN].strip(), CONF_VERIFY_SSL: user_input[CONF_VERIFY_SSL]},
                    )
        return self.async_show_form(step_id="user", data_schema=SCHEMA, errors=errors)
