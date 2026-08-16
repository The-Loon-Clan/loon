package nntp

import (
	"strings"
	"testing"
)

// The overview verb is a property of the SERVER, so it is decided once per
// connection. Two things were wrong before, and the second hid the first:
//
//   - A server that implements only XOVER answered a FAILED OVER before every
//     batch. The crawler issues up to 20,000 batches a round, so that is 20,000
//     wasted round trips against a metered provider.
//   - When XOVER also failed, the OVER error was returned instead of XOVER's.
//     Operators saw "500 command unimplemented" — which is just "this server
//     has no OVER" — while the real failure was discarded.
func TestOverviewRemembersTheVerbAndReportsTheRealError(t *testing.T) {
	t.Run("xover-only server is probed once", func(t *testing.T) {
		srv := newFakeNNTP(t, map[string]string{
			"OVER":  "500 command unimplemented",
			"XOVER": "224 overview follows",
		})
		defer srv.Close()
		c := srv.dial(t)

		for i := 0; i < 3; i++ {
			if _, _, err := c.Overview(1, 10); err != nil {
				t.Fatalf("batch %d: %v", i, err)
			}
		}
		// One failed OVER on the first call, and never again.
		if got := srv.count("OVER"); got != 1 {
			t.Errorf("OVER was issued %d times, want 1 — the probe is being paid per batch", got)
		}
		if got := srv.count("XOVER"); got != 3 {
			t.Errorf("XOVER was issued %d times, want 3", got)
		}
	})

	t.Run("both unsupported reports XOVER's error", func(t *testing.T) {
		srv := newFakeNNTP(t, map[string]string{
			"OVER":  "500 command unimplemented",
			"XOVER": "423 no such article number in this group",
		})
		defer srv.Close()
		c := srv.dial(t)

		_, _, err := c.Overview(1, 10)
		if err == nil {
			t.Fatal("expected an error")
		}
		// The actionable half must be present; the OVER noise may be, as context.
		if !strings.Contains(err.Error(), "423") {
			t.Errorf("error %q does not carry XOVER's real failure", err)
		}
	})
}
