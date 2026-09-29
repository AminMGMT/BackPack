# ADR 0002 — "Would this config start?" has one answer, the engine's

**Status:** accepted (v1.8.5 cycle, 2026-09-28)

## Context

Two pieces of code judged a tunnel configuration. The engine's load-time
validation (`cmd/defaults.go validateConfig`) decides whether `backpack -c`
starts, and a reload uses it to decide whether to keep the running tunnel.
`backpack check` (`manage/spec.ValidateConfigFile`) answered the operator's
question before a restart, with its own, smaller set of rules. The two could
disagree: a file `check` passed could still be refused at start — an unknown
transport, a pck tunnel without the capability, a fallback chain naming a
transport the engine does not have, a bad `pck_flags` list.

`check` lives in `internal/cli`; the engine's validation lives in package
`cmd`, which `internal/cli` cannot import.

## Decision

`cmd.CheckConfigFile` runs exactly what `Run` runs before starting —
`loadConfig`, `applyDefaults`, `validateConfig` — without starting anything.
`main` installs it as `cli.EngineCheck`, and `backpack check` reports its
static checks and then the engine's verdict. The static checks stay: they name
every problem at once, where the engine stops at the first.

## Consequences

- A file `check` passes is a file the engine starts, on this machine, as this
  user. The machine-dependent checks (capabilities, interfaces) now run in
  `check` too, which is the point: the operator runs it on the box that will
  run the tunnel.
- A new load-time rule added to `validateConfig` is in `check` with no further
  work.
- The engine logs to stdout, which `check --json` owns; during a check its log
  goes to stderr, so warnings never land ahead of the JSON.
