# Sending purchases to Fluv

Trakka can send what you buy to [Fluv](https://github.com/Zit0X/Fluv), the self-hosted personal finance app: every item you check off a purchase list (`shopping`, `groceries`, `recurring_shopping`), with its price, becomes an expense in Fluv. Unchecking it takes the expense back out.

Each Trakka user connects their own Fluv, in **Paramètres → Intégrations → Fluv**. Fluv files the purchases in the account its user chose when linking Trakka, and its rules categorize them like any other operation.

Code: `internal/fluv` (connection settings, IP lock, outbox sender), `internal/handlers/fluv.go` (routes and the hook on item changes), `static/js/fluv.js` (the settings section). Endpoints: [API.md](API.md#fluv-integration). Tables: [DATABASE.md](DATABASE.md#fluv_integrations). The receiving side is Fluv's `POST`/`DELETE /api/v1/integrations/trakka/purchases`.

## Setting it up

1. **Fluv's administrator** allows the address Trakka's requests come from: `FLUV_TRAKKA_ALLOWED_IPS` on the Fluv server (Fluv refuses any other address with `403`, and has no Trakka endpoint at all while it is unset). Behind a reverse proxy, Fluv reads the client address through `FLUV_TRUSTED_PROXIES`.
2. **In Fluv**: Settings → Trakka, pick the account the expenses go to, and link. Fluv shows the purchase endpoint (`https://<fluv>/api/v1/integrations/trakka/purchases`) and a token (`fluv_trk_…`) — the token only once.
3. **In Trakka**: Paramètres → Intégrations → Fluv, paste the endpoint and the token, and save. Trakka resolves Fluv's host name and **locks the connection to its IP address(es)** (see the IP lock below), shown under the URL. Then **Tester**.
4. **Trakka's operator**, only if Fluv is not on a public address (same LAN, same host, a container network, Tailscale): set `FLUV_INTEGRATION_ALLOWED_IPS` on Trakka (see below), or the address Fluv resolves to will be refused.

The test sends a request that changes nothing (it takes back a purchase that doesn't exist): Fluv only answers it `404` once it has accepted both the address and the token, so a successful test means purchases will go through.

## What is sent, and when

| Field | Value |
|---|---|
| `id` | a reference unique to this purchase, `trakka-<item id>-<16 hex>`; checking the same item off again later is a new purchase with a new reference |
| `title` | the item's title (cleaned of control and bidirectional characters, at most 100 characters) |
| `store` | the name of the list (at most 40 characters) — it starts Fluv's label, `‹list› · ‹title› x‹quantity›`, so a Fluv rule on it can file the purchase |
| `quantity` | the item's quantity (at least 1) |
| `amount_minor` | what was paid, in cents: the **price paid** (`actual_price`) if one was entered, otherwise the estimated `price`, times the quantity |
| `date` | the day the item was checked off, in `APP_TIMEZONE` (Fluv records the day, not the time; the exact moment stays in Trakka, `fluv_purchases.purchased_at`) |

- **Only items with a price** are sent. An item checked off without a price is sent as soon as a price is entered for it while it is bought (to the Fluv of whoever enters it).
- **The purchase goes to the Fluv of whoever checked the item off.** Unchecking it, or changing its price, quantity or title while it is bought, acts on that same purchase — whoever does it, since Fluv should mirror whether the item is bought. A House member who hasn't connected Fluv checks items off as before; nothing is sent.
- **Sending is asynchronous.** A purchase waits ~20 seconds before it is sent, so that the price paid typed right after checking the item off, or an immediate uncheck, corrects or drops it before Fluv ever sees it. A correction after it was sent is a cancellation plus a new purchase, on the original date.
- **Nothing is lost while Fluv is down.** Purchases sit in an outbox (`fluv_purchases`) and are retried with an exponential backoff (1, 2, 4 … minutes, at most 6 hours, 16 attempts — about two days). A refusal that retrying can't fix — a revoked token (`401`), an address Fluv doesn't allow (`403`), a wrong endpoint (`404`), a refused purchase (`422`), a redirect — marks the purchase **failed** right away. Paramètres shows how many are waiting and failed, and the last error; **Renvoyer les achats en échec** sends the failed ones again, and so does saving the connection (most failures are fixed by new settings). Sending the same purchase twice is harmless: Fluv ignores a reference it already has.
- **Not sent**: tasks and items on `todo`/`custom` lists; changes made by public-share-link visitors (they aren't anyone's purchase); items the server itself unchecks — a recurring item coming back for its next occurrence, a Courses product added again — whose previous purchase stays recorded; deleting a bought item (that is tidying up, not a refund).
- **Pausing** (unchecking "Envoyer mes achats à Fluv") stops new purchases from being queued; queued ones wait until it is turned back on. **Disconnecting** deletes the connection and whatever was not sent yet; what Fluv already recorded stays there.
- An offline check-off reaches the server when the PWA replays it: its date is the day of the replay.

## Security

### The IP lock

The IP lock is automatic — there is no address to type. When the connection is **saved**, Trakka resolves the endpoint's host name (once) and locks the connection to the addresses it resolves to that the policy below allows — up to four, IPv4 and/or IPv6; addresses it refuses are dropped, and if none is left the save is refused (`address_not_allowed`; `unresolvable` when the name doesn't resolve). An IP address in the URL is its own lock, with no lookup. The locked addresses are stored with the sealed token (`fluv_integrations.pinned_ips`) and shown read-only in Paramètres.

From then on, Trakka **only ever connects to those addresses** to send a purchase: the host name is never resolved again for sending, so a hijacked DNS record, a poisoned resolver or a DNS-rebinding trick can't send the token or the purchases anywhere else. With `https://`, the TLS certificate is still verified against the endpoint's host name (the request carries that name for SNI and verification; only the TCP connection goes to the locked address), so the lock adds to certificate checking rather than replacing it.

**Updating the lock**: saving the connection again re-resolves and re-locks. **Tester** re-resolves too and tests against what the name resolves to *now*; the lock moves to the new addresses **only if Fluv accepts the test there** (with `https://`, that means a server holding a valid certificate for the host name and accepting the token), and the result says so (`relocked`). A failed test leaves the lock where it was. If Fluv changes address, purchases fail (`unreachable`) until you test or save again.

Trust on first use: the lock is only as good as the DNS answer at the moment you save or test. Over `https://` that moment is protected by certificate verification; over `http://` (private networks only, see below) it isn't — save and test from a network you trust.

Which addresses may be locked is the operator's decision, on Trakka's side:

- **`FLUV_INTEGRATION_ALLOWED_IPS` unset** (the default): only public addresses — the same public-only policy as Trakka's other outbound requests (scraper, Web Push). Loopback, private (`10/8`, `172.16/12`, `192.168/16`, `fc00::/7`), CGNAT/Tailscale (`100.64/10`) and reserved ranges are refused.
- **set** to a comma-separated list of addresses or CIDR prefixes (e.g. `192.168.1.20` or `192.168.1.0/24,fd00::/8`): a locked address must be inside one of them — public or not. This is how a Fluv on the LAN, on the same host or on a container network is allowed, without opening every private address to every user. A malformed entry, or a `/0`, refuses to start the server.
- **Always refused**, whatever the setting: link-local addresses (including the `169.254.169.254` cloud metadata endpoint), multicast and unspecified addresses.

Each locked address is checked again at every connection, so narrowing `FLUV_INTEGRATION_ALLOWED_IPS` takes effect on connections already saved (their purchases then fail with `blocked_address`).

This is the IP lock on Trakka's side (where the requests may go). Fluv's `FLUV_TRAKKA_ALLOWED_IPS` is the lock on Fluv's side (where they may come from). Use both.

### Everything else

- **https:// only**, except when every locked address is non-public (only possible through `FLUV_INTEGRATION_ALLOWED_IPS`): the token travels in each request, so cleartext is only accepted when traffic stays on the operator's own network. Prefer https there too when you can; Trakka uses the system's CA certificates (no self-signed certificates).
- **The endpoint must end with `/api/v1/integrations/trakka/purchases`** (a path prefix before it is fine) and carry no credentials, query or fragment — the setting can't point Trakka's requests at an arbitrary path of some other service.
- **No redirects, no proxy.** A redirect is never followed (it would resend the token to wherever `Location` points) and fails the purchase with `redirect_refused`; the client ignores `HTTP(S)_PROXY`; connections aren't kept open; at most 4 KiB of a response is ever read; TLS 1.2 minimum.
- **The token is sealed at rest.** It is an outbound credential — Trakka must send it, so it can't be hashed like Trakka's own tokens. It is encrypted (AES-256-GCM, its own HKDF subkey) with the instance key `backup.key`, which lives next to the database, not in it — the same mechanism as the WebDAV backup password. A copy of the database alone, including a backup, holds no working token. The API never returns it (only `token_set`); its shape is checked (`fluv_trk_` + 26 lowercase base32 characters) before it is stored. If the instance key is replaced or lost, the token can't be decrypted any more (`token_unreadable`): paste it again. A restore brings the key of the backup along, so the restored token keeps working.
- **Logs** carry the user id, the purchase reference, the HTTP status and the error code — never the token, a title, an amount or a response body. Only an error code made of lowercase letters and `_` is kept from Fluv's answers.
- **Routes**: the connection is managed with a session only (`/api/v1/integrations/fluv…`, behind `RequireSession` and the cross-origin check). They are not open to [API tokens](API.md#api-tokens): a leaked Home Assistant token can't read or redirect where anyone's purchases go. The connection test is rate-limited (10 per 5 minutes per account).
- **Turning it off**: `FLUV_INTEGRATION_ENABLED=false` removes the routes (they answer `404`, and the Paramètres section disappears) and stops the sender; nothing is sent, nothing queued is lost.

## Operator settings

| Variable | Default | Meaning |
|---|---|---|
| `FLUV_INTEGRATION_ENABLED` | `true` | Offer the Fluv connection to users. `false`: no routes, no sending. |
| `FLUV_INTEGRATION_ALLOWED_IPS` | *(empty)* | Comma-separated addresses or CIDR prefixes the address a user's Fluv resolves to must be in to be locked; empty means public addresses only. Needed for a Fluv on a private network. |

Example, Trakka and Fluv on the same LAN, Fluv at `192.168.1.20`:

```yaml
# Trakka's compose.yml
environment:
  FLUV_INTEGRATION_ALLOWED_IPS: "192.168.1.20"
```

```bash
# Fluv's environment: Trakka's address as Fluv sees it
FLUV_TRAKKA_ALLOWED_IPS=192.168.1.10
```

Then, in Trakka, endpoint `https://fluv.lan/api/v1/integrations/trakka/purchases` (or `http://192.168.1.20:8080/api/v1/integrations/trakka/purchases` on a trusted LAN) ; Trakka locks onto `192.168.1.20` when you save.

## Error codes

Shown in Paramètres (translated) and returned by the API:

| Code | Meaning |
|---|---|
| `unauthenticated` | Fluv refused the token (revoked, or replaced by linking Trakka again): paste the new one |
| `address_not_allowed` | Fluv doesn't accept requests from Trakka's address (`FLUV_TRAKKA_ALLOWED_IPS`); when saving, the address Fluv resolves to isn't allowed on Trakka's side |
| `not_found` | no such endpoint on Fluv (wrong URL, or Fluv's Trakka integration turned off) |
| `unreachable`, `timeout` | nothing answered at the locked address (Fluv moved? test to re-lock); retried |
| `unresolvable` | when saving or testing: the endpoint's host name doesn't resolve |
| `tls_error` | the certificate doesn't match the endpoint's host name |
| `server_error`, `rate_limited` | Fluv's side; retried |
| `redirect_refused` | Fluv answered with a redirect, never followed |
| `blocked_address` | no locked address passes `FLUV_INTEGRATION_ALLOWED_IPS` any more |
| `token_unreadable` | the stored token can't be decrypted with the current instance key |
| *(other)* | Fluv's own problem code, e.g. a refused purchase |
