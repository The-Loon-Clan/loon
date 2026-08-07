package core

import "testing"

// A host that never published a posture must read as CLOSED, not as the zero
// string. Both failure directions matter and they are not symmetric: a site
// that cannot register anybody is noticed within minutes, and a site quietly
// serving every page to anonymous crawlers is not.
func TestUnsetAccessIsClosedNotEmpty(t *testing.T) {
	c := &Core{}
	a := c.Access()
	if a.Viewing != ViewingMembers {
		t.Errorf("viewing = %q, want %q — an unconfigured host must not browse publicly", a.Viewing, ViewingMembers)
	}
	if a.Registration != RegistrationClosed {
		t.Errorf("registration = %q, want %q", a.Registration, RegistrationClosed)
	}
	if a.PublicBrowsing() {
		t.Error("PublicBrowsing() on an unconfigured host")
	}
	if a.SignupAllowed() {
		t.Error("SignupAllowed() on an unconfigured host")
	}
	if a.Indexable {
		t.Error("Indexable on an unconfigured host")
	}
}

// A bad mode must be refused at the setter rather than silently becoming the
// default. Falling through to closed would lock out a site whose operator
// believes it is open; falling through the other way, after some future
// rename, would reopen registration with nothing to say so.
func TestBadModesAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    Access
	}{
		{"typo'd registration", Access{Registration: "inviteonly", Viewing: ViewingPublic}},
		{"typo'd viewing", Access{Registration: RegistrationOpen, Viewing: "members"}},
		{"empty", Access{}},
	} {
		c := &Core{}
		if err := c.SetAccess(tc.a); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
		// A refused write must not have taken effect.
		if c.Access().Viewing != ViewingMembers {
			t.Errorf("%s: a rejected SetAccess still changed the posture", tc.name)
		}
	}
}

// The wire values are the strings the site's settings table already holds.
// Changing one silently reinterprets every existing row: a stored "invite_only"
// would stop matching, fail Validate, and take the site to closed — so this
// pins them.
func TestWireValuesMatchThePersistedStrings(t *testing.T) {
	// A slice, not a map: the two enums share the string "closed", so a map
	// literal here does not compile. That is the collision below, discovered
	// by writing this test — and a decent argument that the separate types are
	// pulling their weight.
	for _, tc := range []struct{ got, want, what string }{
		{string(RegistrationOpen), "open", "RegistrationOpen"},
		{string(RegistrationInvite), "invite_only", "RegistrationInvite"},
		{string(RegistrationClosed), "closed", "RegistrationClosed"},
		{string(ViewingMembers), "closed", "ViewingMembers"},
		{string(ViewingPublic), "public", "ViewingPublic"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q; existing settings rows would stop matching",
				tc.what, tc.got, tc.want)
		}
	}
	// "closed" deliberately means different things in the two enums. Separate
	// types are what keep them apart, so this documents the collision rather
	// than treating it as a bug to be tidied away.
	if string(RegistrationClosed) != string(ViewingMembers) {
		t.Skip("the enums no longer share the string; the comment in access.go needs updating")
	}
}

func TestAccessRoundTrips(t *testing.T) {
	c := &Core{}
	want := Access{Registration: RegistrationInvite, Viewing: ViewingPublic, Indexable: true}
	if err := c.SetAccess(want); err != nil {
		t.Fatal(err)
	}
	got := c.Access()
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if !got.InvitesRequired() || !got.PublicBrowsing() || !got.SignupAllowed() {
		t.Errorf("helpers disagree with the fields: %+v", got)
	}
}
