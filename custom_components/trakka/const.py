"""Constants for the Trakka integration."""

from __future__ import annotations

import logging
from typing import Final

DOMAIN: Final = "trakka"
LOGGER = logging.getLogger(__package__)

CONF_SCAN_INTERVAL: Final = "scan_interval"
DEFAULT_SCAN_INTERVAL: Final = 30
MIN_SCAN_INTERVAL: Final = 10
MAX_SCAN_INTERVAL: Final = 3600

# Every API token starts with this (internal/auth.APITokenPrefix).
API_TOKEN_PREFIX: Final = "trk_"

# Events fired on the Home Assistant bus when a poll sees an item change on
# a list (never on the first poll, nor for the items of a list that just
# appeared). Their data carries the item's details, which the native
# todo.item_added/item_completed/item_removed triggers leave out.
EVENT_ITEM_ADDED: Final = "trakka_item_added"
EVENT_ITEM_COMPLETED: Final = "trakka_item_completed"
EVENT_ITEM_REOPENED: Final = "trakka_item_reopened"
EVENT_ITEM_REMOVED: Final = "trakka_item_removed"

SERVICE_ADD_ITEM: Final = "add_item"
ATTR_ITEM: Final = "item"
ATTR_QUANTITY: Final = "quantity"
ATTR_AISLE: Final = "aisle"
ATTR_PRICE: Final = "price"
ATTR_URGENT: Final = "urgent"

# The list types whose items have a due date in the Trakka app
# (FIELD_VISIBILITY_BY_TYPE in static/js/list_view.js): only their entities
# offer due dates.
DUE_DATE_LIST_TYPES: Final = frozenset({"todo", "recurring_shopping"})

# internal/catalog.Aisles, in store order.
AISLES: Final = (
    "produce",
    "bakery",
    "meat_fish",
    "dairy",
    "pantry",
    "breakfast",
    "drinks",
    "baby",
    "pets",
    "household",
    "hygiene",
    "stationery",
    "clothing",
    "electronics",
    "diy",
    "frozen",
    "other",
)

# The server's bounds (maxItemQuantity in internal/handlers/items.go).
MAX_QUANTITY: Final = 100000
