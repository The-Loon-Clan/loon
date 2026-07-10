package core

import (
	"context"
	"errors"
)

// PointsService is the points-ledger facade. Plugins MUST go
// through this interface for any award / deduct / refund so
// that:
//
//   - The ledger row is written atomically with the user-balance
//     update by the underlying impl.
//   - The reason lands in the typed points_ledger.type catalog
//     (models.LedgerEntryType) so the admin Points Log and the
//     reputation-tier queries see plugin activity like any other
//     economy event.
//
// reason is the ledger TYPE — a stable catalog value that MUST
// carry the verb's prefix: "earn_*" for Award, "spend_*" for
// Deduct, "refund_*" for Refund. (The earned reputation tier sums
// `type LIKE 'earn_%'`, and refunds deliberately do NOT count as
// income — see models.LedgerEntryType.) A mis-prefixed reason is
// not an error: the host maps it onto the generic earn_plugin /
// spend_plugin / refund_plugin types and moves the reason into
// the description, so the ledger stays consistent while the bug
// stays visible in the log.
//
// detail is the free-form description column ("Applied to /c/
// anime"). May be empty. ref is an optional points_ledger
// reference_id linking the row to a domain entity (request id,
// invoice id); pass 0 for none.
//
// Mutating methods return the user's new balance to make "award N
// points and tell the user the running total" a one-call API.
type PointsService interface {
	// Balance returns the user's current points balance. 0 for
	// unknown users.
	Balance(ctx context.Context, userID int64) (int, error)

	// Award credits N points. N must be > 0; otherwise the call
	// is a no-op and the existing balance is returned.
	Award(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error)

	// Deduct debits N points. The impl refuses to take a user
	// negative, returning ErrInsufficientPoints; plugins should
	// treat that as a normal business-logic outcome rather than
	// an internal error.
	Deduct(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error)

	// Refund credits back a previously-deducted amount (escrow
	// returns, cancelled purchases). Ledger type carries the
	// refund_ prefix so the credit never inflates the earned
	// reputation tier.
	Refund(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error)
}

// ErrInsufficientPoints is returned by Deduct when the user's
// balance would go negative. Plugins should compare against this
// sentinel with errors.Is.
var ErrInsufficientPoints = errors.New("core: insufficient points")

// PointsAdapter is the function-bundle the host hands to
// NewPoints. All callbacks may be nil — in that case the service
// returns ErrPointsNotWired from every method so plugins that
// mistakenly rely on points before the real impl lands fail
// loudly rather than silently no-op.
type PointsAdapter struct {
	BalanceFn func(ctx context.Context, userID int64) (int, error)
	AwardFn   func(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error)
	DeductFn  func(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error)
	RefundFn  func(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error)
}

// ErrPointsNotWired indicates the points subsystem was not
// configured at boot. Returned by every method on the
// pointsAdapter when the corresponding callback is nil — see
// the doc on PointsAdapter for why this is loud rather than a
// no-op.
var ErrPointsNotWired = errors.New("core: PointsService not wired — call NewPoints with a non-nil adapter")

// NewPoints constructs a PointsService from the given adapter.
func NewPoints(a PointsAdapter) PointsService { return &pointsAdapter{a: a} }

type pointsAdapter struct{ a PointsAdapter }

func (p *pointsAdapter) Balance(ctx context.Context, userID int64) (int, error) {
	if p.a.BalanceFn == nil {
		return 0, ErrPointsNotWired
	}
	return p.a.BalanceFn(ctx, userID)
}

func (p *pointsAdapter) Award(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error) {
	if p.a.AwardFn == nil {
		return 0, ErrPointsNotWired
	}
	if n <= 0 {
		return 0, nil
	}
	return p.a.AwardFn(ctx, userID, n, reason, detail, ref)
}

func (p *pointsAdapter) Deduct(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error) {
	if p.a.DeductFn == nil {
		return 0, ErrPointsNotWired
	}
	if n <= 0 {
		return 0, nil
	}
	return p.a.DeductFn(ctx, userID, n, reason, detail, ref)
}

func (p *pointsAdapter) Refund(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error) {
	if p.a.RefundFn == nil {
		return 0, ErrPointsNotWired
	}
	if n <= 0 {
		return 0, nil
	}
	return p.a.RefundFn(ctx, userID, n, reason, detail, ref)
}
