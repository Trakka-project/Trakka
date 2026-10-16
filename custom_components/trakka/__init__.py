"""The Trakka integration: every Trakka list as a Home Assistant to-do list."""

from __future__ import annotations

from homeassistant.const import CONF_API_TOKEN, CONF_URL, CONF_VERIFY_SSL, Platform
from homeassistant.core import HomeAssistant
from homeassistant.helpers.aiohttp_client import async_get_clientsession

from .api import TrakkaClient
from .coordinator import TrakkaConfigEntry, TrakkaCoordinator

PLATFORMS: list[Platform] = [Platform.TODO]


async def async_setup_entry(hass: HomeAssistant, entry: TrakkaConfigEntry) -> bool:
    """Connect to the Trakka server and set up its to-do lists."""
    client = TrakkaClient(
        async_get_clientsession(hass, verify_ssl=entry.data.get(CONF_VERIFY_SSL, True)),
        entry.data[CONF_URL],
        entry.data[CONF_API_TOKEN],
    )
    coordinator = TrakkaCoordinator(hass, entry, client)
    await coordinator.async_config_entry_first_refresh()
    entry.runtime_data = coordinator
    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)
    return True


async def async_unload_entry(hass: HomeAssistant, entry: TrakkaConfigEntry) -> bool:
    """Unload the to-do lists."""
    return await hass.config_entries.async_unload_platforms(entry, PLATFORMS)
