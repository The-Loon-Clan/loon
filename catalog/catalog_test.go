package catalog

import (
	"context"
	"testing"
)

type fakeSource struct {
	info DomainInfo
}

func (f fakeSource) Domain() DomainInfo { return f.info }
func (f fakeSource) TitleIndex(context.Context) (map[string]int64, error) {
	return map[string]int64{}, nil
}
func (f fakeSource) Fetch(_ context.Context, id int64) (CatalogEntry, error) {
	return CatalogEntry{Ref: EntityRef{Kind: f.info.Key, ID: id}}, nil
}
func (f fakeSource) Normalize(raw string) string { return DefaultNormalize(raw) }

func TestRegistry_RegisterAndOrder(t *testing.T) {
	r := NewRegistry()
	for _, s := range []fakeSource{
		{DomainInfo{Key: "movie", UnitNoun: "film", Priority: 50}},
		{DomainInfo{Key: "anime", UnitNoun: "episode", Priority: 100}},
		{DomainInfo{Key: "golf", UnitNoun: "round", Priority: 50}},
	} {
		if err := r.RegisterSource(s); err != nil {
			t.Fatalf("register %s: %v", s.info.Key, err)
		}
	}
	got := r.Sources()
	want := []string{"anime", "golf", "movie"} // 100, then 50s alphabetical
	for i, s := range got {
		if s.Domain().Key != want[i] {
			t.Fatalf("order[%d] = %s, want %s", i, s.Domain().Key, want[i])
		}
	}
	if _, ok := r.ByKey("anime"); !ok {
		t.Error("ByKey(anime) missing")
	}
	if _, ok := r.ByKey("nope"); ok {
		t.Error("ByKey(nope) should miss")
	}
}

func TestRegistry_Guards(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterSource(nil); err == nil {
		t.Error("nil source should fail")
	}
	if err := r.RegisterSource(fakeSource{DomainInfo{}}); err == nil {
		t.Error("empty key should fail")
	}
	_ = r.RegisterSource(fakeSource{DomainInfo{Key: "anime"}})
	if err := r.RegisterSource(fakeSource{DomainInfo{Key: "anime"}}); err == nil {
		t.Error("duplicate key should fail")
	}
}

func TestDefaultNormalize(t *testing.T) {
	cases := map[string]string{
		"The.Matrix.1999.1080p.x265-GRP": "the matrix 1999 1080p x265 grp",
		"  PGA Championship: Round 3! ":  "pga championship round 3",
		"already clean":                  "already clean",
		"":                               "",
	}
	for in, want := range cases {
		if got := DefaultNormalize(in); got != want {
			t.Errorf("DefaultNormalize(%q) = %q, want %q", in, got, want)
		}
	}
}
