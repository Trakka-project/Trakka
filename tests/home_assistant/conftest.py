"""Fixtures for the Trakka integration tests."""

from __future__ import annotations

import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry
from pytest_homeassistant_custom_component.test_util.aiohttp import AiohttpClientMocker

from custom_components.trakka.const import DOMAIN
from fake_trakka import BASE_URL, TOKEN, FakeTrakka
from homeassistant.const import CONF_API_TOKEN, CONF_URL, CONF_VERIFY_SSL
from homeassistant.core import HomeAssistant


@pytest.fixture(autouse=True)
def auto_enable_custom_integrations(enable_custom_integrations: None) -> None:
    """Let Home Assistant load custom_components/trakka."""


@pytest.fixture
def fake_trakka(aioclient_mock: AiohttpClientMocker) -> FakeTrakka:
    """A Trakka server with a Courses list, a task list and a read-only share."""
    fake = FakeTrakka(aioclient_mock)
    courses = fake.add_list("Courses", "groceries")
    fake.add_item(courses, "Lait", quantity=2, aisle="dairy", price=1.15)
    fake.add_item(courses, "Pain", done=True)
    tasks = fake.add_list("Tâches", "todo")
    fake.add_item(
        tasks,
        "Sortir les poubelles",
        due_date="2026-10-20",
        due_time="19:30",
        is_urgent=True,
    )
    fake.add_item(
        tasks,
        "Arroser",
        due_date="2026-10-21",
        labels=["Jardin"],
        assignees=[{"user_id": 2, "display_name": "Sarah"}],
    )
    shared = fake.add_list("Bricolage", "shopping", permission="read")
    fake.add_item(shared, "Vis")
    return fake


@pytest.fixture
def config_entry() -> MockConfigEntry:
    """Alice's account on the fake server."""
    return MockConfigEntry(
        domain=DOMAIN,
        title="Alice (trakka.test:8080)",
        unique_id="1@trakka.test:8080",
        data={CONF_URL: BASE_URL, CONF_API_TOKEN: TOKEN, CONF_VERIFY_SSL: True},
    )


@pytest.fixture
async def init_integration(
    hass: HomeAssistant, fake_trakka: FakeTrakka, config_entry: MockConfigEntry
) -> MockConfigEntry:
    """Set the integration up against the fake server."""
    config_entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(config_entry.entry_id)
    await hass.async_block_till_done()
    return config_entry
