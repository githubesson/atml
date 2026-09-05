(() => {
  'use strict';
  const get = (id) => document.getElementById(id);
  let token = '';
  let sites = [];
  let pending;
  const dateFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' });
  function size(bytes) {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }
  function render() {
    const query = get('search').value.trim().toLowerCase();
    const matches = sites.filter((site) => `${site.title} ${site.id}`.toLowerCase().includes(query));
    get('count').textContent = query ? `${matches.length} of ${sites.length} pages` : `${sites.length} ${sites.length === 1 ? 'page' : 'pages'}`;
    get('pages').replaceChildren();
    for (const site of matches) {
      const row = document.createElement('article');
      row.className = 'page';
      const icon = document.createElement('span');
      icon.className = 'page-icon';
      icon.textContent = '▤';
      icon.setAttribute('aria-hidden', 'true');
      const info = document.createElement('div');
      info.className = 'page-info';
      const title = document.createElement('h2');
      title.textContent = site.title || 'Untitled page';
      const metadata = document.createElement('div');
      metadata.className = 'metadata';
      const date = new Date(site.created_at);
      const values = [site.id, Number.isNaN(date.getTime()) ? 'Date unavailable' : `Created ${dateFormat.format(date)}`, `${site.files} ${site.files === 1 ? 'file' : 'files'}`, size(site.bytes)];
      values.forEach((value, index) => {
        const item = document.createElement('span');
        item.textContent = value;
        if (index === 0) item.className = 'site-id';
        metadata.append(item);
      });
      info.append(title, metadata);
      row.append(icon, info);
      const pinButton = document.createElement('button');
      pinButton.className = 'pin-button';
      pinButton.textContent = 'Show PIN';
      pinButton.setAttribute('aria-label', `Show PIN for ${site.title || 'untitled page'}`);
      pinButton.setAttribute('aria-expanded', 'false');
      const pinOutput = document.createElement('span');
      pinOutput.className = 'pin-output';
      pinOutput.setAttribute('role', 'status');
      info.append(pinOutput);
      pinButton.addEventListener('click', async () => {
        if (pinButton.getAttribute('aria-expanded') === 'true') {
          pinOutput.textContent = '';
          pinButton.textContent = 'Show PIN';
          pinButton.setAttribute('aria-expanded', 'false');
          return;
        }
        pinButton.disabled = true;
        pinButton.textContent = 'Loading…';
        pinOutput.textContent = '';
        try {
          const response = await fetch(`./api/v1/sites/${encodeURIComponent(site.id)}/pin`, {
            headers: { Authorization: `Bearer ${token}` },
            cache: 'no-store', credentials: 'omit', redirect: 'error', signal: AbortSignal.timeout(20000),
          });
          if (!row.isConnected) return;
          if (response.status === 401) {
            signOut();
            get('login-error').textContent = 'Your token is no longer valid. Sign in again.';
            return;
          }
          const data = await response.json();
          if (!row.isConnected) return;
          if (!response.ok) throw new Error(data.error || 'Could not load PIN. Try again.');
          if (!/^[0-9]{8}$/.test(data.pin)) throw new Error('The server returned an invalid PIN.');
          pinOutput.textContent = data.pin;
          pinButton.textContent = 'Hide PIN';
          pinButton.setAttribute('aria-expanded', 'true');
        } catch (error) {
          if (row.isConnected) pinOutput.textContent = error.message;
        } finally {
          pinButton.disabled = false;
          if (pinButton.getAttribute('aria-expanded') !== 'true') pinButton.textContent = 'Show PIN';
        }
      });
      row.append(pinButton);
      // Never turn an API-provided non-HTTP URL into an executable link.
      try {
        const url = new URL(site.url);
        if (['http:', 'https:'].includes(url.protocol) && !url.username && !url.password) {
          const link = document.createElement('a');
          link.className = 'open-link';
          link.href = url.href;
          link.target = '_blank';
          link.rel = 'noopener noreferrer';
          link.textContent = 'Open page ↗';
          link.setAttribute('aria-label', `Open ${site.title || 'untitled page'} (new tab)`);
          row.append(link);
        }
      } catch { /* Leave malformed links unclickable. */ }
      get('pages').append(row);
    }
    get('empty').hidden = matches.length !== 0;
    get('empty-title').textContent = query ? 'No matching pages' : 'No pages yet';
  }
  function signOut() {
    pending?.abort();
    pending = undefined;
    token = '';
    sites = [];
    get('token').value = '';
    get('search').value = '';
    get('pages').replaceChildren();
    get('dashboard').hidden = true;
    get('logout').hidden = true;
    get('login').hidden = false;
    get('login-error').textContent = '';
    get('list-error').textContent = '';
    get('token').focus();
  }
  async function load(candidate, signingIn) {
    pending?.abort();
    const controller = new AbortController();
    pending = controller;
    const button = signingIn ? get('submit') : get('refresh');
    const error = signingIn ? get('login-error') : get('list-error');
    error.textContent = '';
    button.disabled = true;
    if (signingIn) button.textContent = 'Signing in…';
    else { button.setAttribute('aria-label', 'Refreshing pages'); button.setAttribute('aria-busy', 'true'); }
    const timeout = setTimeout(() => controller.abort(), 20000);
    try {
      const response = await fetch('./api/v1/sites', {
        headers: { Authorization: `Bearer ${candidate}` },
        cache: 'no-store', credentials: 'omit', redirect: 'error', signal: controller.signal,
      });
      if (response.status === 401) {
        signOut();
        get('login-error').textContent = signingIn ? 'That token was not accepted. Check it and try again.' : 'Your token is no longer valid. Sign in again.';
        return;
      }
      if (!response.ok) throw new Error('Could not load pages. Please try again.');
      const data = await response.json();
      if (!Array.isArray(data.sites)) throw new Error('The server returned an unexpected response.');
      if (pending !== controller) return;
      token = candidate;
      sites = data.sites;
      get('token').value = '';
      get('login').hidden = true;
      get('dashboard').hidden = false;
      get('logout').hidden = false;
      render();
      if (signingIn) get('search').focus();
    } catch (failure) {
      if (pending !== controller) return;
      error.textContent = failure.name === 'AbortError' ? 'The request timed out. Please try again.' : failure instanceof TypeError ? 'Could not reach the server. Check your connection and try again.' : failure.message;
    } finally {
      clearTimeout(timeout);
      button.disabled = false;
      if (signingIn) button.textContent = 'Sign in →';
      else { button.setAttribute('aria-label', 'Refresh pages'); button.removeAttribute('aria-busy'); }
      if (pending === controller) pending = undefined;
    }
  }
  get('login-form').addEventListener('submit', (event) => {
    event.preventDefault();
    const candidate = get('token').value.trim().replace(/^Bearer\s+/i, '');
    if (!candidate) { get('login-error').textContent = 'Enter your API token.'; return; }
    load(candidate, true);
  });
  get('refresh').addEventListener('click', () => load(token, false));
  get('logout').addEventListener('click', signOut);
  get('search').addEventListener('input', render);
})();
