# ADR 0003 — internal/manage is layered, behind one name

**Status:** accepted (v1.8.5 cycle, 2026-09-28)

## Context

`internal/manage` does the management work for every front end — the menu,
the panel, the bot, the CLI, the fleet RPC — and had grown to 17,700 lines in
one package, with 41 of its files in a single dependency cycle. A change to how
a config is rendered was a change to the package holding the update path, the
watchdog and the web API, and nothing kept the health checks from reaching into
the setup wizard or the renderer from reading the wizard's types.

The cycle was held together by a few small edges pointing the wrong way (see
`internal/manage/tunnelspec/doc.go`), not by a tangle of real mutual needs.

## Decision

Split by layer, one sub-package at a time, each built and tested on its own
before the next:

| Layer | Package | Depends on |
|---|---|---|
| vocabulary, systemd, listing | `manage/spec`, `manage/core` | nothing in manage |
| this machine | `manage/host` | core, spec |
| a tunnel's configuration | `manage/tunnelspec` | core, spec |
| backup and restore | `manage/backup` | core |
| whether tunnels work | `manage/health` | core, spec, host, tunnelspec, backup |
| wizards, panel API, update, migration | `internal/manage` | all of the above |

Every name keeps its old spelling in `internal/manage` through a `*_alias.go`
file, as `core` and `spec` already did. Tests moved with the code they test;
the set of test functions is identical before and after.

## Consequences

- The largest cycle left is the top layer's own — the setup wizards, the share
  links and the panel's API, 14 files — which is where offering a choice and
  acting on it genuinely meet.
- A caller outside `internal/manage` notices nothing. A change inside one
  layer can no longer reach up: the compiler refuses the import.
- The alias files are the cost: a new exported name in a sub-package has to be
  re-declared to be reachable as `manage.X`. That is deliberate — it keeps the
  public surface a decision rather than an accident.
