"""One Home Assistant to-do list entity per Trakka list."""

from __future__ import annotations

from collections.abc import Awaitable
from datetime import date, datetime, time, tzinfo
from typing import Any, TypeVar

import voluptuous as vol

from homeassistant.components.todo import (
    DOMAIN as TODO_DOMAIN,
    TodoItem,
    TodoItemStatus,
    TodoListEntity,
    TodoListEntityFeature,
)
from homeassistant.const import CONF_URL
from homeassistant.core import HomeAssistant, callback
from homeassistant.exceptions import HomeAssistantError, ServiceValidationError
from homeassistant.helpers import (
    config_validation as cv,
    entity_platform,
    entity_registry as er,
)
from homeassistant.helpers.device_registry import DeviceEntryType, DeviceInfo
from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .api import TrakkaApiError, TrakkaAuthError, TrakkaError, TrakkaItem, TrakkaList
from .const import (
    AISLES,
    ATTR_AISLE,
    ATTR_ITEM,
    ATTR_PRICE,
    ATTR_QUANTITY,
    ATTR_URGENT,
    DOMAIN,
    DUE_DATE_LIST_TYPES,
    MAX_QUANTITY,
    SERVICE_ADD_ITEM,
)
from .coordinator import TrakkaConfigEntry, TrakkaCoordinator, list_unique_id

# Writes go through the coordinator's single client and refresh it after:
# no limit beyond Home Assistant's own.
PARALLEL_UPDATES = 0

_T = TypeVar("_T")

ADD_ITEM_SCHEMA: dict[vol.Marker, Any] = {
    vol.Required(ATTR_ITEM): vol.All(cv.string, vol.Length(min=1)),
    vol.Optional(ATTR_QUANTITY): vol.All(
        vol.Coerce(int), vol.Range(min=1, max=MAX_QUANTITY)
    ),
    vol.Optional(ATTR_AISLE): vol.In(AISLES),
    vol.Optional(ATTR_PRICE): vol.All(vol.Coerce(float), vol.Range(min=0)),
    vol.Optional(ATTR_URGENT): cv.boolean,
}


async def async_setup_entry(
    hass: HomeAssistant,
    entry: TrakkaConfigEntry,
    async_add_entities: AddConfigEntryEntitiesCallback,
) -> None:
    """Add an entity per list, and follow lists appearing and disappearing."""
    coordinator = entry.runtime_data
    known: set[int] = set()

    @callback
    def _sync_entities() -> None:
        current = set(coordinator.data)
        if new := current - known:
            known.update(new)
            async_add_entities(
                TrakkaTodoListEntity(coordinator, list_id) for list_id in sorted(new)
            )
        if gone := known - current:
            # A deleted list, or a share that was revoked: its entity goes
            # too, and comes back if the list does.
            known.difference_update(gone)
            registry = er.async_get(hass)
            for list_id in gone:
                if entity_id := registry.async_get_entity_id(
                    TODO_DOMAIN, DOMAIN, list_unique_id(entry, list_id)
                ):
                    registry.async_remove(entity_id)

    _sync_entities()
    entry.async_on_unload(coordinator.async_add_listener(_sync_entities))

    entity_platform.async_get_current_platform().async_register_entity_service(
        SERVICE_ADD_ITEM,
        ADD_ITEM_SCHEMA,
        "async_add_item_with_details",
        [TodoListEntityFeature.CREATE_TODO_ITEM],
    )


def _todo_due(item: TrakkaItem, zone: tzinfo) -> date | datetime | None:
    """Return an item's due date, or date and time in the server's zone."""
    if not item.due_date:
        return None
    try:
        day = date.fromisoformat(item.due_date)
        if not item.due_time:
            return day
        return datetime.combine(day, time.fromisoformat(item.due_time), zone)
    except ValueError:
        return None


def _due_fields(due: date | datetime | None, zone: tzinfo) -> dict[str, str]:
    """Return the due_date/due_time fields Trakka takes for a due value.

    Trakka stores a due time as a wall-clock time in its own zone, so an
    absolute time from Home Assistant is converted to that zone first.
    """
    if due is None:
        return {"due_date": "", "due_time": ""}
    if isinstance(due, datetime):
        local = due.astimezone(zone) if due.tzinfo else due.replace(tzinfo=zone)
        return {
            "due_date": local.date().isoformat(),
            "due_time": local.strftime("%H:%M"),
        }
    return {"due_date": due.isoformat(), "due_time": ""}


