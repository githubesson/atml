---
name: publish-html
description: Find, publish, or update static HTML files and site directories through a configured ATML service. Use when a user asks to locate, publish, host, share, or replace an existing ATML artifact; do not invoke merely to create or edit local HTML.
---

# Publish HTML

Use the installed `atml` binary. It should already have a server URL and API token saved by a one-time `atml configure` command.

## Publish

1. Identify the exact static artifact the user intends to expose. Publish a build/output directory, not an entire repository, unless the user explicitly wants every regular file exposed.
2. Ensure a directory has `index.html` at its root and contains every local asset it needs. Use relative asset URLs because the site is hosted below `/s/<site-id>/`; origin-root URLs such as `/assets/app.js` will not resolve inside the site. A single `.html` file is also accepted and becomes `index.html` automatically.
3. Run:

   ```sh
   atml publish --json --title "<short descriptive title>" <file-or-directory>
   ```

4. Read the JSON response and give the user both the exact `url` and the exact 8-character `pin`. Preserve leading zeroes and present the PIN as text, not a number. Keep the URL and PIN separate; never add the PIN to the URL, HTML, or query string.

Publishing is an external write that creates a new site. A request to publish or share the artifact authorizes that publish; a request only to build or edit HTML does not. Each retry that reaches the service may create another URL, so do not retry a successful response.

## Find existing sites

When the user wants an existing ATML site but its URL or ID is unavailable in the current thread, run:

```sh
atml list --json
```

The saved server configuration makes the listing available across threads. Results contain every current site's URL, ID, title, creation time, and size, ordered newest first; PINs are not recoverable and are never included. Use a unique title or other context to identify the intended site. If multiple entries are plausible, ask the user which exact URL to use before updating. Listing is read-only.

## Update

Update a published site only when the user asks to replace that existing site. Confirm the intended site from the user's exact ATML URL or 16-character site ID, prepare the artifact using the same file and asset rules as publishing, then run:

```sh
atml update --json [--title "<new title>"] <site-url-or-id> <file-or-directory>
```

An update replaces the entire remote file set; files absent from the local artifact are removed. It preserves the URL, PIN, and existing browser sessions. Omit `--title` to preserve the current PIN-screen title. The JSON response contains the URL but not the PIN, because ATML does not retain the original PIN in recoverable form. Report the unchanged URL and say that the original PIN still applies; do not invent or request a replacement PIN.

Updating is an external write to the specified site. A request to modify local HTML alone does not authorize it. A rejected upload leaves the current site intact.

If `atml` reports that it is not configured, explain that the service owner must supply the ATML server URL and API token, then configure it once with:

```sh
atml configure --server <service-url> --token <api-token>
```

Do not invent credentials, expose the saved token, or place the token in project files. If listing, publishing, or updating fails, report the command error and retain the local artifact.
