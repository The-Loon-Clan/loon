# Contributing to loon

loon is a framework, which changes what "a good change" means here. Almost
everything in this repository is an **interface somebody else implements** or a
**contract somebody else calls**, so the cost of a change is paid in repos you
cannot see. That shapes most of what follows.

## Running it

```sh
make help      # the targets
make check     # everything CI runs: fmt, vet, sqllint, test
make test      # just the suite
```

The Go toolchain runs **in a container**, via `scripts/go.sh`. That is not a
preference: on Windows an anti-virus quarantines freshly built unsigned
binaries, and the symptom is not an obvious error — it is a toolchain reporting
`no such tool "compile"` because the compiler disappeared between two commands.
The script's own comment has the detail.

If you would rather use the toolchain on your machine, every target takes it:

```sh
make check GO=go
```

CI runs exactly that, for the same reason: a clean Linux container has no
anti-virus to work around, and nesting one container inside another buys
nothing. **The checks are identical either way** — if `make check` passes
locally it passes in CI, and if it does not, that is a bug in the Makefile
rather than something to work around.

There is **no integration target and no database**. loon defines the storage
seam and the host fills it, so every test here is a unit test. If a change
makes this repo need a Postgres to test, that is worth stopping to discuss:
something in the framework grew a dependency it is supposed to abstract.

## What a change costs

Three repositories depend on this one — `loon-plugins`, `loon-demo-site`, and
whatever else somebody has built — and **loon can be green on its own while
having broken all of them**. Removing a method from an interface, renaming a
struct field, or tightening a validation compiles fine here and nowhere else.

CI builds `loon-plugins` against your commit for exactly this reason. It is a
compile check, not a test run: it catches the shape of a break, not the
behaviour of one. **A change that passes it can still be wrong** — a method
whose meaning changed keeps its signature.

So, in rough order of how much care they need:

| change | what it needs |
|---|---|
| a new function, type, or optional interface | ordinary review |
| a new **method on an existing interface** | every implementer must gain it; say so in the PR |
| a **renamed or removed** exported thing | a reason, and a note in the PR body for consumers |
| a change in what a method **means** with the same signature | the most dangerous kind. Say it loudly; nothing mechanical will catch it |

Optional capabilities are the tool for the second row. Rather than adding a
method to `MetadataSource` and breaking every source, declare a new small
interface and type-assert for it — `TitleFinder`, `CrossIDResolver` and
`CompletionProvider` all exist because of this. A source that does not
implement one keeps working.

## Style

**Comments say why, not what.** The code already says what it does. What it
cannot say is the alternative that was tried, the bug that a line prevents, or
the reason an obvious-looking simplification is wrong. Those are what stop the
next person undoing it — several comments in this repo exist because somebody
did.

**gofmt is not negotiable** — `make check` fails on it. Struct-field alignment
drifts when a longer field name is added, which is the usual cause.

**SQL is constant-only.** `scripts/lint-sql` fails the build on a statement
assembled by concatenation or formatting, because that is how parameterisation
is actually lost. It runs here even though loon barely runs SQL: the linter
*lives* here, and a change that stopped it detecting anything would pass
silently in the only repo that could catch it.

**Fail loud at boot, never at request time.** `core.New` returns an error and
the host is expected to die on it. A framework that starts half-configured and
degrades under traffic turns a five-second startup failure into an incident.

## Events and extensions

Both are documented at length in the README, which is the reference. Two things
worth repeating because they are easy to get wrong:

- **Declaring an event is optional** and buys discoverability, not permission.
  An undeclared event still delivers. It just cannot be found by anybody who
  did not read the emitter.
- **`Kind` is required** on a declared event, because either default would be
  wrong half the time in a way nothing reports. `auth.failed_login_spike`
  carries the username being *guessed at* — a victim, not an actor.

## Pull requests

Say what the change does and, if it touches anything exported, what a consumer
has to do about it. A PR body that says "refactor" over a changed interface is
the one thing that reliably costs somebody else a day.

Small and focused beats large and complete. A PR that changes an interface
*and* fixes three unrelated things is one that has to be reviewed as if all of
it were the risky part.

## Security

Do not open a public issue for a vulnerability. `SECURITY.md` has the process.
