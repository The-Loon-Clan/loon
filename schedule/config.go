package schedule

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

// JobConfigVarType is the kind of input rendered on the admin /config page.
// String / int / bool render as the obvious HTML inputs; textarea is for
// multi-line values like JSON-encoded scraper source lists.
type JobConfigVarType string

const (
	JobConfigString   JobConfigVarType = "string"
	JobConfigInt      JobConfigVarType = "int"
	JobConfigBool     JobConfigVarType = "bool"
	JobConfigTextarea JobConfigVarType = "textarea"
)

// JobConfigVar declares one admin-editable variable for a job. The
// declaration lives in code (so the default and label travel with the
// service that owns the variable); admin overrides live in the
// job_settings table and are looked up at runtime via JobInfo.GetConfig*.
//
// Sensitive=true marks values that should never be sent back to the
// browser in cleartext (passwords, API keys). Right now nothing in the
// codebase needs this, but the field is here so future jobs can use it
// without a schema migration.
type JobConfigVar struct {
	Key         string           // unique within the job, used as the form name
	Label       string           // human-readable label shown on the config page
	Description string           // help text shown under the input
	Type        JobConfigVarType // string | int | bool | textarea
	Default     string           // default value when no override is set
	Sensitive   bool             // never echo to UI in cleartext
}

// JobConfigStore is the subset of storage.Store the registry needs to
// persist overrides. Declared as a local interface so this file doesn't
// have to import pkg/storage and create a cycle.
type JobConfigStore interface {
	GetJobSettings(ctx context.Context, jobName string) (map[string]string, error)
	SetJobSetting(ctx context.Context, jobName, key, value string) error
	DeleteJobSetting(ctx context.Context, jobName, key string) error
}

// configMu guards the lazy-load of every job's configCache. JobInfo
// already lives behind GlobalJobRegistry.mu but the config cache is read
// from many goroutines (job loops) and only written rarely, so a separate
// mutex avoids contention with the registry's main lock.
var configMu sync.RWMutex

// DeclareConfig registers the variables an admin can edit for this job.
// Called once at construction time by the service that owns the job. The
// store is needed so subsequent GetConfig* calls can read overrides.
//
// Calling DeclareConfig more than once on the same job replaces the
// declaration — useful for tests but otherwise jobs should declare once.
func (j *JobInfo) DeclareConfig(store JobConfigStore, vars ...JobConfigVar) {
	configMu.Lock()
	j.configVars = vars
	j.configStore = store
	j.configCache = nil
	j.configLoaded = false
	configMu.Unlock()
}

// ConfigVars returns the declared variables (without values). Used by
// the admin config page to render the form. Returns a copy so callers
// can't mutate the registry state.
func (j *JobInfo) ConfigVars() []JobConfigVar {
	configMu.RLock()
	defer configMu.RUnlock()
	out := make([]JobConfigVar, len(j.configVars))
	copy(out, j.configVars)
	return out
}

// HasConfig reports whether this job has declared any admin-editable vars.
// The admin Jobs page uses this to decide whether to show the Config button.
func (j *JobInfo) HasConfig() bool {
	configMu.RLock()
	defer configMu.RUnlock()
	return len(j.configVars) > 0
}

// loadConfigLocked refreshes the cache from the store. Caller must hold
// configMu for write.
func (j *JobInfo) loadConfigLocked() {
	if j.configStore == nil {
		j.configCache = map[string]string{}
		j.configLoaded = true
		return
	}
	vals, err := j.configStore.GetJobSettings(context.Background(), j.Name)
	if err != nil {
		// Don't poison the cache on transient DB errors — leave it empty
		// so defaults apply, and try again next call.
		j.configCache = map[string]string{}
		return
	}
	j.configCache = vals
	j.configLoaded = true
}

