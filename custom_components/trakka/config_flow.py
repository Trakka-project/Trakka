"""Config flow for Trakka: the server's URL and an API token."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any
from urllib.parse import urlsplit

import voluptuous as vol

from homeassistant.config_entries import (
    ConfigEntry,
    ConfigFlow,
    ConfigFlowResult,
    OptionsFlowWithReload,
)
from homeassistant.const import CONF_API_TOKEN, CONF_URL, CONF_VERIFY_SSL
from homeassistant.core import callback
from homeassistant.helpers.aiohttp_client import async_get_clientsession
from homeassistant.helpers.selector import (
    BooleanSelector,
    NumberSelector,
    NumberSelectorConfig,
    NumberSelectorMode,
    TextSelector,
    TextSelectorConfig,
    TextSelectorType,
)

from .api import (
    TrakkaApiError,
    TrakkaAuthError,
    TrakkaClient,
    TrakkaConnectionError,
    TrakkaUser,
)
from .const import (
    API_TOKEN_PREFIX,
    CONF_SCAN_INTERVAL,
    DEFAULT_SCAN_INTERVAL,
    DOMAIN,
    LOGGER,
    MAX_SCAN_INTERVAL,
    MIN_SCAN_INTERVAL,
)

URL_SELECTOR = TextSelector(TextSelectorConfig(type=TextSelectorType.URL))
TOKEN_SELECTOR = TextSelector(TextSelectorConfig(type=TextSelectorType.PASSWORD))

USER_SCHEMA = vol.Schema(
    {
        vol.Required(CONF_URL): URL_SELECTOR,
        vol.Required(CONF_API_TOKEN): TOKEN_SELECTOR,
        vol.Optional(CONF_VERIFY_SSL, default=True): BooleanSelector(),
    }
)
REAUTH_SCHEMA = vol.Schema({vol.Required(CONF_API_TOKEN): TOKEN_SELECTOR})


def normalize_url(raw: str) -> str | None:
    """Return the server's base URL, or None if raw isn't an http(s) URL.

    Trailing slashes and a pasted "/api/v1" are dropped: the integration
    adds the API path itself.
    """
    url = raw.strip().rstrip("/")
    url = url.removesuffix("/api/v1").rstrip("/")
    parts = urlsplit(url)
    if parts.scheme not in ("http", "https") or not parts.hostname:
        return None
    return url


def account_id(url: str, user: TrakkaUser) -> str:
    """Return the config entry's unique id: one account on one server."""
    return f"{user.id}@{urlsplit(url).netloc.lower()}"


