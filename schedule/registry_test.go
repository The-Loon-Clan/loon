package schedule

import (
	"strings"
	"testing"
)

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

// A manual trigger must respect the write gate, and must NOT respect off-peak.
//
// The gate originally lived only in ServiceLoop, ahead of the scheduled tick, so it
// covered the cron path and nothing else: a "Run now" click on /admin/jobs or an
// ops-API trigger called the callback directly. During a migration that copies from
// a live database, that is one click away from writing into a cluster about to be
// replaced, with the rows discarded at cutover and nothing logged anywhere.
//
// The off-peak half is asserted too, because the distinction is deliberate rather
// than an oversight: overriding off-peak by hand is the documented point of a manual
// trigger ("I know it is busy, run it anyway"), while read-only is not a preference
// about timing but a statement that writes must not happen.
func TestTriggerJobRespectsTheWriteGate(t *testing.T) {
	prevW, prevO := WriteGate, OffPeakGate
	defer func() { WriteGate, OffPeakGate = prevW, prevO }()

	t.Run("a writing job is refused while read-only", func(t *testing.T) {
		WriteGate = func() bool { return false }
		reg := NewRegistry()
		ran := false
		j := reg.RegisterJob("tg-writer", "t").MarkWrites()
		j.SetTrigger(func() { ran = true })
		if reg.TriggerJob("tg-writer") {
			t.Error("TriggerJob reported success while the write gate was closed")
		}
		if ran {
			t.Error("a job flagged MarkWrites ran from a manual trigger during read-only")
		}
	})

	t.Run("a reporting job is still triggerable while read-only", func(t *testing.T) {
		WriteGate = func() bool { return false }
		reg := NewRegistry()
		ran := false
		j := reg.RegisterJob("tg-reader", "t") // no MarkWrites
		j.SetTrigger(func() { ran = true })
		if !reg.TriggerJob("tg-reader") || !ran {
			t.Error("a read-only job was refused; read-only must not block reporting jobs")
		}
	})

	t.Run("off-peak does NOT block a manual trigger", func(t *testing.T) {
		WriteGate = func() bool { return true }
		OffPeakGate = func() bool { return false }
		reg := NewRegistry()
		ran := false
		j := reg.RegisterJob("tg-offpeak", "t").MarkOffPeak().MarkWrites()
		j.SetTrigger(func() { ran = true })
		if !reg.TriggerJob("tg-offpeak") || !ran {
			t.Error("off-peak blocked a manual trigger; overriding off-peak by hand is the point of one")
		}
	})
}

// TestTriggerJobRespectsPause is the other half of the gate above, and it was
// missing for the same reason.
//
// ServiceLoop checks IsPaused ahead of every scheduled tick, and its comment
// says why it lives there: the Pause button then works for EVERY loop-driven
// job "not just the ones that remembered to". The MANUAL path does not go
// through the loop, so it was covered by nothing — on 20 Aug 2026, 33 triggers
// across 18 plugins ran regardless of pause, each having been expected to ask
// for itself and none doing so.
//
// core.Job's documentation already put the responsibility here: "RunLoop
// already skips a paused job... This is for the MANUAL trigger, which does not
// go through the loop: without it, 'run now' on a paused job runs it, which is
// not what pausing means."
func TestTriggerJobRespectsPause(t *testing.T) {
	prevW, prevO := WriteGate, OffPeakGate
	defer func() { WriteGate, OffPeakGate = prevW, prevO }()
	WriteGate = func() bool { return true }
	OffPeakGate = func() bool { return true }

	t.Run("a paused job is refused", func(t *testing.T) {
		reg := NewRegistry()
		ran := false
		j := reg.RegisterJob("tg-paused", "t")
		j.SetTrigger(func() { ran = true })
		if !reg.PauseJob("tg-paused") {
			t.Fatal("PauseJob did not pause the job")
		}
		if reg.TriggerJob("tg-paused") {
			t.Error("TriggerJob reported success on a paused job")
		}
		if ran {
			t.Error("a paused job ran from a manual trigger — that is not what pausing means")
		}
	})

	t.Run("resuming makes it triggerable again", func(t *testing.T) {
		reg := NewRegistry()
		ran := false
		j := reg.RegisterJob("tg-resume", "t")
		j.SetTrigger(func() { ran = true })
		reg.PauseJob("tg-resume")
		reg.ResumeJob("tg-resume")
		if !reg.TriggerJob("tg-resume") || !ran {
			t.Error("a resumed job was still refused")
		}
	})

	t.Run("an unpaused job is unaffected", func(t *testing.T) {
		reg := NewRegistry()
		ran := false
		j := reg.RegisterJob("tg-plain", "t")
		j.SetTrigger(func() { ran = true })
		if !reg.TriggerJob("tg-plain") || !ran {
			t.Error("the pause gate refused a job that was never paused")
		}
	})

	// The refusal is LOGGED, or an operator clicks Run now, nothing happens,
	// and the job's own log says nothing about why.
	t.Run("the refusal says why", func(t *testing.T) {
		reg := NewRegistry()
		j := reg.RegisterJob("tg-says", "t")
		j.SetTrigger(func() {})
		reg.PauseJob("tg-says")
		reg.TriggerJob("tg-says")

		var found bool
		for _, s := range reg.GetAllSnapshots() {
			if s.Name != "tg-says" {
				continue
			}
			for _, line := range s.Logs {
				if strings.Contains(line, "paused") {
					found = true
				}
			}
		}
		if !found {
			t.Error("the refusal left nothing in the job log")
		}
	})
}
