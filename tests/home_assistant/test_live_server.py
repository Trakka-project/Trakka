"""End-to-end test against a real Trakka server, over real HTTP.

Skipped unless TRAKKA_E2E_URL and TRAKKA_E2E_TOKEN are set. The server must
answer on 127.0.0.1 (the test harness blocks every other address) and its
token's user must have a "Courses" list (groceries) and a "Tâches" list
(todo), in a server running with APP_TIMEZONE=Europe/Paris. See
docs/HOME_ASSISTANT.md#development.
"""

from __future__ import annotations

import os
from uuid import uuid4

import pytest
from pytest_homeassistant_custom_component.common import (
    MockConfigEntry,
    async_capture_events,
)

from custom_components.trakka.api import TrakkaClient
from custom_components.trakka.const import (
    DOMAIN,
    EVENT_ITEM_ADDED,
    EVENT_ITEM_COMPLETED,
)
from homeassistant.const import (
    ATTR_ENTITY_ID,
    CONF_API_TOKEN,
    CONF_URL,
    CONF_VERIFY_SSL,
)
from homeassistant.core import HomeAssistant
from homeassistant.helpers.aiohttp_client import async_get_clientsession

URL = os.environ.get("TRAKKA_E2E_URL", "")
TOKEN = os.environ.get("TRAKKA_E2E_TOKEN", "")

pytestmark = pytest.mark.skipif(
    not (URL and TOKEN), reason="TRAKKA_E2E_URL/TRAKKA_E2E_TOKEN not set"
)

COURSES = "todo.trakka_courses"
TASKS = "todo.trakka_taches"


# The harness blocks sockets in every test; this one re-enables them, still
# limited to 127.0.0.1.
@pytest.mark.usefixtures("socket_enabled")
async def test_live_server(hass: HomeAssistant) -> None:
    """Every call the integration makes, against the real API."""
    entry = MockConfigEntry(
        domain=DOMAIN,
        unique_id="e2e",
        data={CONF_URL: URL, CONF_API_TOKEN: TOKEN, CONF_VERIFY_SSL: True},
    )
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await hass.async_block_till_done()
    coordinator = entry.runtime_data
    assert str(coordinator.server_time_zone) == "Europe/Paris"
    assert hass.states.get(COURSES) is not None
    assert hass.states.get(TASKS) is not None

    # What the server holds, read with the same token.
    client = TrakkaClient(async_get_clientsession(hass), URL, TOKEN)

    async def server_item(entity_id: str, title: str):
        list_id = hass.states.get(entity_id).attributes["list_id"]
        lists = {
            trakka_list.id: trakka_list for trakka_list in await client.get_lists()
        }
        return next((i for i in lists[list_id].items if i.title == title), None)

    added = async_capture_events(hass, EVENT_ITEM_ADDED)
    completed = async_capture_events(hass, EVENT_ITEM_COMPLETED)
    suffix = uuid4().hex[:6]
    milk, yoghurt, task = f"Lait {suffix}", f"Yaourts {suffix}", f"Poubelles {suffix}"

    # todo.add_item, then trakka.add_item with the details.
    await hass.services.async_call(
        "todo",
        "add_item",
        {"item": milk},
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    await hass.services.async_call(
        DOMAIN,
        "add_item",
        {
            "item": yoghurt,
            "quantity": 4,
            "aisle": "dairy",
            "price": 2.5,
            "urgent": True,
        },
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    item = await server_item(COURSES, yoghurt)
    assert (item.quantity, item.aisle, item.price, item.is_urgent) == (
        4,
        "dairy",
        2.5,
        True,
    )

    # A due date and time: 08:00 in Home Assistant's zone (US/Pacific in
    # tests) is 17:00 in Paris.
    await hass.services.async_call(
        "todo",
        "add_item",
        {"item": task, "due_datetime": "2026-10-22 08:00:00"},
        target={ATTR_ENTITY_ID: TASKS},
        blocking=True,
    )
    item = await server_item(TASKS, task)
    assert (item.due_date, item.due_time) == ("2026-10-22", "17:00")

    # Rename, check off, move, remove.
    await hass.services.async_call(
        "todo",
        "update_item",
        {"item": milk, "rename": f"{milk} bio"},
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    await hass.services.async_call(
        "todo",
        "update_item",
        {"item": f"{milk} bio", "status": "completed"},
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    item = await server_item(COURSES, f"{milk} bio")
    assert item.done
    entity = hass.data["entity_components"]["todo"].get_entity(COURSES)
    last_uid = entity.todo_items[-1].uid
    await entity.async_move_todo_item(last_uid, None)
    assert entity.todo_items[0].uid == last_uid

    # An item added in the Trakka app shows up, and fires the event, at the
    # next poll.
    from_app = await client.create_item(
        hass.states.get(COURSES).attributes["list_id"], f"Café {suffix}"
    )
    await coordinator.async_refresh()
    await hass.async_block_till_done()
    assert f"Café {suffix}" in [e.data["item"] for e in added]
    assert f"{milk} bio" in [e.data["item"] for e in completed]

    await hass.services.async_call(
        "todo",
        "remove_item",
        {"item": [f"{milk} bio", yoghurt, f"Café {suffix}"]},
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    await hass.services.async_call(
        "todo",
        "remove_item",
        {"item": [task]},
        target={ATTR_ENTITY_ID: TASKS},
        blocking=True,
    )
    assert await server_item(COURSES, yoghurt) is None
    assert await server_item(COURSES, from_app.title) is None

    assert await hass.config_entries.async_unload(entry.entry_id)