class TrakkaTodoListEntity(CoordinatorEntity[TrakkaCoordinator], TodoListEntity):
    """A Trakka list."""

    _attr_has_entity_name = True

    def __init__(self, coordinator: TrakkaCoordinator, list_id: int) -> None:
        """Represent list list_id of the coordinator's data."""
        super().__init__(coordinator)
        entry = coordinator.config_entry
        self._list_id = list_id
        self._attr_unique_id = list_unique_id(entry, list_id)
        self._attr_device_info = DeviceInfo(
            identifiers={(DOMAIN, str(entry.unique_id))},
            name="Trakka",
            manufacturer="Trakka",
            entry_type=DeviceEntryType.SERVICE,
            configuration_url=entry.data[CONF_URL],
        )
        trakka_list = coordinator.data[list_id]
        # The device is "Trakka" and the entity is named after the list, so
        # its entity id is todo.trakka_<list name>.
        self._attr_name = trakka_list.name
        # Only picks the icon (icons.json): the name above takes precedence.
        self._attr_translation_key = trakka_list.type or None

    @property
    def trakka_list(self) -> TrakkaList | None:
        """The list as last polled, None once it's gone."""
        return self.coordinator.data.get(self._list_id)

    @property
    def available(self) -> bool:
        """Unavailable once the list is gone, until the entity is removed."""
        return super().available and self._list_id in self.coordinator.data

    @callback
    def _handle_coordinator_update(self) -> None:
        """Follow a renamed list."""
        if (trakka_list := self.trakka_list) is not None:
            self._attr_name = trakka_list.name
        super()._handle_coordinator_update()

    @property
    def supported_features(self) -> TodoListEntityFeature:
        """Everything on a writable list, nothing on a read-only share."""
        trakka_list = self.trakka_list
        if trakka_list is None or not trakka_list.writable:
            return TodoListEntityFeature(0)
        features = (
            TodoListEntityFeature.CREATE_TODO_ITEM
            | TodoListEntityFeature.UPDATE_TODO_ITEM
            | TodoListEntityFeature.DELETE_TODO_ITEM
            | TodoListEntityFeature.MOVE_TODO_ITEM
        )
        if trakka_list.type in DUE_DATE_LIST_TYPES:
            features |= (
                TodoListEntityFeature.SET_DUE_DATE_ON_ITEM
                | TodoListEntityFeature.SET_DUE_DATETIME_ON_ITEM
            )
        return features

    @property
    def extra_state_attributes(self) -> dict[str, Any] | None:
        """The list's Trakka id and type, and whether it's read-only."""
        if (trakka_list := self.trakka_list) is None:
            return None
        return {
            "list_id": trakka_list.id,
            "list_type": trakka_list.type,
            "read_only": not trakka_list.writable,
        }

    @property
    def todo_items(self) -> list[TodoItem] | None:
        """The list's items, in their Trakka order."""
        if (trakka_list := self.trakka_list) is None:
            return None
        zone = self.coordinator.server_time_zone
        return [
            TodoItem(
                uid=str(item.id),
                summary=item.title,
                status=TodoItemStatus.COMPLETED
                if item.done
                else TodoItemStatus.NEEDS_ACTION,
                due=_todo_due(item, zone),
                description=self._description(item),
            )
            for item in trakka_list.items
        ]

    def _description(self, item: TrakkaItem) -> str | None:
        """Summarize what the Trakka app shows beside the title, read-only."""
        parts: list[str] = []
        if item.is_urgent:
            parts.append("🚨")
        if item.quantity > 1:
            parts.append(f"× {item.quantity}")
        if item.aisle:
            parts.append(self.coordinator.aisle_names.get(item.aisle, item.aisle))
        if item.price is not None:
            amount = f"{item.price:.2f}"
            if self.hass is not None and self.hass.config.language.startswith("fr"):
                amount = amount.replace(".", ",")
            parts.append(f"{amount} €")
        parts.extend(f"#{label}" for label in item.labels)
        if names := [name for name in item.assignees if name]:
            parts.append("👤 " + ", ".join(names))
        return " · ".join(parts) or None

    def _writable_list(self) -> TrakkaList:
        """Return the list, refusing a write to a read-only or missing one."""
        trakka_list = self.trakka_list
        if trakka_list is None or not trakka_list.writable:
            raise ServiceValidationError(
                translation_domain=DOMAIN,
                translation_key="read_only_list",
                translation_placeholders={"list": self.name or str(self._list_id)},
            )
        return trakka_list

    def _find_item(self, trakka_list: TrakkaList, uid: str | None) -> TrakkaItem:
        """Return the list's item with this uid."""
        for item in trakka_list.items:
            if str(item.id) == uid:
                return item
        raise ServiceValidationError(
            translation_domain=DOMAIN,
            translation_key="item_not_found",
            translation_placeholders={"item": str(uid), "list": trakka_list.name},
        )

    async def _call(self, request: Awaitable[_T]) -> _T:
        """Await a request to Trakka, then refresh, whatever its outcome.

        A Trakka error becomes a HomeAssistantError carrying Trakka's own
        message; a revoked token also starts the re-authentication flow.
        """
        try:
            return await request
        except TrakkaAuthError as err:
            self.coordinator.config_entry.async_start_reauth(self.hass)
            raise HomeAssistantError(
                translation_domain=DOMAIN, translation_key="auth_failed"
            ) from err
        except TrakkaApiError as err:
            raise HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="request_refused",
                translation_placeholders={
                    "list": self.name or "",
                    "error": err.message,
                },
            ) from err
        except TrakkaError as err:
            raise HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="cannot_connect",
                translation_placeholders={"error": str(err)},
            ) from err
        finally:
            await self.coordinator.async_refresh()

    async def async_create_todo_item(self, item: TodoItem) -> None:
        """Add an item (todo.add_item)."""
        trakka_list = self._writable_list()
        fields: dict[str, Any] = {}
        if item.due is not None:
            fields = _due_fields(item.due, self.coordinator.server_time_zone)
        await self._call(
            self.coordinator.client.create_item(
                trakka_list.id, item.summary or "", **fields
            )
        )

    async def async_add_item_with_details(
        self,
        item: str,
        quantity: int | None = None,
        aisle: str | None = None,
        price: float | None = None,
        urgent: bool | None = None,
    ) -> None:
        """Add an item with a quantity, aisle, price or urgent flag (trakka.add_item)."""
        trakka_list = self._writable_list()
        fields: dict[str, Any] = {}
        if quantity is not None:
            fields["quantity"] = quantity
        if aisle is not None:
            fields["aisle"] = aisle
        if price is not None:
            fields["price"] = price
        if urgent is not None:
            fields["is_urgent"] = urgent
        await self._call(
            self.coordinator.client.create_item(trakka_list.id, item, **fields)
        )

    async def async_update_todo_item(self, item: TodoItem) -> None:
        """Rename, check off, uncheck or reschedule an item (todo.update_item).

        Only what changed is sent, so the item's other Trakka fields stay as
        they are.
        """
        trakka_list = self._writable_list()
        current = self._find_item(trakka_list, item.uid)
        changes: dict[str, Any] = {}
        if item.summary is not None and item.summary.strip() != current.title:
            changes["title"] = item.summary
        if item.status is not None:
            done = item.status == TodoItemStatus.COMPLETED
            if done != current.done:
                changes["done"] = done
        zone = self.coordinator.server_time_zone
        wanted_due = _due_fields(item.due, zone)
        if wanted_due != _due_fields(_todo_due(current, zone), zone):
            changes.update(wanted_due)
        if changes:
            await self._call(self.coordinator.client.update_item(current.id, **changes))

    async def async_delete_todo_items(self, uids: list[str]) -> None:
        """Delete items (todo.remove_item, or clearing completed items)."""
        trakka_list = self._writable_list()
        items = [self._find_item(trakka_list, uid) for uid in uids]

        async def delete_all() -> None:
            for item in items:
                try:
                    await self.coordinator.client.delete_item(item.id)
                except TrakkaApiError as err:
                    if err.status != 404:  # already gone is what was asked
                        raise

        await self._call(delete_all())

    async def async_move_todo_item(
        self, uid: str, previous_uid: str | None = None
    ) -> None:
        """Move an item after previous_uid, or first (drag and drop)."""
        trakka_list = self._writable_list()
        if uid == previous_uid:
            return
        order = [item.id for item in trakka_list.items]
        moved = self._find_item(trakka_list, uid).id
        order.remove(moved)
        index = 0
        if previous_uid is not None:
            index = order.index(self._find_item(trakka_list, previous_uid).id) + 1
        order.insert(index, moved)
        await self._call(self.coordinator.client.reorder_items(trakka_list.id, order))
