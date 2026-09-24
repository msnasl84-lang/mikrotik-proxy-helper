# MikroTik Proxy Helper

## Version 0.3.0-dev.6

This release separates profile preparation from activation and adds file-based
RouterOS mode requests for `vless`, `ovpn`, `blocked`, and `direct`. RouterOS
remains the only privileged controller; the Helper stores no router password.

- Close SSE streams when the application context is cancelled.
- Allow an intentional RouterOS container stop to finish without reaching the HTTP shutdown deadline.
- Add a regression test for SSE shutdown behavior.

`manual-health` subscription and tunnel helper for RouterOS containers.

## Safety invariants

- Automatic failover is disabled.
- Automatic failback is disabled.
- Only the selected profile may be prepared.
- A profile change writes `config.pending.json`; it never overwrites the live Xray config directly.
- RouterOS remains responsible for stopping Xray, validating/applying the pending config, starting Xray, and rolling back.
- Secrets are runtime data and are not embedded in the image.

## Version 0.3 development scope

- HTTP subscription download.
- Base64 or plain-text subscription parsing.
- Dynamic VLESS profile inventory.
- Manual profile selection.
- Xray config generation for VLESS raw/TCP links.
- End-to-end HTTP health check through the active Xray SOCKS5 listener.
- Persistent state in `/data`.
- Pending apply request for the RouterOS coordinator.
- In-page action results without navigation or reload.
- Responsive status cards, profile table, and operation log.
- Graceful `SIGTERM`/`SIGINT` shutdown with an eight-second deadline.
- A process lock in `/data/helper.lock` to prevent concurrent helper instances.
- Health worker cancellation during shutdown.
- An isolated, short-lived Xray process for testing one inactive profile.
- Loopback-only test SOCKS listeners allocated from a bounded port pool.
- Per-test total, startup, and HTTP timeouts with deterministic process cleanup.
- Sequential Test All with one isolated Xray probe at a time.
- Per-profile end-to-end TTFB latency with automatic `Best` highlighting.
- Persistent latest test results restored after a Helper restart.
- Live progress events over authenticated SSE.
- Cancellation of the active Test All run.
- Atomic retention of the latest 20 completed test runs.

Other protocols are detected but intentionally marked unsupported until their core adapters are implemented.

## Runtime mounts

- `/data`: persistent helper state.
- `/shared/xray-config`: shared Xray configuration directory.

## RouterOS firewall requirements for isolated tests

The isolated Xray process runs inside the Helper container, so RouterOS must
allow the Helper address to resolve DNS and connect to the VLESS server. Keep
these permissions narrow:

- allow TCP from the Helper address only to a `VLESS-SERVER` address list;
- do not restrict the destination port, because subscription providers may
  change it;
- allow TCP/UDP port 53 only to an explicit `VLESS-DNS` address list;
- keep all of these accept rules before the final forward-chain drop rule.

The Helper does not manage RouterOS firewall or address-list entries. If a
subscription changes the server hostname, add the new hostname to
`VLESS-SERVER` before testing or activating that profile.

## Runtime environment

| Variable | Default |
|---|---|
| `LISTEN_ADDR` | `:8080` |
| `SUBSCRIPTION_URL` | empty; required |
| `DATA_DIR` | `/data` |
| `XRAY_CONFIG_DIR` | `/shared/xray-config` |
| `SOCKS_ADDR` | `172.19.0.2:1080` |
| `HEALTH_URL` | Cloudflare trace URL |
| `HEALTH_INTERVAL_SECONDS` | `60` |
| `HEALTH_TIMEOUT_SECONDS` | `10` |
| `HEALTH_FAILURE_LIMIT` | `3` |
| `HELPER_USER` | `admin` |
| `HELPER_PASSWORD` | empty; set in production to enable HTTP Basic authentication |
| `XRAY_BINARY` | `/usr/local/bin/xray` |
| `TEST_URL` | Cloudflare trace URL |
| `TEST_START_TIMEOUT_SECONDS` | `4` |
| `TEST_HTTP_TIMEOUT_SECONDS` | `10` |
| `TEST_TOTAL_TIMEOUT_SECONDS` | `15` |
| `TEST_PORT_MIN` / `TEST_PORT_MAX` | `12000` / `12031` |
| `TEST_ALL_CONCURRENCY` | `1` |

## Test API

| Endpoint | Purpose |
|---|---|
| `POST /api/tests/profile` | Test one profile through an isolated Xray process. |
| `POST /api/tests/all` | Start a sequential Test All run. |
| `POST /api/tests/cancel` | Cancel the active Test All run. |
| `GET /api/tests` | Return retained completed runs. |
| `GET /api/events` | Stream live test events using SSE. |

## Build

The included GitHub Actions workflow publishes:

```text
ghcr.io/<github-user>/mikrotik-proxy-helper:latest
```

for `linux/arm/v7`, `linux/arm64`, and `linux/amd64`.

Before publishing, replace `OWNER` in `go.mod` with the GitHub owner if desired.

## Not implemented yet

- RouterOS coordinator script.
- Xray binary validation of pending configuration.
- Atomic activation/rollback.
- Multi-connection testing.
- Multi-core support for sing-box.
- Optional failover policy.
