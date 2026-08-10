package core

import "context"

// Site state: what the whole site is currently willing to do.
//
// The problem this solves is not a banner. A site that must stop accepting
// WRITES while still serving READS is what lets a database migration run against
// a live source — the old cluster answers queries while the new one is built
// beside it — which turns the dangerous part of a migration from a timed outage
// into an operation you can abandon and retry for free. That property is worth
// more than any amount of shaving the window: three failed upgrade attempts on
// 2026-08-10 cost eleven minutes of downtime each, and under read-only they would
// have cost nothing.
//
// It is on Core rather than being each plugin's business because the expensive
// part is the jobs, not the routes. A crawler that keeps writing during a dump is
// silent divergence: the dump's snapshot is taken at its start, so anything
// committed afterwards is lost at cutover without a word. Every plugin author
// remembering to check is not a mechanism; a gate the scheduler consults is.
// See schedule.WriteGate and JobInfo.MarkWrites.

// SiteMode is what the site is currently willing to do. Closed set, compared
// against constants and never against string literals: a mistyped literal in a
// comparison silently takes the wrong branch, and the wrong branch here means
// accepting writes during a migration.
type SiteMode string

const (
	// SiteNormal is everything working. The default, and what an absent
	// SiteState implementation must report.
	SiteNormal SiteMode = "normal"

	// SiteReadOnly serves reads and refuses writes. The site is UP: members
	// browse, search and download normally, and the pages that would change
	// something say why they cannot.
	SiteReadOnly SiteMode = "read-only"

	// SiteMaintenance is the existing all-stop, where even reads are refused.
	// Distinct from read-only on purpose: conflating them is how "we are doing
	// maintenance" came to mean "the site is gone", which is exactly the habit
	// read-only exists to break.
	SiteMaintenance SiteMode = "maintenance"
)

// Writable reports whether this mode accepts writes. A helper rather than a
// comparison at each call site, so adding a fourth mode later cannot leave a
// stale `== SiteReadOnly` test somewhere quietly allowing writes.
func (m SiteMode) Writable() bool { return m == SiteNormal || m == "" }

// SiteStateService is how a plugin asks what the site is currently willing to do.
//
// FAIL OPEN is the contract, and it is not a suggestion. Callers use this to
// decide whether to attempt a write, and a caller that cannot reach the answer
// must behave as though writes are allowed rather than break the request:
// several read paths write as a side effect (a download records a grab, an API
// call records a request, an agent poll stamps last_seen), so a service that
// turned an unavailable mode into an error would take the site down in the mode
// meant to keep it up. Hence Mode has no error return — an implementation that
// cannot determine the mode reports SiteNormal.
type SiteStateService interface {
	// Mode is the current site mode. Cheap enough to call per request: hosts
	// are expected to serve it from memory and refresh in the background,
	// exactly as the existing maintenance flag does.
	Mode(ctx context.Context) SiteMode

	// Reason is operator-supplied text for why, shown to members in the banner
	// ("upgrading the database, back shortly"). Empty is fine and the UI must
	// cope: a mode with no explanation is still a mode.
	Reason(ctx context.Context) string
}

// SiteStateOf reads the mode from a Core, tolerating a Core that has no
// SiteState wired.
//
// Every plugin would otherwise write the same nil-check, and the ones that forgot
// would panic in the mode that exists to avoid an outage. A host that has not
// adopted site state reports SiteNormal, which is both the truth for that host
// and the fail-open answer.
func SiteStateOf(ctx context.Context, c *Core) SiteMode {
	if c == nil || c.SiteState == nil {
		return SiteNormal
	}
	return c.SiteState.Mode(ctx)
}

// SiteWritable is the question almost every caller actually has: may I write?
func SiteWritable(ctx context.Context, c *Core) bool {
	return SiteStateOf(ctx, c).Writable()
}
