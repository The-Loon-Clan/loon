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

## The extension directory

Plugins publish services for each other through one registry, and consume them
by name:

```go
c.Register("wiki.render", renderer)          // publish
svc, ok := c.Lookup("wiki.render")           // consume, then assert
```

That works and stays supported. But a name and a Go type answer *what do I
assert to* and not *what is this for*, and — the one a type genuinely cannot
answer — *am I meant to call this, or supply it?* `func(context.Context,
int64) error` reads identically either way.

So a seam worth explaining registers with a definition:

```go
c.RegisterDef(core.ExtensionDef{
    Name:    "rewards.trigger",
    Summary: "fire a surface's rewards for one member",
    Kind:    core.ExtService,
    Stable:  true,
}, engine)
```

`Kind` is the direction:

| kind | meaning |
|---|---|
| `service` | the registrant offers behaviour; you call it |
| `callback` | **you** supply it and its owner calls you — a host's counter for a `per_unit` reward is this |
| `data` | a value rather than behaviour — a catalogue, a config set |

Everything lands in the same registry with the same `Lookup` and the same
duplicate rule; the definition only means the directory can describe it.
`/admin/plugins` renders the lot — every core service with whether *this* host
wired it, and every published extension with its kind, summary and the concrete
type to assert to. An undescribed extension still appears, marked
`undescribed`, because a seam you can see and not explain still beats one you
cannot see.

A def with an empty summary is refused. It would take the space the answer goes
in and give nothing back, and `Register(name, svc)` already says "no comment"
more honestly.

`Stable: false` marks a seam still moving. A consumer may depend on it anyway —
this only says they should expect to be broken, which is kinder than finding
out.

## Status

Production-proven: loon runs behind a full content site (~19 plugins)
and powers the public `loon-demo-site` skeleton, which doubles as
living documentation. Consumers pin it via a sibling-checkout
`replace github.com/the-loon-clan/loon => ../loon` (or `../../loon`). Tagged
releases will follow.
