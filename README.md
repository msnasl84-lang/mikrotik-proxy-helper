# MikroTik Proxy Helper

`manual-health` subscription and tunnel helper for RouterOS containers.

## Safety invariants

- Automatic failover is disabled.
- Automatic failback is disabled.
- Only the selected profile may be prepared.
- A profile change writes `config.pending.json`; it never overwrites the live Xray config directly.
- RouterOS remains responsible for stopping Xray, validating/applying the pending config, starting Xray, and rolling back.
- Secrets are runtime data and are not embedded in the image.

## Version 0.2 scope

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

Other protocols are detected but intentionally marked unsupported until their core adapters are implemented.

## Runtime mounts

- `/data`: persistent helper state.
- `/shared/xray-config`: shared Xray configuration directory.

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
- Sequential testing of inactive profiles.
- Multi-core support for sing-box.
- Optional failover policy.
