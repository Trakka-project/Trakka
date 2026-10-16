"""An in-memory Trakka API, served through Home Assistant's aiohttp mocker.

It implements the routes the integration calls, with the server's rules
that matter to it (a read-only share refuses writes, a reorder must name
every item, clearing a due date clears its time), so the tests exercise the
integration's HTTP client and its entities together.
"""

from __future__ import annotations

from http import HTTPStatus
import re
from typing import Any

from aiohttp import ClientConnectionError
from pytest_homeassistant_custom_component.test_util.aiohttp import (
    AiohttpClientMocker,
    AiohttpClientMockResponse,
)
from yarl import URL

BASE_URL = "http://trakka.test:8080"
TOKEN = "trk_" + "A" * 43


class FakeTrakka:
    """A Trakka server holding one user's lists."""

    def __init__(self, aioclient_mock: AiohttpClientMocker) -> None:
        """Answer every /api/v1 request to BASE_URL."""
        self.user: dict[str, Any] = {
            "id": 1,
            "email": "alice@example.com",
            "display_name": "Alice",
        }
        self.timezone = "Europe/Paris"
        self.lists: dict[int, dict[str, Any]] = {}
        self.items: dict[int, dict[str, Any]] = {}
        # Flip to make the token revoked (401) or the server unreachable.
        self.token_valid = True
        self.reachable = True
        # Every write the integration sent: (method, path, body).
        self.writes: list[tuple[str, str, Any]] = []
        self._next_id = 1
        pattern = re.compile(rf"^{re.escape(BASE_URL)}/api/v1/")
        for method in ("get", "post", "put", "patch", "delete"):
            aioclient_mock.request(method, pattern, side_effect=self._handle)

    def _id(self) -> int:
        self._next_id += 1
        return self._next_id

    def add_list(
        self, name: str, list_type: str = "todo", permission: str = "write"
    ) -> int:
        """Add a list the user can open, through a House or a share."""
        list_id = self._id()
        self.lists[list_id] = {
            "id": list_id,
            "house_id": 1,
            "name": name,
            "type": list_type,
            "access_source": "house_member" if permission == "write" else "list_share",
            "access_permission": permission,
        }
        return list_id

    def add_item(self, list_id: int, title: str, **fields: Any) -> int:
        """Add an item, as if from the Trakka app."""
        item_id = self._id()
        position = sum(1 for item in self.items.values() if item["list_id"] == list_id)
        self.items[item_id] = {
            "id": item_id,
            "list_id": list_id,
            "title": title,
            "done": False,
            "quantity": 1,
            "position": position,
            "is_urgent": False,
            "labels": [],
            "assignees": [],
            **fields,
        }
        return item_id

    def item_titled(self, title: str) -> dict[str, Any]:
        """Return the item with this title."""
        return next(item for item in self.items.values() if item["title"] == title)

    def _list_body(self, trakka_list: dict[str, Any]) -> dict[str, Any]:
        body = dict(trakka_list)
        items = sorted(
            (
                item
                for item in self.items.values()
                if item["list_id"] == trakka_list["id"]
            ),
            key=lambda item: (item["position"], item["id"]),
        )
        if items:  # like the server: "items" is omitted when empty
            body["items"] = [dict(item) for item in items]
        return body

    async def _handle(
        self, method: str, url: URL, data: Any
    ) -> AiohttpClientMockResponse:
        if not self.reachable:
            raise ClientConnectionError("Connection refused")
        if not self.token_valid:
            return self._json(
                method, url, HTTPStatus.UNAUTHORIZED, {"error": "invalid API token"}
            )
        method = method.upper()
        path = url.path.removeprefix("/api/v1")
        if method != "GET":
            self.writes.append((method, path, data))

        if method == "GET" and path == "/me":
            return self._json(method, url, HTTPStatus.OK, self.user)
        if method == "GET" and path == "/config":
            return self._json(
                method,
                url,
                HTTPStatus.OK,
                {"saas_mode": False, "timezone": self.timezone},
            )
        if method == "GET" and path == "/lists":
            assert url.query.get("scope") == "all"
            assert url.query.get("include") == "items"
            body = [self._list_body(trakka_list) for trakka_list in self.lists.values()]
            return self._json(method, url, HTTPStatus.OK, body)
        if method == "POST" and path == "/items":
            return self._create_item(method, url, data)
        if match := re.fullmatch(r"/items/(\d+)", path):
            return self._item_route(method, url, int(match[1]), data)
        if method == "PUT" and (match := re.fullmatch(r"/lists/(\d+)/reorder", path)):
            return self._reorder(method, url, int(match[1]), data["item_ids"])
        return self._json(
            method,
            url,
            HTTPStatus.FORBIDDEN,
            {"error": "this endpoint is not available to API tokens"},
        )

    def _writable(self, list_id: int) -> bool:
        return self.lists[list_id]["access_permission"] == "write"

    def _create_item(
        self, method: str, url: URL, data: dict[str, Any]
    ) -> AiohttpClientMockResponse:
        if not self._writable(data["list_id"]):
            return self._json(
                method,
                url,
                HTTPStatus.FORBIDDEN,
                {"error": "read-only access to this list"},
            )
        fields = {
            key: value for key, value in data.items() if key not in ("list_id", "title")
        }
        if fields.get("due_date") == "":
            del fields["due_date"]
        if fields.get("due_time") == "":
            del fields["due_time"]
        item_id = self.add_item(data["list_id"], data["title"].strip(), **fields)
        return self._json(method, url, HTTPStatus.CREATED, self.items[item_id])

    def _item_route(
        self, method: str, url: URL, item_id: int, data: Any
    ) -> AiohttpClientMockResponse:
        item = self.items.get(item_id)
        if item is None:
            return self._json(
                method, url, HTTPStatus.NOT_FOUND, {"error": "item not found"}
            )
        if method != "GET" and not self._writable(item["list_id"]):
            return self._json(
                method,
                url,
                HTTPStatus.FORBIDDEN,
                {"error": "read-only access to this list"},
            )
        if method == "DELETE":
            del self.items[item_id]
            return AiohttpClientMockResponse(method, url, status=HTTPStatus.NO_CONTENT)
        if method == "PATCH":
            for key, value in data.items():
                item[key] = value
            if item.get("due_date") == "":
                item.pop("due_date")
                item.pop("due_time", None)
            if item.get("due_time") == "":
                item.pop("due_time")
        return self._json(method, url, HTTPStatus.OK, item)

    def _reorder(
        self, method: str, url: URL, list_id: int, item_ids: list[int]
    ) -> AiohttpClientMockResponse:
        current = {
            item_id
            for item_id, item in self.items.items()
            if item["list_id"] == list_id
        }
        if sorted(item_ids) != sorted(current):
            return self._json(
                method,
                url,
                HTTPStatus.BAD_REQUEST,
                {"error": "item_ids must list every item of the list exactly once"},
            )
        for position, item_id in enumerate(item_ids):
            self.items[item_id]["position"] = position
        return self._json(method, url, HTTPStatus.OK, [self.items[i] for i in item_ids])

    @staticmethod
    def _json(
        method: str, url: URL, status: int, body: Any
    ) -> AiohttpClientMockResponse:
        return AiohttpClientMockResponse(
            method,
            url,
            status=status,
            json=body,
            headers={"Content-Type": "application/json"},
        )
