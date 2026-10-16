"""Tests of the Trakka to-do list entities."""

from __future__ import annotations

import importlib.util
from typing import Any

import pytest
from pytest_homeassistant_custom_component.common import (
    MockConfigEntry,
    async_capture_events,
)
from pytest_homeassistant_custom_component.typing import WebSocketGenerator
import voluptuous as vol

from custom_components.trakka.const import (
    DOMAIN,
    EVENT_ITEM_ADDED,
    EVENT_ITEM_COMPLETED,
    EVENT_ITEM_REMOVED,
    EVENT_ITEM_REOPENED,
)
from fake_trakka import FakeTrakka
from homeassistant.components.todo import TodoListEntityFeature
from homeassistant.config_entries import SOURCE_REAUTH
from homeassistant.const import (
    ATTR_ENTITY_ID,
    ATTR_FRIENDLY_NAME,
    ATTR_SUPPORTED_FEATURES,
    STATE_UNAVAILABLE,
)
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import (
    HomeAssistantError,
    ServiceNotSupported,
    ServiceValidationError,
)
from homeassistant.helpers import entity_registry as er
from homeassistant.setup import async_setup_component

COURSES = "todo.trakka_courses"
TASKS = "todo.trakka_taches"
SHARED = "todo.trakka_bricolage"


async def get_items(hass: HomeAssistant, entity_id: str) -> list[dict[str, Any]]:
    """Return an entity's items through todo.get_items."""
    response = await hass.services.async_call(
        "todo",
        "get_items",
        target={ATTR_ENTITY_ID: entity_id},
        blocking=True,
        return_response=True,
    )
    return response[entity_id]["items"]


async def refresh(hass: HomeAssistant, entry: MockConfigEntry) -> None:
    """Poll the server now."""
    await entry.runtime_data.async_refresh()
    await hass.async_block_till_done()


@pytest.mark.usefixtures("init_integration")
async def test_entities(
    hass: HomeAssistant, entity_registry: er.EntityRegistry
) -> None:
    """Each list is a todo.trakka_<name> entity whose state counts what's left to do."""
    courses = hass.states.get(COURSES)
    assert courses.state == "1"
    assert courses.attributes[ATTR_FRIENDLY_NAME] == "Trakka Courses"
    assert courses.attributes["list_type"] == "groceries"
    assert courses.attributes["read_only"] is False
    # A Courses list has no due dates in Trakka, so it offers none here.
    assert courses.attributes[ATTR_SUPPORTED_FEATURES] == (
        TodoListEntityFeature.CREATE_TODO_ITEM
        | TodoListEntityFeature.UPDATE_TODO_ITEM
        | TodoListEntityFeature.DELETE_TODO_ITEM
        | TodoListEntityFeature.MOVE_TODO_ITEM
    )
    tasks = hass.states.get(TASKS)
    assert tasks.state == "2"
    assert (
        tasks.attributes[ATTR_SUPPORTED_FEATURES]
        & TodoListEntityFeature.SET_DUE_DATETIME_ON_ITEM
    )
    shared = hass.states.get(SHARED)
    assert shared.attributes["read_only"] is True
    assert shared.attributes[ATTR_SUPPORTED_FEATURES] == 0

    entry = entity_registry.async_get(COURSES)
    assert entry.unique_id == f"1@trakka.test:8080_{courses.attributes['list_id']}"
    assert entry.translation_key == "groceries"


@pytest.mark.usefixtures("init_integration")
async def test_items(hass: HomeAssistant, fake_trakka: FakeTrakka) -> None:
    """Items keep their Trakka order, status, due date and details."""
    assert await get_items(hass, COURSES) == [
        {
            "uid": str(fake_trakka.item_titled("Lait")["id"]),
            "summary": "Lait",
            "status": "needs_action",
            "description": "× 2 · Dairy & chilled · 1.15 €",
        },
        {
            "uid": str(fake_trakka.item_titled("Pain")["id"]),
            "summary": "Pain",
            "status": "completed",
        },
    ]
    tasks = await get_items(hass, TASKS)
    # 19:30 in Trakka's zone (Europe/Paris, still summer time on 20 October).
    assert tasks[0]["summary"] == "Sortir les poubelles"
    assert tasks[0]["due"] == "2026-10-20T19:30:00+02:00"
    assert tasks[0]["description"] == "🚨"
    assert tasks[1]["due"] == "2026-10-21"
    assert tasks[1]["description"] == "#Jardin · 👤 Sarah"


