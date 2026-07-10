package core

// RBACService is the role-check facade. Plugins call methods
// here rather than comparing Role ints directly so the enum stays
// opaque — if we ever add a new role tier between Mod and Admin,
// every plugin's "is the actor at least a moderator" check still
// gives the right answer.
//
// Most checks (`u.AtLeast(RoleMod)`) can also be done on the
// User type directly; this interface is here for symmetry with
// the design doc and for the rare cases where a check happens
// in middleware that doesn't have a *User in hand.
type RBACService interface {
	// AtLeast returns true if u's role is >= minRole. Returns
	// false for a nil user — anonymous requests never satisfy a
	// role gate.
	AtLeast(u *User, minRole Role) bool

	// IsAdmin is a sugar form of AtLeast(u, RoleAdmin).
	IsAdmin(u *User) bool

	// IsMod is a sugar form of AtLeast(u, RoleMod). Note:
	// admins ARE mods under this check (RoleAdmin > RoleMod) —
	// use AtLeast(u, RoleMod) && !IsAdmin(u) for "strictly a
	// mod, not an admin".
	IsMod(u *User) bool
}

// NewRBAC returns the default RBACService implementation. The
// rules live in this package (rather than in an adapter) because
// they are pure comparisons against the Role enum which itself is
// owned by this package — there is no external state to wire in.
func NewRBAC() RBACService { return defaultRBAC{} }

type defaultRBAC struct{}

func (defaultRBAC) AtLeast(u *User, minRole Role) bool {
	if u == nil {
		return false
	}
	return u.Role >= minRole
}

func (defaultRBAC) IsAdmin(u *User) bool { return defaultRBAC{}.AtLeast(u, RoleAdmin) }
func (defaultRBAC) IsMod(u *User) bool   { return defaultRBAC{}.AtLeast(u, RoleMod) }
