package core

import (
	"testing"
)

// A capability nobody registered is recorded rather than lost.
//
// This is the quietest failure the plugin architecture has: a consumer that
// cannot find its provider degrades to doing nothing, and doing nothing looks
// exactly like having nothing to do. So the feature is absent, no error is
// raised, and it is found weeks later by somebody noticing it never worked.
// Five times, across different plugins, before this existed.
//
// The registry is the right place to count it because it is the only place
// that knows the answer for every plugin at once — the alternative is each
// plugin remembering to report its own misses, which is the arrangement that
// produced the five.
func TestLookupRecordsMisses(t *testing.T) {
	c := &Core{}

	if _, ok := c.Lookup("usenet.nfostore"); ok {
		t.Fatal("found a service in an empty registry")
	}
	c.Lookup("usenet.nfostore") // asked twice
	c.Lookup("some.other.cap")

	missing := c.MissingExtensions()
	if missing["usenet.nfostore"] != 2 {
		t.Errorf("usenet.nfostore counted %d times, want 2", missing["usenet.nfostore"])
	}
	if missing["some.other.cap"] != 1 {
		t.Errorf("some.other.cap counted %d times, want 1", missing["some.other.cap"])
	}
}

// A capability that IS registered must not be reported as missing — otherwise
// the boot report cries wolf and stops being read, which is worse than not
// having it.
func TestLookupDoesNotRecordHits(t *testing.T) {
	c := &Core{}
	if err := c.Register("present.cap", struct{}{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := c.Lookup("present.cap"); !ok {
		t.Fatal("registered service not found")
	}
	if n := c.MissingExtensions()["present.cap"]; n != 0 {
		t.Errorf("a registered capability was counted as missing %d times", n)
	}
}

// The returned map is a copy: a caller iterating it while another goroutine
// looks something up must not race, and must not be able to edit the registry's
// own bookkeeping.
func TestMissingExtensionsReturnsACopy(t *testing.T) {
	c := &Core{}
	c.Lookup("gone")
	m := c.MissingExtensions()
	m["gone"] = 99
	m["invented"] = 1
	again := c.MissingExtensions()
	if again["gone"] != 1 {
		t.Errorf("mutating the returned map changed the registry (gone=%d)", again["gone"])
	}
	if _, ok := again["invented"]; ok {
		t.Error("mutating the returned map added an entry to the registry")
	}
}
