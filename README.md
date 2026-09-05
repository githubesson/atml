# ATML

ATML is a small, agent-first service for listing, publishing, and updating static HTML. An agent runs one command against a directory (or a single HTML file), and receives a URL plus a backend-generated 8-digit PIN. Visitors enter the PIN once and receive a scoped, signed browser session.

The server and client are one dependency-free Go binary.

## Build

```sh
go build -o atml ./cmd/atml
```

## Run the server

Generate a long API token, keep it private, and put TLS in front of ATML for any public deployment:

```sh
export ATML_API_TOKEN="$(openssl rand -hex 32)"
export ATML_PUBLIC_URL="https://html.example.com"
./atml serve --addr :8080 --data ./atml-data
```

`ATML_PUBLIC_URL` controls the URLs returned after publishing. The data directory contains the static sites, protected metadata, and the server's persistent signing secret; back it up and mount it on durable storage.

Server options can be supplied as flags or environment variables:

| Flag | Environment | Default |
|---|---|---|
| `--addr` | `ATML_ADDR` | `:8080` |
| `--data` | `ATML_DATA_DIR` | `./atml-data` |
| `--token` | `ATML_API_TOKEN` | required |
| `--public-url` | `ATML_PUBLIC_URL` | inferred from request |
| `--max-upload-bytes` | — | 25 MiB |
| `--max-site-bytes` | — | 100 MiB |
| `--max-files` | — | 500 |
| `--trust-proxy` | `ATML_TRUST_PROXY` | `false` |

The health endpoint is `GET /healthz`.

## Web panel

Open the server’s root URL (for example, `https://html.example.com/`) and sign in with the same bearer token used by the CLI. You can paste either the token alone or `Bearer <token>`.

The panel lists deployed pages with their title, ID, creation date, file count, and size. Search by title or ID, refresh the list, or open a page in a new tab. Use Show PIN to reveal a page’s PIN and Hide PIN to clear it. Reveal uses the bearer-protected `GET /api/v1/sites/{id}/pin` endpoint. Existing pages published before encrypted PIN storage must be unlocked once with their original PIN before reveal is available. Pages retain their existing PIN protection; the list API does not expose PINs.

The token is held only in the panel’s memory, never in browser storage or URLs. Signing out or reloading clears it. The panel is embedded in the Go binary and needs no separate frontend build or server. It reuses `GET /api/v1/sites`; the root URL now serves HTML instead of the service-info JSON. `GET /healthz` is unchanged.

## Configure a client once

```sh
./atml configure \
  --server https://html.example.com \
  --token "$ATML_API_TOKEN"
```

Configuration is stored with owner-only permissions at the platform user-config path (normally `~/.config/atml/config.json`). Set `ATML_CONFIG` to use another path. `ATML_SERVER` and `ATML_TOKEN` can override saved values for automation.

## Publish

The directory must contain `index.html` at its root. A single `.html` file is automatically published as `index.html`.
Because each site is mounted below `/s/<site-id>/`, use relative asset links such as `./assets/app.js` rather than origin-root links such as `/assets/app.js`.

```sh
./atml publish --title "Quarterly prototype" ./dist
```

Example output:

```text
Published 4 files (18342 bytes)
URL: https://html.example.com/s/kx7n2m4pq6rstuva/
PIN: 04278136
```

Agents should request structured output:

```sh
./atml publish --json --title "Quarterly prototype" ./dist
```

Each publish creates a new URL and PIN.

## List existing sites

List every site known to the configured server when you need to find a URL from an earlier session or thread:

```sh
./atml list
```

The default output includes each site's title, URL, ID, creation time, and size. Agents should use structured output:

```sh
./atml list --json
```

Listing uses the configured API token and does not expose site PINs. The underlying API is `GET /api/v1/sites`; results are ordered newest first.

## Update an existing site

Use either the site URL returned by `publish` or its 16-character ID. If the original thread is unavailable, use `atml list` to recover the URL first. Updating replaces the site's complete file set while preserving its URL, PIN, and existing browser sessions. Files omitted from the new source are removed. The current title is preserved unless `--title` is supplied.

```sh
./atml update https://html.example.com/s/kx7n2m4pq6rstuva/ ./dist
```

To change the PIN-screen title at the same time:

```sh
./atml update --title "Revised quarterly prototype" kx7n2m4pq6rstuva ./dist
```

For structured output, add `--json`. The update response does not contain the PIN; use the panel’s Show PIN control or the PIN returned by the original publish.

The underlying API is `PUT /api/v1/sites/<site-id>` with the same bearer authorization, gzip archive format, title header, and upload limits as publishing. Updates are staged and validated before the existing site is replaced, so a rejected upload leaves the current version intact.

## Security model

- Listing, publishing, and updating require a constant-time-checked bearer token.
- PINs come from the operating system's cryptographic random source and always contain exactly 8 digits, including possible leading zeroes.
- PINs are stored as keyed verifiers and AES-GCM encrypted values using a key derived from the persistent server secret. Plaintext PINs are returned only at publish time or through the bearer-protected reveal endpoint; neither the list endpoint nor public pages expose them.
- Failed PIN attempts are limited to five per site and source IP in a ten-minute window.
- Successful unlocks use a 12-hour, HttpOnly, SameSite, site-path-scoped signed cookie. Cookies are marked Secure when the configured public URL uses HTTPS.
- Upload extraction rejects absolute paths, traversal, links, special files, duplicates, oversized archives, excessive expanded content, and excessive file counts.
- Sites are static and cannot access the publish bearer token. Because uploaded HTML is intentionally active content, host ATML on a dedicated origin rather than as a path beneath a sensitive application.

An 8-digit PIN is a sharing gate, not a substitute for user identity, audit logs, or high-assurance authentication. TLS is required to protect PINs and content in transit.

When ATML is reachable only through a trusted reverse proxy, enable `--trust-proxy` so PIN rate limits use the proxy-provided client IP. ATML prefers Cloudflare's `CF-Connecting-IP` header when present. Do not enable this option when clients can connect directly to ATML, because they could forge proxy headers.

## Agent skill

The repository includes [`skills/publish-html`](skills/publish-html/SKILL.md). Install or expose that folder to an agent after installing and configuring the `atml` binary. The skill tells the agent how to find existing sites across threads, prepare static artifacts, and publish or update them safely.