@pytest.mark.usefixtures("init_integration")
async def test_add_item(hass: HomeAssistant, fake_trakka: FakeTrakka) -> None:
    """todo.add_item adds the item, and the state follows at once."""
    await hass.services.async_call(
        "todo",
        "add_item",
        {"item": "Beurre"},
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    assert fake_trakka.writes[-1] == (
        "POST",
        "/items",
        {"list_id": hass.states.get(COURSES).attributes["list_id"], "title": "Beurre"},
    )
    assert hass.states.get(COURSES).state == "2"

    # A due date and time in Home Assistant's zone (US/Pacific in tests)
    # becomes a wall-clock time in Trakka's (Europe/Paris): 8:00 PDT is
    # 17:00 CEST.
    await hass.services.async_call(
        "todo",
        "add_item",
        {"item": "Payer la cantine", "due_datetime": "2026-10-22 08:00:00"},
        target={ATTR_ENTITY_ID: TASKS},
        blocking=True,
    )
    body = fake_trakka.writes[-1][2]
    assert (body["due_date"], body["due_time"]) == ("2026-10-22", "17:00")
    await hass.services.async_call(
        "todo",
        "add_item",
        {"item": "Réviser la voiture", "due_date": "2026-11-02"},
        target={ATTR_ENTITY_ID: TASKS},
        blocking=True,
    )
    body = fake_trakka.writes[-1][2]
    assert (body["due_date"], body["due_time"]) == ("2026-11-02", "")

    # A Courses list takes no due date.
    with pytest.raises(ServiceValidationError):
        await hass.services.async_call(
            "todo",
            "add_item",
            {"item": "Oeufs", "due_date": "2026-11-02"},
            target={ATTR_ENTITY_ID: COURSES},
            blocking=True,
        )


@pytest.mark.usefixtures("init_integration")
async def test_add_item_with_details(
    hass: HomeAssistant, fake_trakka: FakeTrakka
) -> None:
    """trakka.add_item sends the quantity, aisle, price and urgent flag."""
    await hass.services.async_call(
        DOMAIN,
        "add_item",
        {
            "item": "Yaourts",
            "quantity": 4,
            "aisle": "dairy",
            "price": 2.5,
            "urgent": True,
        },
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    assert fake_trakka.writes[-1][2] == {
        "list_id": hass.states.get(COURSES).attributes["list_id"],
        "title": "Yaourts",
        "quantity": 4,
        "aisle": "dairy",
        "price": 2.5,
        "is_urgent": True,
    }
    items = await get_items(hass, COURSES)
    assert items[-1]["description"] == "🚨 · × 4 · Dairy & chilled · 2.50 €"

    with pytest.raises(vol.Invalid):  # not an aisle Trakka knows
        await hass.services.async_call(
            DOMAIN,
            "add_item",
            {"item": "x", "aisle": "garage"},
            target={ATTR_ENTITY_ID: COURSES},
            blocking=True,
        )


@pytest.mark.usefixtures("init_integration")
async def test_read_only_share(hass: HomeAssistant, fake_trakka: FakeTrakka) -> None:
    """A list shared read-only takes no change."""
    for domain, data in (
        ("todo", {"item": "Clous"}),
        (DOMAIN, {"item": "Clous"}),
    ):
        with pytest.raises(ServiceNotSupported):
            await hass.services.async_call(
                domain, "add_item", data, target={ATTR_ENTITY_ID: SHARED}, blocking=True
            )
    assert fake_trakka.writes == []


@pytest.mark.usefixtures("init_integration")
async def test_update_item(hass: HomeAssistant, fake_trakka: FakeTrakka) -> None:
    """todo.update_item sends only what changed."""
    milk_id = fake_trakka.item_titled("Lait")["id"]
    await hass.services.async_call(
        "todo",
        "update_item",
        {"item": "Lait", "rename": "Lait demi-écrémé"},
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    assert fake_trakka.writes[-1] == (
        "PATCH",
        f"/items/{milk_id}",
        {"title": "Lait demi-écrémé"},
    )

    await hass.services.async_call(
        "todo",
        "update_item",
        {"item": str(milk_id), "status": "completed"},
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    assert fake_trakka.writes[-1] == ("PATCH", f"/items/{milk_id}", {"done": True})
    assert hass.states.get(COURSES).state == "0"

    task_id = fake_trakka.item_titled("Sortir les poubelles")["id"]
    await hass.services.async_call(
        "todo",
        "update_item",
        {"item": "Sortir les poubelles", "due_datetime": "2026-10-27 11:15:00"},
        target={ATTR_ENTITY_ID: TASKS},
        blocking=True,
    )
    # 11:15 PDT is 19:15 in Paris, back on winter time by 27 October.
    assert fake_trakka.writes[-1] == (
        "PATCH",
        f"/items/{task_id}",
        {"due_date": "2026-10-27", "due_time": "19:15"},
    )

    await hass.services.async_call(
        "todo",
        "update_item",
        {"item": "Sortir les poubelles", "due_datetime": None},
        target={ATTR_ENTITY_ID: TASKS},
        blocking=True,
    )
    assert fake_trakka.writes[-1] == (
        "PATCH",
        f"/items/{task_id}",
        {"due_date": "", "due_time": ""},
    )
    assert "due" not in (await get_items(hass, TASKS))[0]

    # Nothing changed, nothing sent.
    writes = len(fake_trakka.writes)
    await hass.services.async_call(
        "todo",
        "update_item",
        {"item": "Arroser", "status": "needs_action"},
        target={ATTR_ENTITY_ID: TASKS},
        blocking=True,
    )
    assert len(fake_trakka.writes) == writes


@pytest.mark.usefixtures("init_integration")
async def test_remove_items(hass: HomeAssistant, fake_trakka: FakeTrakka) -> None:
    """todo.remove_item and clearing completed items delete them."""
    bread_id = fake_trakka.item_titled("Pain")["id"]
    await hass.services.async_call(
        "todo",
        "remove_completed_items",
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    assert fake_trakka.writes[-1] == ("DELETE", f"/items/{bread_id}", None)
    await hass.services.async_call(
        "todo",
        "remove_item",
        {"item": ["Lait"]},
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    assert hass.states.get(COURSES).state == "0"
    assert await get_items(hass, COURSES) == []


@pytest.mark.usefixtures("init_integration")
async def test_move_item(
    hass: HomeAssistant, hass_ws_client: WebSocketGenerator, fake_trakka: FakeTrakka
) -> None:
    """Dragging an item reorders the whole list in Trakka."""
    first = fake_trakka.item_titled("Sortir les poubelles")["id"]
    second = fake_trakka.item_titled("Arroser")["id"]
    client = await hass_ws_client(hass)
    await client.send_json_auto_id(
        {"type": "todo/item/move", "entity_id": TASKS, "uid": str(second)}
    )
    assert (await client.receive_json())["success"]
    assert fake_trakka.writes[-1][2] == {"item_ids": [second, first]}
    assert [item["summary"] for item in await get_items(hass, TASKS)] == [
        "Arroser",
        "Sortir les poubelles",
    ]

    await client.send_json_auto_id(
        {
            "type": "todo/item/move",
            "entity_id": TASKS,
            "uid": str(second),
            "previous_uid": str(first),
        }
    )
    assert (await client.receive_json())["success"]
    assert fake_trakka.writes[-1][2] == {"item_ids": [first, second]}


async def test_trakka_refuses_a_change(
    hass: HomeAssistant, init_integration: MockConfigEntry, fake_trakka: FakeTrakka
) -> None:
    """Trakka's own error message reaches the user."""
    # The share became read-only after the last poll.
    courses_id = hass.states.get(COURSES).attributes["list_id"]
    fake_trakka.lists[courses_id]["access_permission"] = "read"
    with pytest.raises(HomeAssistantError, match="read-only access to this list"):
        await hass.services.async_call(
            "todo",
            "add_item",
            {"item": "Beurre"},
            target={ATTR_ENTITY_ID: COURSES},
            blocking=True,
        )
    # The refresh that follows every write caught up with it.
    assert hass.states.get(COURSES).attributes["read_only"] is True


async def test_item_events(
    hass: HomeAssistant, init_integration: MockConfigEntry, fake_trakka: FakeTrakka
) -> None:
    """A poll fires an event per item added, completed, reopened or removed."""
    events = {
        name: async_capture_events(hass, name)
        for name in (
            EVENT_ITEM_ADDED,
            EVENT_ITEM_COMPLETED,
            EVENT_ITEM_REOPENED,
            EVENT_ITEM_REMOVED,
        )
    }
    courses_id = hass.states.get(COURSES).attributes["list_id"]
    fake_trakka.add_item(courses_id, "Café", aisle="breakfast", price=4.2)
    fake_trakka.item_titled("Lait")["done"] = True
    fake_trakka.item_titled("Pain")["done"] = False
    del fake_trakka.items[fake_trakka.item_titled("Arroser")["id"]]
    # Items of a list that just appeared aren't news.
    new_list = fake_trakka.add_list("Pharmacie", "shopping")
    fake_trakka.add_item(new_list, "Doliprane")

    await refresh(hass, init_integration)

    assert [e.data for e in events[EVENT_ITEM_ADDED]] == [
        {
            "entity_id": COURSES,
            "list_id": courses_id,
            "list_name": "Courses",
            "list_type": "groceries",
            "item_id": fake_trakka.item_titled("Café")["id"],
            "item": "Café",
            "quantity": 1,
            "aisle": "breakfast",
            "price": 4.2,
            "due_date": None,
            "urgent": False,
        }
    ]
    assert [e.data["item"] for e in events[EVENT_ITEM_COMPLETED]] == ["Lait"]
    assert [e.data["item"] for e in events[EVENT_ITEM_REOPENED]] == ["Pain"]
    assert [
        (e.data["entity_id"], e.data["item"]) for e in events[EVENT_ITEM_REMOVED]
    ] == [(TASKS, "Arroser")]

    # Nothing new, nothing fired.
    await refresh(hass, init_integration)
    assert sum(len(captured) for captured in events.values()) == 4


@pytest.mark.skipif(
    importlib.util.find_spec("homeassistant.components.todo.trigger") is None,
    reason="this Home Assistant predates the todo item triggers",
)
async def test_native_item_triggers(
    hass: HomeAssistant, init_integration: MockConfigEntry, fake_trakka: FakeTrakka
) -> None:
    """Home Assistant's own todo.item_added/item_completed triggers work on these entities."""
    assert await async_setup_component(
        hass,
        "automation",
        {
            "automation": [
                {
                    "triggers": {
                        "trigger": f"todo.{kind}",
                        "target": {"entity_id": COURSES},
                    },
                    "actions": {
                        "event": f"test_{kind}",
                        "event_data": {"ids": "{{ trigger.item_ids }}"},
                    },
                }
                for kind in ("item_added", "item_completed")
            ]
        },
    )
    await hass.async_block_till_done()
    added = async_capture_events(hass, "test_item_added")
    completed = async_capture_events(hass, "test_item_completed")

    await hass.services.async_call(
        "todo",
        "add_item",
        {"item": "Beurre"},
        target={ATTR_ENTITY_ID: COURSES},
        blocking=True,
    )
    fake_trakka.item_titled("Lait")["done"] = True
    await refresh(hass, init_integration)

    assert [e.data["ids"] for e in added] == [
        [str(fake_trakka.item_titled("Beurre")["id"])]
    ]
    assert [e.data["ids"] for e in completed] == [
        [str(fake_trakka.item_titled("Lait")["id"])]
    ]


async def test_lists_come_and_go(
    hass: HomeAssistant,
    init_integration: MockConfigEntry,
    fake_trakka: FakeTrakka,
    entity_registry: er.EntityRegistry,
) -> None:
    """A new list gets an entity, a gone one loses it, a renamed one keeps its id."""
    new_list = fake_trakka.add_list("Pharmacie", "shopping")
    tasks_id = hass.states.get(TASKS).attributes["list_id"]
    del fake_trakka.lists[tasks_id]
    courses_id = hass.states.get(COURSES).attributes["list_id"]
    fake_trakka.lists[courses_id]["name"] = "Courses de la semaine"

    await refresh(hass, init_integration)

    assert hass.states.get("todo.trakka_pharmacie").attributes["list_id"] == new_list
    assert hass.states.get(TASKS) is None
    assert entity_registry.async_get(TASKS) is None
    assert (
        hass.states.get(COURSES).attributes[ATTR_FRIENDLY_NAME]
        == "Trakka Courses de la semaine"
    )

    # The list is shared again: its entity comes back.
    fake_trakka.lists[tasks_id] = {
        "id": tasks_id,
        "house_id": 1,
        "name": "Tâches",
        "type": "todo",
        "access_source": "list_share",
        "access_permission": "write",
    }
    await refresh(hass, init_integration)
    assert hass.states.get(TASKS) is not None


async def test_server_unreachable(
    hass: HomeAssistant, init_integration: MockConfigEntry, fake_trakka: FakeTrakka
) -> None:
    """The entities are unavailable while the server can't be reached."""
    fake_trakka.reachable = False
    await refresh(hass, init_integration)
    assert hass.states.get(COURSES).state == STATE_UNAVAILABLE
    fake_trakka.reachable = True
    await refresh(hass, init_integration)
    assert hass.states.get(COURSES).state == "1"


async def test_token_revoked(
    hass: HomeAssistant, init_integration: MockConfigEntry, fake_trakka: FakeTrakka
) -> None:
    """A revoked token asks the user for a new one, on a poll or on a write."""
    fake_trakka.token_valid = False
    with pytest.raises(HomeAssistantError, match="revoked"):
        await hass.services.async_call(
            "todo",
            "add_item",
            {"item": "Beurre"},
            target={ATTR_ENTITY_ID: COURSES},
            blocking=True,
        )
    await hass.async_block_till_done()
    flows = hass.config_entries.flow.async_progress_by_handler(DOMAIN)
    assert [flow["context"]["source"] for flow in flows] == [SOURCE_REAUTH]
    assert hass.states.get(COURSES).state == STATE_UNAVAILABLE
