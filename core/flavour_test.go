package core

import "testing"

// TestPluginSuitsFlavour pins the three rules that make this usable, all of
// which are about NOT having to think about "both".
func TestPluginSuitsFlavour(t *testing.T) {
	indexerOnly := Metadata{Name: "usenet", Flavours: []string{FlavourIndexer}}
	trackerOnly := Metadata{Name: "hitrun", Flavours: []string{FlavourTracker}}
	either := Metadata{Name: "forum"}
	anyDeclared := Metadata{Name: "store", Flavours: []string{FlavourAny}}
	both := Metadata{Name: "search", Flavours: []string{FlavourIndexer, FlavourTracker}}

	indexerSite := []string{FlavourIndexer}
	trackerSite := []string{FlavourTracker}
	bothSite := []string{FlavourIndexer, FlavourTracker}

	cases := []struct {
		what string
		md   Metadata
		site []string
		want bool
	}{
		// A plugin that declares nothing runs everywhere. This is the common
		// case and the reason the field is opt-in: a forum does not care what
		// the site indexes.
		{"undeclared on an indexer", either, indexerSite, true},
		{"undeclared on a tracker", either, trackerSite, true},

		// The same answer, said out loud. Both run everywhere; the difference
		// is that one of them has been decided and the other has only been
		// left alone, and only a declaration can tell a reviewer which.
		{"any on an indexer", anyDeclared, indexerSite, true},
		{"any on a tracker", anyDeclared, trackerSite, true},
		{"any on a both site", anyDeclared, bothSite, true},

		{"indexer plugin on an indexer site", indexerOnly, indexerSite, true},
		{"indexer plugin on a tracker site", indexerOnly, trackerSite, false},
		{"tracker plugin on a tracker site", trackerOnly, trackerSite, true},
		{"tracker plugin on an indexer site", trackerOnly, indexerSite, false},

		// "Both" is not a special case anywhere — it is the two-element set,
		// and every one-sided plugin matches it on one element. Treating it as
		// a third value is how every caller grows a three-way switch.
		{"indexer plugin on a both site", indexerOnly, bothSite, true},
		{"tracker plugin on a both site", trackerOnly, bothSite, true},
		{"two-sided plugin on an indexer site", both, indexerSite, true},
		{"two-sided plugin on a tracker site", both, trackerSite, true},
	}
	for _, c := range cases {
		if got := pluginSuitsFlavour(c.md, c.site); got != c.want {
			t.Errorf("%s: got %v, want %v", c.what, got, c.want)
		}
	}
}

// TestNoSiteFlavourKeepsEverything is the compatibility promise, and it is the
// one that matters most in a shared core: a host that has never heard of
// flavours must keep every plugin it had. The alternative is a field arriving
// in somebody else's build and silently switching off half their site.
func TestNoSiteFlavourKeepsEverything(t *testing.T) {
	for _, md := range []Metadata{
		{Name: "usenet", Flavours: []string{FlavourIndexer}},
		{Name: "hitrun", Flavours: []string{FlavourTracker}},
		{Name: "forum"},
	} {
		if !pluginSuitsFlavour(md, nil) {
			t.Errorf("%s was skipped on a host that declared no flavours", md.Name)
		}
	}
}

// TestUnknownFlavourSkipsDeclaredPlugins. A host naming a half nobody
// implements keeps the undeclared plugins and drops the declared ones, which
// is the fail-closed direction: a typo in a setting must not silently mount a
// tracker's announce routes.
func TestUnknownFlavourSkipsDeclaredPlugins(t *testing.T) {
	site := []string{"wiki"}
	if pluginSuitsFlavour(Metadata{Flavours: []string{FlavourTracker}}, site) {
		t.Error("a tracker plugin ran on a site that never asked for a tracker")
	}
	if !pluginSuitsFlavour(Metadata{}, site) {
		t.Error("an undeclared plugin was dropped by an unknown flavour")
	}
}
