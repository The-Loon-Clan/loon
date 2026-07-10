package catalog

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is the typed facade over "which catalog domains does
// this instance run". The composition root constructs one,
// registers its sources, and publishes it on the Core extension
// registry under RegistryExtension so plugins and host surfaces
// resolve it uniformly.
type Registry struct {
	mu      sync.RWMutex
	sources map[string]MetadataSource
}

// RegistryExtension is the core extension-registry name the host
// publishes the Registry under.
const RegistryExtension = "catalog.registry"

func NewRegistry() *Registry {
	return &Registry{sources: map[string]MetadataSource{}}
}

// RegisterSource adds a source. Errors on a nil source, an empty
// Domain().Key, or a duplicate key — composition-root bugs that
// should fail boot loudly.
func (r *Registry) RegisterSource(s MetadataSource) error {
	if s == nil {
		return fmt.Errorf("catalog: RegisterSource called with nil source")
	}
	key := s.Domain().Key
	if key == "" {
		return fmt.Errorf("catalog: source has empty Domain().Key")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.sources[key]; dup {
		return fmt.Errorf("catalog: source %q registered twice", key)
	}
	r.sources[key] = s
	return nil
}

// Sources returns every registered source ordered by Priority
// descending (ties break alphabetically by Key for determinism).
func (r *Registry) Sources() []MetadataSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]MetadataSource, 0, len(r.sources))
	for _, s := range r.sources {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := out[i].Domain(), out[j].Domain()
		if di.Priority != dj.Priority {
			return di.Priority > dj.Priority
		}
		return di.Key < dj.Key
	})
	return out
}

// ByKey returns the source registered for a domain key.
func (r *Registry) ByKey(key string) (MetadataSource, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sources[key]
	return s, ok
}
