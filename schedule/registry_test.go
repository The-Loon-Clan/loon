package schedule

import "testing"

// TestPausePersistsAcrossRegistration pins the pause-persistence contract:
// PauseJob writes through SavePaused, and a re-registration (i.e. a process
// restart) restores the flag via LoadPaused — so an operator's pause survives
// a deploy instead of silently resuming. In-memory behavior with no hooks
// installed is unchanged.
func TestPausePersistsAcrossRegistration(t *testing.T) {
	prevLoad, prevSave := LoadPaused, SavePaused
	defer func() { LoadPaused, SavePaused = prevLoad, prevSave }()
	saved := map[string]bool{}
	SavePaused = func(name string, paused bool) { saved[name] = paused }
	LoadPaused = func(name string) bool { return saved[name] }

	r := &Registry{}
	j := r.RegisterJob("Legacy Crawler", "d")
	if j.IsPaused() {
		t.Fatal("fresh job must not be paused")
	}
	if !r.PauseJob("Legacy Crawler") {
		t.Fatal("pause: job not found")
	}
	if !saved["Legacy Crawler"] {
		t.Error("pause not persisted through SavePaused")
	}

	// "Restart": a new registry registers the same job name.
	r2 := &Registry{}
	j2 := r2.RegisterJob("Legacy Crawler", "d")
	if !j2.IsPaused() {
		t.Error("persisted pause not restored at RegisterJob")
	}
	if j2.Status != "paused" {
		t.Errorf("restored status = %q, want paused", j2.Status)
	}

	if !r2.ResumeJob("Legacy Crawler") {
		t.Fatal("resume: job not found")
	}
	if saved["Legacy Crawler"] {
		t.Error("resume not persisted through SavePaused")
	}
}
