package core

import (
	"fmt"
	"sort"
	"strings"
)

// Feature flags: switching a capability off on a running site.
//
// The site FLAVOUR (Metadata.Flavours) answers "what kind of site is this" and
// answers it at boot, because the things it governs — a crawler, an announce
// endpoint, ratio accounting — cannot be half-started. This is the other axis:
// a capability the site HAS and the operator does not want on today, decided
// per request, no restart.
//
// WHY IT IS NOT JUST A CONFIG KEY. Every plugin can already read its own
// config, and several do, which is exactly the problem: the flags are invisible.
// Nothing lists them, nothing says what turning one off actually stops, and an
// operator cannot find the switch without reading the source of the plugin they
// want to switch off. A flag nobody can find is a flag nobody uses, and the
// feature ships permanently on.
//
// So a feature DECLARES itself the way a view or a widget does. The declaration
// carries what it is called and what stops working, which is the part an
// operator is actually deciding about — "comments.thanks" tells them nothing,
// "members can thank a comment; the points already awarded are kept" tells them
// everything.
//
// FAIL ON, always. A host that has not adopted features, a key nobody
// registered, an unreachable store — every one of them answers "on". The
// alternative is a feature silently vanishing because a lookup failed, and the
// first anybody knows is a member asking where the button went. A flag that
// fails closed is worse than no flag: at least without one, the code is
// obviously always on.

// Feature is one switchable capability.
type Feature struct {
	// Key is the stable id, namespaced by the plugin that owns it —
	// "comments.thanks", "mediainfo.screenshots". Stored against the site, so
	// renaming one resets whatever the operator decided.
	Key string

	// Title is what the admin page calls it.
	Title string

	// Description says what turning it OFF stops, in the operator's terms
	// rather than the code's. This is the field that makes the page usable:
	// somebody deciding whether to switch something off needs to know what
	// breaks, and "thanks" does not tell them whether the points already
	// awarded are clawed back.
	Description string

	// Default is whether the feature is on for a site that has never decided.
	// Almost always true — a feature shipping off by default is one nobody
	// discovers.
	Default bool
}

// Namespace is the part of the key before the first dot — the plugin that owns
// it, by the convention keys follow. Derived rather than stored, because core
// has no notion of which plugin is provisioning and inventing one to fill in a
// display field would be a lot of machinery for a grouping header.
func (f Feature) Namespace() string {
	if i := strings.Index(f.Key, "."); i > 0 {
		return f.Key[:i]
	}
	return f.Key
}

// FeatureService is how a HOST answers whether a feature is on.
//
// Two returns rather than one, because "off" and "no opinion" are different
// answers and collapsing them loses the registered default: a host that has
// never been asked about a feature must fall back to what the plugin shipped,
// not to false.
//
// Expected to be served from memory. This is called per request, sometimes
// several times per page, and a host that reaches a database for each one has
// built a slow way to render the same page.
type FeatureService interface {
	FeatureEnabled(key string) (on bool, decided bool)
}

// RegisterFeature declares a switchable capability.
//
// Called from Provision, like RegisterView and RegisterWidget. A duplicate key
// is an error rather than a silent overwrite: two plugins claiming one switch
// means an operator toggling one of them and surprising the other.
func (c *Core) RegisterFeature(f Feature) error {
	if c == nil {
		return fmt.Errorf("core: RegisterFeature on a nil Core")
	}
	if f.Key == "" || f.Title == "" {
		return fmt.Errorf("core: RegisterFeature requires Key and Title (got %q/%q)", f.Key, f.Title)
	}
	c.featMu.Lock()
	defer c.featMu.Unlock()
	if c.features == nil {
		c.features = map[string]Feature{}
	}
	if _, dup := c.features[f.Key]; dup {
		return fmt.Errorf("core: feature %q registered twice", f.Key)
	}
	c.features[f.Key] = f
	return nil
}

// Features is the catalogue, ordered by key so an admin page does not
// reshuffle between loads.
func (c *Core) Features() []Feature {
	if c == nil {
		return nil
	}
	c.featMu.Lock()
	defer c.featMu.Unlock()
	out := make([]Feature, 0, len(c.features))
	for _, f := range c.features {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// FeatureByKey resolves one declaration.
func (c *Core) FeatureByKey(key string) (Feature, bool) {
	if c == nil {
		return Feature{}, false
	}
	c.featMu.Lock()
	defer c.featMu.Unlock()
	f, ok := c.features[key]
	return f, ok
}

// FeatureOn is the question every caller actually has.
//
// The order is: what the host decided, then what the plugin declared, then on.
// An empty key is on — a View or Widget that names no feature is not gated by
// one, and that is the common case by a distance.
func FeatureOn(c *Core, key string) bool {
	if key == "" {
		return true
	}
	if c == nil {
		return true
	}
	if c.FeatureState != nil {
		if on, decided := c.FeatureState.FeatureEnabled(key); decided {
			return on
		}
	}
	if f, ok := c.FeatureByKey(key); ok {
		return f.Default
	}
	// Registered by nobody. ON, because the alternative is a typo in a check
	// switching a feature off across the site with nothing to say so — and a
	// declared-but-unchecked feature is findable (it is on the admin page and
	// does nothing), while an undeclared-but-checked one is not.
	return true
}
