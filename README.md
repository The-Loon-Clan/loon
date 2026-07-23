<p align="center">
  <img src="img/logo.png" alt="loon" width="180">
</p>

<h1 align="center">loon</h1>

loon is a site framework extracted from a production indexer: a
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
- **`schedule`** — the job runtime: `JobInfo`, the registry, the
  cron-like `ServiceLoop`, off-peak gating, interval overrides, and
  CPU accounting behind the host's `/admin/jobs`.
- **`nntp`** — an NNTP client + connection pool (greeting validation,
  overview fetch, STAT), used by the usenet indexer plugin.
- **`img`** — small image helpers (resize/encode) with no cgo.

loon has zero dependencies on any application package: the host
adapts its own storage, sessions, and job registry onto the
interfaces at its composition root.

## Status

Production-proven: loon runs behind a full content site (~19 plugins)
and powers the public `loon-demo-site` skeleton, which doubles as
living documentation. Consumers pin it via a sibling-checkout
`replace github.com/the-loon-clan/loon => ../loon` (or `../../loon`). Tagged
releases will follow.
