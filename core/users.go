package core

import "context"

// UsersService is the read API every plugin uses to resolve
// users. The WRITE surface (Create, UpdateEmail, ChangePassword,
// password rotation, MFA enrolment, …) is core-internal and is
// intentionally NOT exposed here — those flows belong to the
// auth/account handlers that core owns.
//
// All methods are context-aware so plugins can propagate request
// deadlines and tracing handles down through the DB call.
//
// The concrete adapter lives in cmd/main.go and wraps the
// existing composite.Storage / models.User pair into core.User
// instances. See the doc comment on usersAdapter below.
type UsersService interface {
	// GetByID returns the user with the given primary-key ID.
	// Returns (nil, nil) when no row matches — plugins should
	// nil-check the return value rather than relying on a typed
	// "not found" error.
	GetByID(ctx context.Context, id int64) (*User, error)

	// GetByUsername is case-insensitive (matches the existing
	// CITEXT semantics on users.username). Same (nil, nil)
	// convention as GetByID for missing rows.
	GetByUsername(ctx context.Context, name string) (*User, error)

	// DisplayName returns the rendered username for the given
	// ID. Equivalent to `(GetByID().Username)` but cheaper since
	// the impl can hit a per-process cache. Returns "" when no
	// row matches — never errors on absence, only on DB faults.
	DisplayName(ctx context.Context, id int64) (string, error)

	// BulkDisplayNames resolves many IDs in one query. Used by
	// list-views that need to render alongside a join (forum
	// thread lists, wiki edit queues). The returned map omits
	// IDs that didn't match — the caller's "unknown user"
	// placeholder fills the gap.
	BulkDisplayNames(ctx context.Context, ids []int64) (map[int64]string, error)
}

// UsersAdapter is the function-bundle a host (cmd/main.go)
// passes into NewUsers to construct the live UsersService. We
// take callbacks rather than a *composite.Storage so this
// package keeps zero dependency on pkg/storage — the same
// pattern is used for every Core sub-service.
type UsersAdapter struct {
	GetByIDFn          func(ctx context.Context, id int64) (*User, error)
	GetByUsernameFn    func(ctx context.Context, name string) (*User, error)
	DisplayNameFn      func(ctx context.Context, id int64) (string, error)
	BulkDisplayNamesFn func(ctx context.Context, ids []int64) (map[int64]string, error)
}

// NewUsers constructs a UsersService from a UsersAdapter. Each
// nil callback degrades to a sensible default (returns nil/empty,
// never errors) so a partial wiring during incremental adoption
// doesn't crash plugin code that holds a Core reference.
func NewUsers(a UsersAdapter) UsersService { return &usersAdapter{a: a} }

type usersAdapter struct{ a UsersAdapter }

func (u *usersAdapter) GetByID(ctx context.Context, id int64) (*User, error) {
	if u.a.GetByIDFn == nil {
		return nil, nil
	}
	return u.a.GetByIDFn(ctx, id)
}

func (u *usersAdapter) GetByUsername(ctx context.Context, name string) (*User, error) {
	if u.a.GetByUsernameFn == nil {
		return nil, nil
	}
	return u.a.GetByUsernameFn(ctx, name)
}

func (u *usersAdapter) DisplayName(ctx context.Context, id int64) (string, error) {
	if u.a.DisplayNameFn == nil {
		return "", nil
	}
	return u.a.DisplayNameFn(ctx, id)
}

func (u *usersAdapter) BulkDisplayNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	if u.a.BulkDisplayNamesFn == nil {
		return map[int64]string{}, nil
	}
	return u.a.BulkDisplayNamesFn(ctx, ids)
}