class TrakkaConfigFlow(ConfigFlow, domain=DOMAIN):
    """Set up a Trakka account."""

    VERSION = 1

    async def _check(
        self, url: str, token: str, verify_ssl: bool
    ) -> tuple[dict[str, str], TrakkaUser | None]:
        """Try the token against the server: (errors, its user)."""
        if not token.startswith(API_TOKEN_PREFIX):
            return {CONF_API_TOKEN: "invalid_token_format"}, None
        client = TrakkaClient(
            async_get_clientsession(self.hass, verify_ssl=verify_ssl), url, token
        )
        try:
            return {}, await client.get_me()
        except TrakkaAuthError:
            return {"base": "invalid_auth"}, None
        except (TrakkaConnectionError, TrakkaApiError) as err:
            LOGGER.debug("Cannot reach Trakka at %s: %s", url, err)
            return {"base": "cannot_connect"}, None
        except Exception:
            LOGGER.exception("Unexpected error checking the Trakka server")
            return {"base": "unknown"}, None

    async def _validated(
        self, user_input: dict[str, Any]
    ) -> tuple[dict[str, str], dict[str, Any], TrakkaUser | None]:
        """Validate the user form: (errors, cleaned data, the token's user)."""
        url = normalize_url(user_input[CONF_URL])
        if url is None:
            return {CONF_URL: "invalid_url"}, user_input, None
        data = {
            CONF_URL: url,
            CONF_API_TOKEN: user_input[CONF_API_TOKEN].strip(),
            CONF_VERIFY_SSL: user_input.get(CONF_VERIFY_SSL, True),
        }
        errors, user = await self._check(
            url, data[CONF_API_TOKEN], data[CONF_VERIFY_SSL]
        )
        return errors, data, user

    async def async_step_user(
        self, user_input: dict[str, Any] | None = None
    ) -> ConfigFlowResult:
        """Ask for the server's URL and an API token."""
        errors: dict[str, str] = {}
        if user_input is not None:
            errors, data, user = await self._validated(user_input)
            if user is not None:
                await self.async_set_unique_id(account_id(data[CONF_URL], user))
                self._abort_if_unique_id_configured()
                host = urlsplit(data[CONF_URL]).netloc
                return self.async_create_entry(
                    title=f"{user.display_name or user.email} ({host})", data=data
                )
        return self.async_show_form(
            step_id="user",
            data_schema=self.add_suggested_values_to_schema(USER_SCHEMA, user_input),
            errors=errors,
        )

    async def async_step_reauth(
        self, entry_data: Mapping[str, Any]
    ) -> ConfigFlowResult:
        """The token was revoked: ask for a new one."""
        return await self.async_step_reauth_confirm()

    async def async_step_reauth_confirm(
        self, user_input: dict[str, Any] | None = None
    ) -> ConfigFlowResult:
        """Ask for a new API token for the same account."""
        entry = self._get_reauth_entry()
        errors: dict[str, str] = {}
        if user_input is not None:
            token = user_input[CONF_API_TOKEN].strip()
            errors, user = await self._check(
                entry.data[CONF_URL], token, entry.data.get(CONF_VERIFY_SSL, True)
            )
            if user is not None:
                await self.async_set_unique_id(account_id(entry.data[CONF_URL], user))
                self._abort_if_unique_id_mismatch(reason="wrong_account")
                return self.async_update_reload_and_abort(
                    entry, data_updates={CONF_API_TOKEN: token}
                )
        return self.async_show_form(
            step_id="reauth_confirm",
            data_schema=REAUTH_SCHEMA,
            errors=errors,
            description_placeholders={"url": entry.data[CONF_URL]},
        )

    async def async_step_reconfigure(
        self, user_input: dict[str, Any] | None = None
    ) -> ConfigFlowResult:
        """Change the URL (the server moved), the token or the TLS check."""
        entry = self._get_reconfigure_entry()
        errors: dict[str, str] = {}
        if user_input is not None:
            errors, data, user = await self._validated(user_input)
            if user is not None:
                # Same account, compared by user id alone: the URL may be
                # what changed. The entry keeps its unique id, which its
                # entities' unique ids are built from.
                if str(entry.unique_id).split("@", 1)[0] != str(user.id):
                    return self.async_abort(reason="wrong_account")
                return self.async_update_reload_and_abort(entry, data_updates=data)
        return self.async_show_form(
            step_id="reconfigure",
            data_schema=self.add_suggested_values_to_schema(
                USER_SCHEMA, user_input or dict(entry.data)
            ),
            errors=errors,
        )

    @staticmethod
    @callback
    def async_get_options_flow(config_entry: ConfigEntry) -> TrakkaOptionsFlow:
        """Return the options flow."""
        return TrakkaOptionsFlow()


class TrakkaOptionsFlow(OptionsFlowWithReload):
    """How often to poll; a change reloads the entry."""

    async def async_step_init(
        self, user_input: dict[str, Any] | None = None
    ) -> ConfigFlowResult:
        """Ask for the polling interval."""
        if user_input is not None:
            # The number selector answers a float.
            return self.async_create_entry(
                data={CONF_SCAN_INTERVAL: int(user_input[CONF_SCAN_INTERVAL])}
            )
        schema = vol.Schema(
            {
                vol.Required(
                    CONF_SCAN_INTERVAL, default=DEFAULT_SCAN_INTERVAL
                ): NumberSelector(
                    NumberSelectorConfig(
                        min=MIN_SCAN_INTERVAL,
                        max=MAX_SCAN_INTERVAL,
                        step=1,
                        mode=NumberSelectorMode.BOX,
                        unit_of_measurement="s",
                    )
                ),
            }
        )
        return self.async_show_form(
            step_id="init",
            data_schema=self.add_suggested_values_to_schema(
                schema, self.config_entry.options
            ),
        )
