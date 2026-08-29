---
name: publish-html
description: Publish static HTML files or site directories through a configured ATML service and return a PIN-protected share URL. Use when a user asks to publish, host, share, or make an HTML artifact accessible through ATML; do not invoke merely to create or edit local HTML.
---

# Publish HTML

Use the installed `atml` binary. It should already have a server URL and publish token saved by a one-time `atml configure` command.

## Publish

1. Identify the exact static artifact the user intends to expose. Publish a build/output directory, not an entire repository, unless the user explicitly wants every regular file exposed.
2. Ensure a directory has `index.html` at its root and contains every local asset it needs. Use relative asset URLs because the site is hosted below `/s/<site-id>/`; origin-root URLs such as `/assets/app.js` will not resolve inside the site. A single `.html` file is also accepted and becomes `index.html` automatically.
3. Run:

   ```sh
   atml publish --json --title "<short descriptive title>" <file-or-directory>
   ```

4. Read the JSON response and give the user both the exact `url` and the exact 8-character `pin`. Preserve leading zeroes and present the PIN as text, not a number. Keep the URL and PIN separate; never add the PIN to the URL, HTML, or query string.

Publishing is an external write that creates a new immutable site. A request to publish or share the artifact authorizes that publish; a request only to build or edit HTML does not. Each retry that reaches the service may create another URL, so do not retry a successful response.

If `atml` reports that it is not configured, explain that the service owner must supply the ATML server URL and publish token, then configure it once with:

```sh
atml configure --server <service-url> --token <publish-token>
```

Do not invent credentials, expose the saved token, or place the token in project files. If publishing fails before a successful response, report the command error and retain the local artifact.
