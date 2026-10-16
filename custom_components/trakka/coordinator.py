"""Polls a Trakka server for every list the API token's user can open."""

from __future__ import annotations

from datetime import timedelta, tzinfo
from typing import Any

from homeassistant.components.todo import DOMAIN as TODO_DOMAIN
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import ConfigEntryAuthFailed
from homeassistant.helpers import entity_registry as er
from homeassistant.helpers.translation import async_get_translations
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator, UpdateFailed
from homeassistant.util import dt as dt_util

from .api import (
    TrakkaAuthError,
    TrakkaClient,
    TrakkaError,
    TrakkaItem,
    TrakkaList,
)
from .const import (
    CONF_SCAN_INTERVAL,
    DEFAULT_SCAN_INTERVAL,
    DOMAIN,
    EVENT_ITEM_ADDED,
    EVENT_ITEM_COMPLETED,
    EVENT_ITEM_REMOVED,
    EVENT_ITEM_REOPENED,
    LOGGER,
)

type TrakkaConfigEntry = ConfigEntry[TrakkaCoordinator]


def list_unique_id(entry: ConfigEntry, list_id: int) -> str:
    """Return the unique id of the to-do entity of a list."""
    return f"{entry.unique_id}_{list_id}"


def scan_interval(entry: ConfigEntry) -> timedelta:
    """Return the polling interval the entry's options ask for."""
    return timedelta(
        seconds=entry.options.get(CONF_SCAN_INTERVAL, DEFAULT_SCAN_INTERVAL)
    )


class TrakkaCoordinator(DataUpdateCoordinator[dict[int, TrakkaList]]):
    """Fetches every list with its items in one request, keyed by list id."""

    config_entry: TrakkaConfigEntry

    def __init__(
        self, hass: HomeAssistant, entry: TrakkaConfigEntry, client: TrakkaClient
    ) -> None:
        """Poll with client at the entry's interval."""
        super().__init__(
            hass,
            LOGGER,
            config_entry=entry,
            name=DOMAIN,
            update_interval=scan_interval(entry),
        )
        self.client = client
        # The zone Trakka's due times are wall-clock times in (APP_TIMEZONE),
        # read from GET /api/v1/config at setup; Home Assistant's own zone
        # until then, or with a server too old to say.
        self.server_time_zone: tzinfo = dt_util.get_default_time_zone()
        # Aisle key -> label in Home Assistant's language, from this
        # integration's own translations (selector.aisle.options).
        self.aisle_names: dict[str, str] = {}

    async def _async_setup(self) -> None:
        """Read the server's time zone, and the aisle labels to show."""
        try:
            config = await self.client.get_config()
        except TrakkaAuthError as err:
            raise ConfigEntryAuthFailed(
                translation_domain=DOMAIN, translation_key="auth_failed"
            ) from err
        except TrakkaError as err:
            raise UpdateFailed(
                translation_domain=DOMAIN,
                translation_key="update_failed",
                translation_placeholders={"error": str(err)},
            ) from err
        if (name := config.get("timezone")) and (zone := dt_util.get_time_zone(name)):
            self.server_time_zone = zone

        prefix = f"component.{DOMAIN}.selector.aisle.options."
        translations = await async_get_translations(
            self.hass, self.hass.config.language, "selector", {DOMAIN}
        )
        self.aisle_names = {
            key.removeprefix(prefix): label
            for key, label in translations.items()
            if key.startswith(prefix)
        }

    async def _async_update_data(self) -> dict[int, TrakkaList]:
        """Fetch every list, and fire the events of what changed since."""
        try:
            lists = await self.client.get_lists()
        except TrakkaAuthError as err:
            raise ConfigEntryAuthFailed(
                translation_domain=DOMAIN, translation_key="auth_failed"
            ) from err
        except TrakkaError as err:
            raise UpdateFailed(
                translation_domain=DOMAIN,
                translation_key="update_failed",
                translation_placeholders={"error": str(err)},
            ) from err
        data = {trakka_list.id: trakka_list for trakka_list in lists}
        if self.data is not None:
            self._fire_item_events(self.data, data)
        return data

    def _fire_item_events(
        self, previous: dict[int, TrakkaList], current: dict[int, TrakkaList]
    ) -> None:
        """Fire an event for each item added, completed, reopened or removed.

        A list that just appeared (created, or newly shared) fires nothing:
        its items aren't news. On a Courses list, adding a product that's
        already there reopens that item rather than adding another, so it
        fires trakka_item_reopened.
        """
        registry = er.async_get(self.hass)
        for list_id, trakka_list in current.items():
            if (before := previous.get(list_id)) is None:
                continue
            entity_id = registry.async_get_entity_id(
                TODO_DOMAIN, DOMAIN, list_unique_id(self.config_entry, list_id)
            )
            old_items = {item.id: item for item in before.items}
            new_items = {item.id: item for item in trakka_list.items}
            for item_id, item in new_items.items():
                old = old_items.get(item_id)
                if old is None:
                    event = EVENT_ITEM_ADDED
                elif item.done and not old.done:
                    event = EVENT_ITEM_COMPLETED
                elif old.done and not item.done:
                    event = EVENT_ITEM_REOPENED
                else:
                    continue
                self.hass.bus.async_fire(
                    event, _event_data(entity_id, trakka_list, item)
                )
            for item_id in old_items.keys() - new_items.keys():
                self.hass.bus.async_fire(
                    EVENT_ITEM_REMOVED,
                    _event_data(entity_id, trakka_list, old_items[item_id]),
                )


def _event_data(
    entity_id: str | None, trakka_list: TrakkaList, item: TrakkaItem
) -> dict[str, Any]:
    """Return the data of an item event."""
    return {
        "entity_id": entity_id,
        "list_id": trakka_list.id,
        "list_name": trakka_list.name,
        "list_type": trakka_list.type,
        "item_id": item.id,
        "item": item.title,
        "quantity": item.quantity,
        "aisle": item.aisle,
        "price": item.price,
        "due_date": item.due_date,
        "urgent": item.is_urgent,
    }
