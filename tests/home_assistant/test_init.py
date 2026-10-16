"""Tests of setting up and unloading the Trakka integration."""

from __future__ import annotations

from pytest_homeassistant_custom_component.common import MockConfigEntry

from fake_trakka import FakeTrakka
from homeassistant.config_entries import SOURCE_REAUTH, ConfigEntryState
from homeassistant.core import HomeAssistant


async def test_setup_and_unload(
    hass: HomeAssistant, init_integration: MockConfigEntry
) -> None:
    """The entry loads, reads the server's zone, and unloads."""
    assert init_integration.state is ConfigEntryState.LOADED
    assert str(init_integration.runtime_data.server_time_zone) == "Europe/Paris"
    assert init_integration.runtime_data.aisle_names["dairy"] == "Dairy & chilled"

    assert await hass.config_entries.async_unload(init_integration.entry_id)
    await hass.async_block_till_done()
    assert init_integration.state is ConfigEntryState.NOT_LOADED


async def test_setup_server_down(
    hass: HomeAssistant, fake_trakka: FakeTrakka, config_entry: MockConfigEntry
) -> None:
    """An unreachable server retries setup later."""
    fake_trakka.reachable = False
    config_entry.add_to_hass(hass)
    await hass.config_entries.async_setup(config_entry.entry_id)
    await hass.async_block_till_done()
    assert config_entry.state is ConfigEntryState.SETUP_RETRY


async def test_setup_token_revoked(
    hass: HomeAssistant, fake_trakka: FakeTrakka, config_entry: MockConfigEntry
) -> None:
    """A revoked token at startup asks for a new one."""
    fake_trakka.token_valid = False
    config_entry.add_to_hass(hass)
    await hass.config_entries.async_setup(config_entry.entry_id)
    await hass.async_block_till_done()
    assert config_entry.state is ConfigEntryState.SETUP_ERROR
    assert [
        flow["context"]["source"] for flow in hass.config_entries.flow.async_progress()
    ] == [SOURCE_REAUTH]


async def test_server_without_timezone(
    hass: HomeAssistant, fake_trakka: FakeTrakka, config_entry: MockConfigEntry
) -> None:
    """A server that doesn't say its zone: Home Assistant's own is assumed."""
    fake_trakka.timezone = ""
    config_entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(config_entry.entry_id)
    assert str(config_entry.runtime_data.server_time_zone) == hass.config.time_zone
