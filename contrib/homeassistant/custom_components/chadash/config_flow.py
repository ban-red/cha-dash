"""Set up Cha Dash with its address and an API token."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

import aiohttp
import voluptuous as vol
from homeassistant.config_entries import ConfigFlow, ConfigFlowResult
from homeassistant.exceptions import ConfigEntryAuthFailed
from homeassistant.helpers.aiohttp_client import async_get_clientsession

from .const import CONF_TOKEN, CONF_URL, CONF_VERIFY_SSL, DOMAIN
from .coordinator import fetch_summary
from .summary import SummaryError, check_url

REAUTH_SCHEMA = vol.Schema({vol.Required(CONF_TOKEN): str})

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
            if err := check_url(url):
                errors[CONF_URL] = err
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

    async def async_step_reauth(self, entry_data: Mapping[str, Any]) -> ConfigFlowResult:
        """Cha Dash refused the token: it was revoked or lost its scope."""
        return await self.async_step_reauth_confirm()

    async def async_step_reauth_confirm(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        errors: dict[str, str] = {}
        entry = self._get_reauth_entry()
        if user_input is not None:
            token = user_input[CONF_TOKEN].strip()
            verify = entry.data.get(CONF_VERIFY_SSL, True)
            session = async_get_clientsession(self.hass, verify_ssl=verify)
            try:
                await fetch_summary(session, entry.data[CONF_URL], token, verify)
            except ConfigEntryAuthFailed:
                errors["base"] = "invalid_auth"
            except (aiohttp.ClientError, TimeoutError, SummaryError):
                errors["base"] = "cannot_connect"
            else:
                return self.async_update_reload_and_abort(entry, data_updates={CONF_TOKEN: token})
        return self.async_show_form(step_id="reauth_confirm", data_schema=REAUTH_SCHEMA, errors=errors)
