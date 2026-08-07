package core

import (
	"fmt"
	"sync/atomic"
)

// Site access modes: who may create an account, and who may read the site.
//
// These were host settings on the site that grew them, and they moved here for
// one reason: core already owns HALF of this seam and could not see the other
// half. AuthService.Authenticate() is documented as enforcing "the site's
// access mode", and View carries Public and MinRole so a plugin declares who
// may see its page — but nothing let a plugin, or core itself, ask what the
// mode currently IS. A plugin deciding what to show an anonymous visitor, or
// gating registration behind invites, had to reach into the host.
//
// They are in core rather than loon-baseline deliberately. Baseline is
// optional — a host can run loon without it — so a mode that lives there is a
// mode plugins cannot read. The ENFORCEMENT still belongs where the action is:
// baseline's authflow wraps Register and consults RegistrationMode; the host's
// middleware enforces ViewingMode. Core holds the answer, not the policing.

// RegistrationMode says who may create an account.
type RegistrationMode string

const (
	// RegistrationOpen — anyone may sign up.
	RegistrationOpen RegistrationMode = "open"
	// RegistrationInvite — an invite code is required.
	RegistrationInvite RegistrationMode = "invite_only"
	// RegistrationClosed — nobody may sign up; an admin creates accounts.
	RegistrationClosed RegistrationMode = "closed"
)

// ViewingMode says who may read the site's pages.
type ViewingMode string

const (
	// ViewingMembers — every page requires a login ("closed" on the wire).
	ViewingMembers ViewingMode = "closed"
	// ViewingPublic — anonymous visitors may browse public pages; writes and
	// member-scoped pages still require a login, and search engines can reach
	// whatever is left open.
	ViewingPublic ViewingMode = "public"
)

// The wire values are the strings the settings table already holds, so
// adopting this needs no migration and no rewrite of existing rows. It does
// mean "closed" appears in BOTH enums meaning different things — closed
// registration and members-only viewing. They are separate types precisely so
// the compiler keeps them apart; do not collapse them into one string.

// Access is the site's current access posture. Read it with Core.Access(),
// which is cheap enough to call per render.
type Access struct {
	Registration RegistrationMode
	Viewing      ViewingMode

	// Indexable mirrors the host's SEO switch: when true the site emits
	// sitemaps, Open Graph tags and a permissive robots.txt.
	//
	// It is NOT an access control and must never be used as one. robots.txt is
	// a request, honoured by the search engines that choose to and ignored by
	// everything else — and a page an anonymous visitor can fetch is public
	// whatever this says. Viewing is the boundary; Indexable only decides
	// whether we advertise.
	Indexable bool
}

// PublicBrowsing reports whether anonymous visitors may read public pages.
func (a Access) PublicBrowsing() bool { return a.Viewing == ViewingPublic }

// InvitesRequired reports whether signing up needs an invite code.
func (a Access) InvitesRequired() bool { return a.Registration == RegistrationInvite }

// SignupAllowed reports whether a visitor can create an account at all, with
// or without a code. False means an admin has to do it.
func (a Access) SignupAllowed() bool { return a.Registration != RegistrationClosed }

// Validate rejects a mode string that is not one of the known values.
//
// Worth failing on rather than defaulting: a typo in a config file or a stray
// value in the settings table would otherwise fall through to the closed
// default and lock every visitor out of a site whose operator believes it is
// public — or, in the direction that actually matters, a future rename could
// silently reopen registration.
func (a Access) Validate() error {
	switch a.Registration {
	case RegistrationOpen, RegistrationInvite, RegistrationClosed:
	default:
		return fmt.Errorf("core: registration mode %q is not one of open, invite_only, closed", a.Registration)
	}
	switch a.Viewing {
	case ViewingMembers, ViewingPublic:
	default:
		return fmt.Errorf("core: viewing mode %q is not one of closed, public", a.Viewing)
	}
	return nil
}

// accessState is the atomically-swapped holder. A pointer swap rather than a
// mutex because Access is read on nearly every render — the host's own version
// of this was an atomic for the same reason — and written only at boot and when
// an operator flips a toggle.
type accessState struct {
	atomic.Pointer[Access]
}

// Access returns the site's current posture.
//
// A host that has never called SetAccess gets the CLOSED answer for both:
// members-only viewing, no registration, not indexable. That is deliberate and
// it is the safe direction. A host that forgets to load its settings and
// therefore cannot register anybody has a loud, immediate, obvious bug; a host
// that forgets and silently serves the whole site to anonymous crawlers has a
// quiet one it may not notice for months.
func (c *Core) Access() Access {
	if a := c.access.Load(); a != nil {
		return *a
	}
	return Access{Registration: RegistrationClosed, Viewing: ViewingMembers}
}

// SetAccess publishes the posture. The host calls it once at boot from
// persisted settings, and again whenever an operator changes one — the change
// then applies to the very next request without a restart, which is how the
// site's own toggles already behave.
//
// Core does not persist this. The settings table is the host's, and a
// framework that wrote to it would need to know its shape.
func (c *Core) SetAccess(a Access) error {
	if err := a.Validate(); err != nil {
		return err
	}
	c.access.Store(&a)
	return nil
}
