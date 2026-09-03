# The panel

The web UI the Go binary serves at `/panel/`. It is plain ES modules and plain
CSS — no build step, no bundler, no dependencies. What is in this directory is
what the browser gets.

## Where the data comes from

One place: `js/api.js`. Every function in it is one route the Go server serves,
named after it, and nothing else in the panel calls `fetch()` for data. A route
that changes shape therefore breaks in one file rather than ten.

There is no second source. The screens were originally drawn against a
directory of JSON fixtures, chosen by a flag, and that is gone: the fixtures
were never shipped inside the binary, so switching to them on a real panel
fetched files that did not exist and drew an empty server over a busy one — and
having somewhere for invented numbers to come from is what let so many of them
survive on screens that were supposed to have been wired up. If something on a
screen is not read from an endpoint, it is not shown.

## Layout

```
index.html          the shell: header, server strip, dock, #view
js/main.js          boot, routes, the header and the appearance menu
js/api.js           every call to the server
js/router.js        hash routing
js/store.js         the polled state every screen reads
js/lib/             dom helpers, formatting, icons, tunnel state, numeric fields
js/ui/              screen (modal) plumbing, toasts, confirms, the server strip
js/views/           one file per screen
views/              one HTML template per screen, opened by js/ui/screen.js
css/                tokens, base, components, one file per screen
```

## Working on it

Run the binary with `--webui` and open `/panel/`. The templates and stylesheets
are embedded, so a change needs a rebuild; everything else is ordinary
front-end work.

Two rules the tests enforce (`internal/webui`):

- every control on every screen has a handler — a `<div class="sw3">` that
  looks like a switch and does nothing is a bug, not a drawing;
- the fields the forms post match the Go structs they post into, by name and
  by type.
