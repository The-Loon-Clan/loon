# Security

## Reporting a vulnerability

Please report privately through GitHub's
[security advisories](https://github.com/The-Loon-Clan/loon/security/advisories/new)
rather than opening a public issue.

Include what you did, what happened, and what you expected. A proof of concept
helps but is not required — a clear description of the flaw is enough to act on.

This is a small project without a paid security team, so expect a first reply
within a week rather than within hours.

## What this software is

**A framework, not an application.** loon is a plugin runtime and a set of
interfaces; it stores nothing, serves no routes of its own, and authenticates
nobody. The host supplies storage, sessions, and auth by implementing the seams
loon defines.

That shapes what a vulnerability here means. Most of the security-relevant
surface is not code loon runs — it is a **contract loon publishes**, and the
dangerous failure is an interface whose guarantee is weaker than its name
suggests. A host that reads `SafeFetch` and believes it will not reach
`169.254.169.254` has made a security decision on the strength of a method
name.

Report those as vulnerabilities. "This is safe only if the caller also does X,
and nothing says so" is a real finding here even when no line of loon is wrong.

## What is defended, and how

**Outbound SSRF** (`httpclient`). `NewSafeFetch` refuses RFC1918, loopback,
link-local, cloud metadata (169.254.0.0/16), CGNAT, and the IPv6 equivalents.
`NewWhitelisted` adds a suffix-matched host allowlist on top.

The check runs in `net.Dialer.Control`, which is handed the **address actually
being connected to**, after resolution and before connect. That placement *is*
the defence: a check in `DialContext` sees the hostname, and resolving it there
to inspect the addresses does not bind the connection to what was inspected —
the dial resolves again, and an attacker serving a low-TTL record answers the
first lookup publicly and the second with `127.0.0.1`. A pre-resolution sweep
still runs as a second layer, because it can refuse a host that offers a
blocked address at all and can say which one; but it is the diagnosis, not the
guarantee.

**This was wrong until 21 Aug 2026** and is written up here rather than
quietly fixed, because the shape of the mistake is worth more than the fix: the
ranges were right, the allowlist matching was right, both were unit-tested, and
the client was vulnerable the whole time. Nothing tested the dialer, so nothing
saw that the guard was reading a different lookup from the one that connected.
The doc comment claimed rebinding safety throughout.

**SQL injection** (`scripts/lint-sql`). Statements must be constants. The
linter fails the build on SQL assembled by concatenation or formatting, which
is how parameterisation is actually lost, and it runs in this repo — which
barely executes SQL — because the linter *lives* here and a change that stopped
it detecting anything would pass silently in the only repo that could catch it.

**Fail-loud boot** (`core.New`). Misconfiguration is an error the host is
expected to die on, not a degraded mode. A framework that starts
half-configured turns a five-second startup failure into an incident under
traffic.

**Contained subscribers** (the event bus). A panicking listener cannot unwind
the action that announced the event, nor stop the listeners after it. Delivery
is synchronous and in subscription order, so a slow handler is visible rather
than silently queued and lost on restart.

**Actor attribution** (`EventDef.Kind`). Required, because either default would
be wrong half the time. `auth.failed_login_spike` carries the username being
*guessed at* — a victim, not an actor — and before `Kind` existed the only
thing stopping a subscriber from counting failed logins against that member was
a comment. core refuses `system` + `Countable` for the same reason: there is no
member to total it against.

## Known limitations

Stated because a security document that lists only strengths is not useful.

- **The rebinding regression test is structural, not behavioural.** It asserts
  that `Dialer.Control` is installed. Proving the attack fails needs a lying
  nameserver, which is not stood up here — so the test catches the guard being
  removed, not every way it could be weakened.
- **`NewSafeFetch` does not restrict the scheme or the port.** A caller that
  passes a user-supplied `gopher://` or `file://` URL gets whatever
  `net/http` does with it. Validate the scheme before you hand a URL over.
- **The guard runs per dial, not per request.** Redirects are covered — each
  hop to a new host is a new dial and gets both checks — but a **pooled
  connection is reused without re-dialling**, so `Control` does not run again.
  If an allowed host's DNS changes to a blocked address while an idle
  connection to it is still in the pool, requests keep flowing over the
  existing connection until it is closed.
- **When an egress proxy is configured, the IP block does not apply.** It
  cannot: the address being dialled is the trusted internal proxy, and blocking
  private ranges would refuse it. Target enforcement moves to the proxy's
  firewall, which must be fail-closed. A misconfigured proxy is an open SSRF
  path with no warning from here.
- **No signed releases and no tagged versions.** Consumers pin by commit or by
  sibling `replace`. Worth having; not there yet.
- **Coverage is uneven** — 100% in `catalog`, 86% in `bencode`, 54% in `nntp`.
  CI reports it per package on every run, which is the point: the number is
  visible rather than claimed.
