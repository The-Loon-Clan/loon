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

## Events

The other direction. An extension is something you **call**; an event is
something that **happens to you**.

```go
// The forum, in Provision -- declare what you emit so it is findable:
c.DeclareEvent(core.EventDef{
    Name: "forum.post.created", Summary: "a member posted in a thread",
    Emitter: "forum", Countable: true, Stable: true,
})

// ...and announce it, wherever a post is created:
c.Emit(ctx, core.Event{Name: "forum.post.created", UserID: u.ID, Subject: postID})

// Achievements, in ITS Provision -- listen:
c.On("forum.post.created", "achievements", func(ctx context.Context, e core.Event) {
    progress.Add(ctx, e.UserID, e.Count)
})
```

The forum does not know achievements exists. Achievements does not know the
forum exists — only that *something* declared `forum.post.created`. Neither
imports the other, and a third listener changes nothing on either side. A
direct call between them would be a dependency, an ordering problem, and a
reason for one plugin's bug to break the other.

**Delivery is synchronous**, in subscription order, and a handler must be
quick — hand off to your own goroutine if it is not. The alternative is a
queue, and a queue that loses its contents on restart is worse than a handler
you can see blocking: it turns "the achievement did not fire" into an
unfalsifiable claim.

**A panicking subscriber is contained.** The post already happened; one
listener's bug must not unwind the action that announced it, nor stop the
listeners after it.

**Declaring is optional, and buys discoverability rather than permission.** An
undeclared event still delivers — failing a member's action over a missing doc
comment would be absurd — but it does not appear in the directory, so the only
way to learn it exists is to read the emitter's source.

`Kind` says **who acted**, and it is required because either default would be
wrong half the time in a way nothing reports:

| kind | meaning |
|---|---|
| `member` | a member did it; `UserID` is the actor and counting it against them is meaningful |
| `system` | the site did it, or it happened *to* the site. `UserID` may name somebody involved, but they did not act |

The case that forced the field: `auth.failed_login_spike` carries the username
being **guessed at** — a victim, not an actor. Before `Kind` existed, the only
thing stopping a subscriber counting failed logins against that member was a
comment asking the emitter to leave `UserID` at zero. A `system` event may not
be `Countable`, and core refuses the combination, because there is no member to
total it against.

Emitting a `member` event with `UserID` zero is logged. That is an emitter that
forgot, and the symptom is otherwise pure silence: every per-member subscriber
skips it, the achievement never moves, and nothing says why.

`Countable` marks an event worth totalling per member. That is what an
achievement threshold can be scored on; "member deleted their account" is an
event nobody should build one from.

`/admin/plugins` lists every declared event with its emitter and its listeners,
and separately lists **subscriptions with no emitter** — a typo, or a plugin
this host does not have. Those are worth surfacing because a listener for an
event that never fires is completely silent, and silence is exactly what it
looks like when everything is fine.

## Status

Production-proven: loon runs behind a full content site (~19 plugins)
and powers the public `loon-demo-site` skeleton, which doubles as
living documentation. Consumers pin it via a sibling-checkout
`replace github.com/the-loon-clan/loon => ../loon` (or `../../loon`). Tagged
releases will follow.
