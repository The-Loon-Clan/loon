# loon

loon is the site framework extracted from the ameNZB indexer: a
plugin runtime and mediator kernel for building community content
sites — Usenet indexers, torrent trackers, or anything with a
catalog, an economy, and members — the way Gazelle sites build on
Gazelle, but as a Go module you `require`.

## What's in the box

- **`core`** — the mediator (`core.New(Deps)`, fails loud) and
  plugin runtime: Caddy-style registration, deterministic topo-sort
  boot, per-schema plugin migrations, process-kind filtering
  (web / worker / all), an extension registry for cross-plugin
  services, and interface seams for everything a plugin consumes:
  Users, Auth (Optional / Authenticate / RequireUser / RequireRole),
  RBAC, Storage, Scheduler, Router, Config, Notifications, Points
  (typed-ledger facade), HTTPClient, Errors.
- **`catalog`** — the domain-swap seam: `MetadataSource`
  (`Domain / TitleIndex / Fetch / Normalize`) with optional
  `TitleFinder`, `CrossIDResolver`, and `CompletionProvider`
  capabilities, `EntityRef`/`CatalogEntry` neutral types, and a
  priority-ordered `Registry`. Register an anime source, a movie
  source, or a golf source — the host machinery doesn't change.
- **`httpclient`** — the SSRF-safe outbound HTTP factory (pooled
  clients, user-URL SafeFetch with DNS-rebinding protection,
  host-allowlisted variants).

loon has zero dependencies on any application package: the host
adapts its own storage, sessions, and job registry onto the
interfaces at its composition root.

## Status

Currently a nested module inside the ameNZB repository
(`replace github.com/ameNZB/loon => ./loon`), consumed by the
reference instance exactly as a third-party dependency would be.
It moves to its own repository, with tagged releases, when the
reference instance's public release ships.