// RefreshConfig forces a re-read from the store. Called from the admin
// config-save handler so a saved change takes effect on the next job tick
// without waiting for the cache TTL (there isn't one — refresh is
// admin-driven, since admin saves are rare).
func (j *JobInfo) RefreshConfig() {
	configMu.Lock()
	j.loadConfigLocked()
	configMu.Unlock()
}

// configValueLocked returns the merged value for one key (override → default).
// Caller must hold configMu for read.
func (j *JobInfo) configValueLocked(key string) (string, JobConfigVar, bool) {
	for _, v := range j.configVars {
		if v.Key != key {
			continue
		}
		if val, ok := j.configCache[key]; ok && val != "" {
			return val, v, true
		}
		return v.Default, v, true
	}
	return "", JobConfigVar{}, false
}

// readConfig is the shared lazy-load + lookup helper for the typed getters.
func (j *JobInfo) readConfig(key string) (string, JobConfigVar, bool) {
	configMu.RLock()
	if !j.configLoaded {
		configMu.RUnlock()
		configMu.Lock()
		if !j.configLoaded {
			j.loadConfigLocked()
		}
		configMu.Unlock()
		configMu.RLock()
	}
	defer configMu.RUnlock()
	return j.configValueLocked(key)
}

// GetConfigString returns the merged value for a string variable, falling
// back to the declared default. Returns "" if the key isn't declared.
func (j *JobInfo) GetConfigString(key string) string {
	val, _, _ := j.readConfig(key)
	return val
}

// GetConfigInt parses an int variable. Falls back to the declared default
// (also parsed) on parse errors so a typo in the admin UI doesn't break
// the running job — the next admin save can fix it.
func (j *JobInfo) GetConfigInt(key string) int {
	val, decl, ok := j.readConfig(key)
	if !ok {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
		return n
	}
	if n, err := strconv.Atoi(decl.Default); err == nil {
		return n
	}
	return 0
}

// GetConfigBool parses a bool variable. Accepts "1", "true", "yes" (any
// case) as true; everything else is false. Same fall-through-to-default
// behaviour as GetConfigInt.
func (j *JobInfo) GetConfigBool(key string) bool {
	val, _, _ := j.readConfig(key)
	v := strings.ToLower(strings.TrimSpace(val))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// SetConfig persists an override and refreshes the cache so the next
// Get* call sees the new value. Empty string deletes the override.
// Called by the admin config-save handler.
func (j *JobInfo) SetConfig(ctx context.Context, key, value string) error {
	configMu.RLock()
	store := j.configStore
	configMu.RUnlock()
	if store == nil {
		return nil
	}
	if err := store.SetJobSetting(ctx, j.Name, key, value); err != nil {
		return err
	}
	j.RefreshConfig()
	return nil
}

// JobConfigSnapshot is the per-variable view rendered on the admin
// config page. Value is the *current effective* value (override or
// default), HasOverride flags whether the override row exists so the
// page can show "(default)" badges and a Reset button.
type JobConfigSnapshot struct {
	Var         JobConfigVar
	Value       string
	HasOverride bool
}

// ConfigSnapshot returns one entry per declared variable, suitable for
// rendering the admin config page. Sensitive values are returned as
// empty strings to avoid echoing them back to the browser.
func (j *JobInfo) ConfigSnapshot() []JobConfigSnapshot {
	configMu.RLock()
	if !j.configLoaded {
		configMu.RUnlock()
		j.RefreshConfig()
		configMu.RLock()
	}
	defer configMu.RUnlock()

	out := make([]JobConfigSnapshot, 0, len(j.configVars))
	for _, v := range j.configVars {
		override, hasOverride := j.configCache[v.Key]
		val := override
		if !hasOverride || val == "" {
			val = v.Default
			hasOverride = false
		}
		if v.Sensitive {
			val = ""
		}
		out = append(out, JobConfigSnapshot{
			Var:         v,
			Value:       val,
			HasOverride: hasOverride,
		})
	}
	return out
}
