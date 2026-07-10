// Package catalog is the domain-swap seam from
// FRAMEWORK-ARCHITECTURE.md §4: the MetadataSource interface that
// collapses AniDB / MangaDex / MusicBrainz / TMDB from bespoke
// sibling services into implementations of one contract, so a site
// built on this framework can index movies, games, or golf rounds
// by registering a different source — no host changes.
//
// Two axes stay orthogonal by design: MetadataSource says what a
// thing IS; DeliverySource (future) says how bytes arrive. "Anime
// over Usenet" and "movies over BitTorrent" differ only in which
// implementations are registered.
//
// Like pkg/core, this package has zero outbound dependency on
// pkg/models or pkg/storage — adapters translate at their own
// edge.
package catalog

import (
	"context"
	"strings"
	"unicode"
)

// EntityRef is the polymorphic catalog reference — the pair that
// replaces the hardcoded anime_id/manga_id/music_id/... ID-fan on
// releases and requests. Kind matches a registered DomainInfo.Key.
type EntityRef struct {
	Kind string
	ID   int64
}

// ExternalID is a namespaced foreign identifier ("imdb" →
// "tt0133093", "mal" → "5114") carried by a CatalogEntry and
// resolvable back to a local ID by sources that implement
// CrossIDResolver.
type ExternalID struct {
	Namespace string
	Value     string
}

// DomainInfo is a source's static identity.
type DomainInfo struct {
	// Key is the canonical short domain name ("anime", "movie",
	// "golf"). Lowercase, unique across registered sources; it is
	// the EntityRef.Kind namespace.
	Key string
	// UnitNoun names the domain's countable unit ("episode",
	// "film", "round") for UI copy like "12 episodes tracked".
	UnitNoun string
	// Priority orders sources when several could match the same
	// release name — higher wins. The reference instance runs
	// anime at 100 so an ambiguous title stays an anime match.
	Priority int
}

// CatalogEntry is the neutral projection of one catalog row —
// what the host needs to render a card, link a release, and feed
// search, with domain-specific extras in Fields.
type CatalogEntry struct {
	Ref       EntityRef
	Title     string
	AltTitles []string
	CoverURL  string
	Year      int
	Genres    []string
	External  []ExternalID
	// Fields carries domain extras (episode counts, studio,
	// rating). Consumers must treat it as display-only — typed
	// behaviour belongs on optional interfaces, not map peeking.
	Fields map[string]any
}

// Bucket is one completion-tracking unit (a season, an album, a
// tournament) for sources that implement CompletionProvider.
type Bucket struct {
	Key   string // stable per-entity bucket id ("s1", "round-3")
	Label string // display name ("Season 1")
	Total int    // expected units in the bucket; 0 = unknown
}

// MetadataSource is the required contract every catalog domain
// implements. Optional capabilities (CrossIDResolver,
// CompletionProvider) are feature-detected by type assertion so a
// minimal source stays minimal.
type MetadataSource interface {
	// Domain returns the source's static identity. Called at
	// registration; must be cheap and pure.
	Domain() DomainInfo

	// TitleIndex returns the normalized-title → local-id map that
	// feeds the shared TitleMatcher. Sources without a local
	// index yet (e.g. an API-only source before its export import
	// lands) return an empty map — the host degrades to no
	// title-matching for that domain rather than erroring.
	TitleIndex(ctx context.Context) (map[string]int64, error)

	// Fetch returns one entry by local id.
	Fetch(ctx context.Context, id int64) (CatalogEntry, error)

	// Normalize applies the domain's release-name cleaning policy
	// (the anime source folds sequel numbering; a movie source
	// keeps years). Feed the result to TitleIndex keys.
	Normalize(raw string) string
}

// TitleFinder is the optional live-matching capability. A source
// whose matching policy is richer than an exact index lookup —
// the anime source runs a 5-step matcher over a hot-reloaded
// titles dump — implements this and the host matcher delegates to
// it; sources without it get a plain normalized-map lookup built
// from TitleIndex.
type TitleFinder interface {
	FindByTitle(raw string) (int64, bool)
}

// CrossIDResolver is the optional capability for sources that can
// map a namespaced external id to a local id ("imdb"/"tt0133093"
// → 42). ok=false means unresolvable — the host degrades to text
// search.
type CrossIDResolver interface {
	ResolveExternalID(ctx context.Context, namespace, value string) (int64, bool)
}

// CompletionProvider is the optional capability for sources that
// can enumerate completion buckets for an entity.
type CompletionProvider interface {
	Buckets(ctx context.Context, id int64) ([]Bucket, error)
}

// DefaultNormalize is the domain-neutral release-name cleaner:
// lowercase, punctuation → space, whitespace collapsed. Sources
// with no special policy (movies, golf) use it directly; sources
// with one (anime sequel folding) wrap it.
func DefaultNormalize(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	lastSpace := true
	for _, r := range strings.ToLower(raw) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}
