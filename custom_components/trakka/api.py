"""Asynchronous client for the Trakka REST API.

Covers the routes a Trakka API token opens (docs/API.md#api-tokens): the
token's user and the instance settings, every list the user can open with
its items in one request, and creating, updating, reordering and deleting
items. Every request carries "Authorization: Bearer trk_...".
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import Any

import aiohttp

API_PREFIX = "/api/v1"
DEFAULT_TIMEOUT = 15


class TrakkaError(Exception):
    """Base class for the errors of this client."""


class TrakkaConnectionError(TrakkaError):
    """The server can't be reached, or didn't answer like the Trakka API."""


class TrakkaAuthError(TrakkaError):
    """The API token is unknown or revoked (HTTP 401)."""


class TrakkaApiError(TrakkaError):
    """Trakka refused the request (an HTTP error other than 401)."""

    def __init__(self, status: int, message: str) -> None:
        """Keep the status and the server's own error message."""
        super().__init__(f"HTTP {status}: {message}")
        self.status = status
        self.message = message


@dataclass(frozen=True, slots=True)
class TrakkaUser:
    """The account an API token belongs to."""

    id: int
    email: str
    display_name: str

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> TrakkaUser:
        """Build from GET /api/v1/me."""
        return cls(
            id=int(data["id"]),
            email=str(data.get("email") or ""),
            display_name=str(data.get("display_name") or ""),
        )


@dataclass(frozen=True, slots=True)
class TrakkaItem:
    """An item of a list, with the fields the integration uses."""

    id: int
    list_id: int
    title: str
    done: bool
    quantity: int
    price: float | None
    aisle: str | None
    due_date: str | None
    due_time: str | None
    is_urgent: bool
    labels: tuple[str, ...]
    assignees: tuple[str, ...]

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> TrakkaItem:
        """Build from an item of the API (models.Item)."""
        price = data.get("price")
        return cls(
            id=int(data["id"]),
            list_id=int(data["list_id"]),
            title=str(data.get("title") or ""),
            done=bool(data.get("done")),
            quantity=int(data.get("quantity") or 1),
            price=float(price) if price is not None else None,
            aisle=data.get("aisle") or None,
            due_date=data.get("due_date") or None,
            due_time=data.get("due_time") or None,
            is_urgent=bool(data.get("is_urgent")),
            labels=tuple(str(label) for label in data.get("labels") or ()),
            assignees=tuple(
                str(a.get("display_name") or "") for a in data.get("assignees") or ()
            ),
        )


@dataclass(frozen=True, slots=True)
class TrakkaList:
    """A list the token's user can open, with its items."""

    id: int
    house_id: int
    name: str
    type: str
    writable: bool
    items: tuple[TrakkaItem, ...]

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> TrakkaList:
        """Build from GET /api/v1/lists?scope=all&include=items.

        That mode always says how the list is reached; access_permission is
        "write" for a list of one of the user's Houses, the share's own
        permission otherwise. "items" is left out when the list is empty.
        """
        return cls(
            id=int(data["id"]),
            house_id=int(data.get("house_id") or 0),
            name=str(data.get("name") or ""),
            type=str(data.get("type") or ""),
            writable=data.get("access_permission", "write") == "write",
            items=tuple(TrakkaItem.from_dict(i) for i in data.get("items") or ()),
        )


class TrakkaClient:
    """Talks to one Trakka server with one API token."""

    def __init__(
        self,
        session: aiohttp.ClientSession,
        base_url: str,
        token: str,
        *,
        timeout: float = DEFAULT_TIMEOUT,
    ) -> None:
        """Use session for every request to base_url (the app's own URL)."""
        self._session = session
        self._base_url = base_url.rstrip("/")
        self._token = token
        self._timeout = timeout

    @property
    def base_url(self) -> str:
        """The server's URL, without a trailing slash."""
        return self._base_url

    async def _request(
        self,
        method: str,
        path: str,
        *,
        params: dict[str, str] | None = None,
        json: dict[str, Any] | None = None,
    ) -> Any:
        """Send one request and return its decoded JSON body (None for 204)."""
        headers = {
            "Authorization": f"Bearer {self._token}",
            "Accept": "application/json",
        }
        try:
            async with asyncio.timeout(self._timeout):
                async with self._session.request(
                    method,
                    f"{self._base_url}{API_PREFIX}{path}",
                    params=params,
                    json=json,
                    headers=headers,
                    # A redirect means something stands between us and the
                    # API (a login page of a reverse proxy, http -> https):
                    # report it rather than follow it with the token.
                    allow_redirects=False,
                ) as resp:
                    if resp.status == 401:
                        raise TrakkaAuthError("the API token is unknown or revoked")
                    if resp.status == 204:
                        return None
                    if 300 <= resp.status < 400:
                        raise TrakkaConnectionError(
                            f"HTTP {resp.status} redirect to {resp.headers.get('Location')}"
                        )
                    try:
                        body = await resp.json(content_type=None)
                    except ValueError as err:
                        raise TrakkaConnectionError(
                            f"HTTP {resp.status}: not a Trakka API response"
                        ) from err
                    if resp.status >= 400:
                        message = body.get("error") if isinstance(body, dict) else None
                        raise TrakkaApiError(resp.status, message or "")
                    return body
        except TimeoutError as err:
            raise TrakkaConnectionError("the server did not answer in time") from err
        except aiohttp.ClientError as err:
            raise TrakkaConnectionError(str(err) or type(err).__name__) from err

    async def get_me(self) -> TrakkaUser:
        """Return the token's user."""
        return TrakkaUser.from_dict(await self._request("GET", "/me"))

    async def get_config(self) -> dict[str, Any]:
        """Return the instance settings (saas_mode, timezone)."""
        data = await self._request("GET", "/config")
        return data if isinstance(data, dict) else {}

    async def get_lists(self) -> list[TrakkaList]:
        """Return every list the user can open, with its items."""
        data = await self._request(
            "GET", "/lists", params={"scope": "all", "include": "items"}
        )
        return [TrakkaList.from_dict(entry) for entry in data or ()]

    async def create_item(self, list_id: int, title: str, **fields: Any) -> TrakkaItem:
        """Add an item to a list.

        fields are any other POST /api/v1/items field (quantity, price,
        aisle, is_urgent, due_date, due_time...). On a Courses list a
        product already there is updated instead of added twice.
        """
        body = {"list_id": list_id, "title": title, **fields}
        return TrakkaItem.from_dict(await self._request("POST", "/items", json=body))

    async def update_item(self, item_id: int, **fields: Any) -> TrakkaItem:
        """Change only the given fields of an item (PATCH)."""
        return TrakkaItem.from_dict(
            await self._request("PATCH", f"/items/{item_id}", json=fields)
        )

    async def delete_item(self, item_id: int) -> None:
        """Delete an item."""
        await self._request("DELETE", f"/items/{item_id}")

    async def reorder_items(self, list_id: int, item_ids: list[int]) -> None:
        """Put a list's items in this order; item_ids must name every one."""
        await self._request(
            "PUT", f"/lists/{list_id}/reorder", json={"item_ids": item_ids}
        )
