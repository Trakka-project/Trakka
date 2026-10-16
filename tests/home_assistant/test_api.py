"""Tests of the Trakka API client's HTTP handling."""

from __future__ import annotations

import pytest
from pytest_homeassistant_custom_component.test_util.aiohttp import AiohttpClientMocker

from custom_components.trakka.api import (
    TrakkaApiError,
    TrakkaAuthError,
    TrakkaClient,
    TrakkaConnectionError,
)
from fake_trakka import BASE_URL, TOKEN
from homeassistant.core import HomeAssistant
from homeassistant.helpers.aiohttp_client import async_get_clientsession

ME = BASE_URL + "/api/v1/me"


@pytest.fixture
def client(hass: HomeAssistant, aioclient_mock: AiohttpClientMocker) -> TrakkaClient:
    """A client for BASE_URL, given with a trailing slash."""
    return TrakkaClient(async_get_clientsession(hass), BASE_URL + "/", TOKEN)


async def test_sends_the_token(
    client: TrakkaClient, aioclient_mock: AiohttpClientMocker
) -> None:
    """Every request carries the token as a bearer credential."""
    aioclient_mock.get(
        ME, json={"id": 1, "email": "alice@example.com", "display_name": ""}
    )
    user = await client.get_me()
    assert (user.id, user.email, user.display_name) == (1, "alice@example.com", "")
    _, url, _, headers = aioclient_mock.mock_calls[0]
    assert str(url) == ME
    assert headers["Authorization"] == f"Bearer {TOKEN}"


@pytest.mark.parametrize(
    ("response", "error", "message"),
    [
        (
            {"status": 401, "json": {"error": "invalid API token"}},
            TrakkaAuthError,
            "revoked",
        ),
        (
            {
                "status": 403,
                "json": {"error": "this endpoint is not available to API tokens"},
            },
            TrakkaApiError,
            "403: this endpoint",
        ),
        (
            {"status": 500, "text": "oops"},
            TrakkaConnectionError,
            "not a Trakka API response",
        ),
        # A reverse proxy's login page, answered 200.
        (
            {"status": 200, "text": "<html>Sign in</html>"},
            TrakkaConnectionError,
            "not a Trakka API response",
        ),
        (
            {"status": 302, "headers": {"Location": "https://auth.example.com/"}},
            TrakkaConnectionError,
            "redirect to https://auth.example.com/",
        ),
        ({"exc": TimeoutError()}, TrakkaConnectionError, "in time"),
    ],
)
async def test_errors(
    client: TrakkaClient,
    aioclient_mock: AiohttpClientMocker,
    response: dict,
    error: type,
    message: str,
) -> None:
    """Each kind of failure maps to the error the integration acts on."""
    aioclient_mock.get(ME, **response)
    with pytest.raises(error, match=message):
        await client.get_me()


async def test_lists(client: TrakkaClient, aioclient_mock: AiohttpClientMocker) -> None:
    """Lists come with their items; an empty list has no "items" key."""
    aioclient_mock.get(
        BASE_URL + "/api/v1/lists?scope=all&include=items",
        json=[
            {
                "id": 1,
                "house_id": 1,
                "name": "Vide",
                "type": "todo",
                "access_source": "house_member",
                "access_permission": "write",
            },
            {
                "id": 2,
                "house_id": 9,
                "name": "Bricolage",
                "type": "shopping",
                "access_source": "space_share",
                "access_permission": "read",
                "items": [
                    {
                        "id": 5,
                        "list_id": 2,
                        "title": "Vis",
                        "done": False,
                        "quantity": 3,
                        "price": 2,
                        "labels": None,
                        "assignees": [],
                    }
                ],
            },
        ],
    )
    empty, shared = await client.get_lists()
    assert empty.items == ()
    assert empty.writable
    assert not shared.writable
    item = shared.items[0]
    assert (item.price, item.quantity, item.labels) == (2.0, 3, ())
