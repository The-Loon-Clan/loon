package core

import (
	"encoding/json"
	"fmt"
)

// ConfigService is the typed per-plugin config accessor.
//
// Plugin config lives under the top-level "plugins" key in
// config.yml (matching PLUGIN-ARCHITECTURE.md § 10):
//
//	plugins:
//	  wiki:
//	    review_quorum: 1
//	    points_per_field_approve: 5
//	  forum:
//	    new_thread_min_role: user
//
// PluginInto is the canonical entry point — plugins declare a
// struct with mapstructure tags and unmarshal directly into it.
// Plugin() is the escape hatch for fully dynamic config (key/
// value pairs whose names aren't known until the plugin runs).
type ConfigService interface {
	// Plugin returns the raw sub-map keyed by plugin name, or
	// an empty (non-nil) map if no config section exists.
	// Mutating the returned map is undefined — treat it as
	// read-only.
	Plugin(name string) map[string]any

	// PluginInto unmarshals the named plugin's config section
	// into dst. dst must be a pointer to a struct (or
	// map[string]any). Missing section → no-op, no error;
	// malformed section → error.
	PluginInto(name string, dst any) error
}

// NewConfig constructs a ConfigService from a snapshot map.
// The snapshot is typically the contents of the top-level
// "plugins" key from viper at boot — see cmd/main.go for the
// canonical assembly path. A nil snapshot yields a service
// where every Plugin() returns an empty map and every
// PluginInto() is a no-op.
func NewConfig(snapshot map[string]any) ConfigService {
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	return &configSnapshot{plugins: snapshot}
}

type configSnapshot struct {
	plugins map[string]any
}

func (c *configSnapshot) Plugin(name string) map[string]any {
	if c == nil {
		return map[string]any{}
	}
	raw, ok := c.plugins[name]
	if !ok {
		return map[string]any{}
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return m
}

// PluginInto JSON-roundtrips the section into dst. The
// round-trip is wasteful but it gets us mapstructure-equivalent
// semantics (case-insensitive key matching, embedded structs,
// pointers) without taking a viper dependency in pkg/core. For
// the per-boot, per-plugin call rate this is unmeasurable.
func (c *configSnapshot) PluginInto(name string, dst any) error {
	sec := c.Plugin(name)
	if len(sec) == 0 {
		return nil
	}
	buf, err := json.Marshal(sec)
	if err != nil {
		return fmt.Errorf("core: marshal plugins.%s config: %w", name, err)
	}
	if err := json.Unmarshal(buf, dst); err != nil {
		return fmt.Errorf("core: unmarshal plugins.%s config into %T: %w", name, dst, err)
	}
	return nil
}
