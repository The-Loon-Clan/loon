package core

import "time"

// This file declares the SMALL, plugin-facing shadow types the
// mediator surfaces from Core. They intentionally re-declare a
// trimmed-down shape of pkg/models so that pkg/core has no
// outbound dependency on pkg/models (which itself pulls in
// bcrypt, hmac, sha256, etc.). Adapters in cmd/main.go translate
// between the live *models.User and the public core.User.
//
// Keep these types SMALL. A field that no plugin actually reads
// should not be exposed here — every field is a one-way ratchet
// once shipped (see PLUGIN-ARCHITECTURE.md § 14).

// Role is the plugin-facing role enum. The integer values mirror
// pkg/models.RoleLevel verbatim so the adapter is a trivial
// numeric cast, but plugins MUST use the named constants below
// rather than the raw integer — the enum is otherwise opaque.
type Role int

const (
	RoleBanned      Role = -2
	RoleDisabled    Role = -1
	RoleUser        Role = 0
	RoleContributor Role = 1
	RoleMod         Role = 2
	RoleAdmin       Role = 3
)

// User is the read-only projection of a user that plugins see.
// The fields are intentionally minimal: ID for FKs, Username for
// display, Email for transactional mail (decrypted), Role for
// permission checks, CreatedAt for "joined N days ago" UI.
//
// Anything plugin-specific (Points, TOTP, password hashes,
// invite counts, etc.) is INTENTIONALLY excluded — those fields
// either belong to a different service (PointsService) or are
// core-internal (auth surface only).
type User struct {
	ID        int64
	Username  string
	Email     string
	Role      Role
	CreatedAt time.Time
}

// AtLeast returns true if the user's role meets or exceeds the
// given level. Provided as a method so plugins can write
// `u.AtLeast(core.RoleMod)` without dereferencing a separate
// RBAC service for every check. The RBACService facade still
// exists for the "no User in hand" case (e.g. inside
// middleware) and for consistency with the design document.
func (u *User) AtLeast(r Role) bool {
	if u == nil {
		return false
	}
	return u.Role >= r
}
