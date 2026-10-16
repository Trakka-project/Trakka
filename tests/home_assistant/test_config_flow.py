"""Tests of the Trakka config flow."""

from __future__ import annotations

from contextlib import nullcontext
from unittest.mock import patch

import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.trakka.config_flow import normalize_url
from custom_components.trakka.const import CONF_SCAN_INTERVAL, DOMAIN
from fake_trakka import BASE_URL, TOKEN, FakeTrakka
from homeassistant.config_entries import SOURCE_USER
from homeassistant.const import CONF_API_TOKEN, CONF_URL, CONF_VERIFY_SSL
from homeassistant.core import HomeAssistant
from homeassistant.data_entry_flow import FlowResultType

USER_INPUT = {
    CONF_URL: BASE_URL + "/",
    CONF_API_TOKEN: f"  {TOKEN} ",
    CONF_VERIFY_SSL: False,
}


@pytest.mark.parametrize(
    ("raw", "expected"),
    [
        ("https://trakka.example.com", "https://trakka.example.com"),
        (" https://trakka.example.com/ ", "https://trakka.example.com"),
        ("http://192.168.1.10:8080/api/v1/", "http://192.168.1.10:8080"),
        ("https://example.com/trakka/", "https://example.com/trakka"),
        ("trakka.lan:8080", None),
        ("ftp://trakka.lan", None),
        ("https://", None),
    ],
)
def test_normalize_url(raw: str, expected: str | None) -> None:
    """The URL is cleaned up, and anything but http(s) refused."""
    assert normalize_url(raw) == expected


@pytest.mark.usefixtures("fake_trakka")
async def test_user_flow(hass: HomeAssistant) -> None:
    """A valid URL and token create the entry, named after the account."""
    result = await hass.config_entries.flow.async_init(
        DOMAIN, context={"source": SOURCE_USER}
    )
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == {}

    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], USER_INPUT
    )
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert result["title"] == "Alice (trakka.test:8080)"
    assert result["data"] == {
        CONF_URL: BASE_URL,
        CONF_API_TOKEN: TOKEN,
        CONF_VERIFY_SSL: False,
    }
    assert result["result"].unique_id == "1@trakka.test:8080"


@pytest.mark.parametrize(
    ("user_input", "setup", "errors"),
    [
        ({CONF_URL: "trakka.lan"}, None, {CONF_URL: "invalid_url"}),
        (
            {CONF_API_TOKEN: "https://trakka.test/api/v1/calendar/feed.ics?token=abc"},
            None,
            {CONF_API_TOKEN: "invalid_token_format"},
        ),
        ({}, "revoke", {"base": "invalid_auth"}),
        ({}, "down", {"base": "cannot_connect"}),
        ({}, "crash", {"base": "unknown"}),
    ],
)
async def test_user_flow_errors(
    hass: HomeAssistant,
    fake_trakka: FakeTrakka,
    user_input: dict,
    setup: str | None,
    errors: dict,
) -> None:
    """Each problem is reported on the form, which can then be fixed."""
    if setup == "revoke":
        fake_trakka.token_valid = False
    elif setup == "down":
        fake_trakka.reachable = False
    result = await hass.config_entries.flow.async_init(
        DOMAIN, context={"source": SOURCE_USER}
    )
    crash = patch(
        "custom_components.trakka.config_flow.TrakkaClient.get_me",
        side_effect=RuntimeError("boom"),
    )
    with crash if setup == "crash" else nullcontext():
        result = await hass.config_entries.flow.async_configure(
            result["flow_id"], USER_INPUT | user_input
        )
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == errors

    fake_trakka.token_valid = fake_trakka.reachable = True
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], USER_INPUT
    )
    assert result["type"] is FlowResultType.CREATE_ENTRY


@pytest.mark.usefixtures("fake_trakka")
async def test_user_flow_already_configured(
    hass: HomeAssistant, config_entry: MockConfigEntry
) -> None:
    """The same account on the same server can't be added twice."""
    config_entry.add_to_hass(hass)
    result = await hass.config_entries.flow.async_init(
        DOMAIN, context={"source": SOURCE_USER}
    )
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], USER_INPUT
    )
    assert result["type"] is FlowResultType.ABORT
    assert result["reason"] == "already_configured"


async def test_reauth(
    hass: HomeAssistant, init_integration: MockConfigEntry, fake_trakka: FakeTrakka
) -> None:
    """A new token for the same account replaces the revoked one."""
    result = await init_integration.start_reauth_flow(hass)
    assert result["step_id"] == "reauth_confirm"

    new_token = "trk_" + "B" * 43
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], {CONF_API_TOKEN: new_token}
    )
    assert result["type"] is FlowResultType.ABORT
    assert result["reason"] == "reauth_successful"
    assert init_integration.data[CONF_API_TOKEN] == new_token


async def test_reauth_other_account(
    hass: HomeAssistant, init_integration: MockConfigEntry, fake_trakka: FakeTrakka
) -> None:
    """A token of another account is refused."""
    result = await init_integration.start_reauth_flow(hass)
    fake_trakka.user = {"id": 2, "email": "bob@example.com", "display_name": "Bob"}
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], {CONF_API_TOKEN: TOKEN}
    )
    assert result["type"] is FlowResultType.ABORT
    assert result["reason"] == "wrong_account"


async def test_reauth_invalid_token(
    hass: HomeAssistant, init_integration: MockConfigEntry, fake_trakka: FakeTrakka
) -> None:
    """A token that doesn't work keeps the form open."""
    result = await init_integration.start_reauth_flow(hass)
    fake_trakka.token_valid = False
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], {CONF_API_TOKEN: TOKEN}
    )
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == {"base": "invalid_auth"}


async def test_reconfigure(
    hass: HomeAssistant, init_integration: MockConfigEntry, fake_trakka: FakeTrakka
) -> None:
    """The server's URL can change; the entry, and so its entities, keep their ids."""
    result = await init_integration.start_reconfigure_flow(hass)
    assert result["step_id"] == "reconfigure"

    # Same server under another name (the fake answers any host it's
    # registered for, so reuse BASE_URL with a different spelling).
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"],
        {CONF_URL: BASE_URL + "/api/v1", CONF_API_TOKEN: TOKEN, CONF_VERIFY_SSL: False},
    )
    assert result["type"] is FlowResultType.ABORT
    assert result["reason"] == "reconfigure_successful"
    assert init_integration.data[CONF_VERIFY_SSL] is False
    assert init_integration.unique_id == "1@trakka.test:8080"

    result = await init_integration.start_reconfigure_flow(hass)
    fake_trakka.user = {"id": 2, "email": "bob@example.com", "display_name": "Bob"}
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"],
        {CONF_URL: BASE_URL, CONF_API_TOKEN: TOKEN, CONF_VERIFY_SSL: True},
    )
    assert result["type"] is FlowResultType.ABORT
    assert result["reason"] == "wrong_account"


async def test_options(hass: HomeAssistant, init_integration: MockConfigEntry) -> None:
    """A new polling interval reloads the entry with it."""
    assert init_integration.runtime_data.update_interval.total_seconds() == 30

    result = await hass.config_entries.options.async_init(init_integration.entry_id)
    assert result["type"] is FlowResultType.FORM
    result = await hass.config_entries.options.async_configure(
        result["flow_id"], {CONF_SCAN_INTERVAL: 120.0}
    )
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert init_integration.options == {CONF_SCAN_INTERVAL: 120}
    await hass.async_block_till_done()
    assert init_integration.runtime_data.update_interval.total_seconds() == 120
