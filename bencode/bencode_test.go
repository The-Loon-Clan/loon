package bencode

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"testing"
)

// A minimal but real .torrent shape: an outer dict with announce + info, where
// info carries name/length/piece length. Built by hand so the byte layout is
// known and the span arithmetic is checked against something, not against
// whatever an encoder happened to emit.
const oneFile = "d8:announce19:http://tracker/annc4:infod6:lengthi1024e4:name8:Some.mkv12:piece lengthi16384eee"

func TestInfoHashIsTheHashOfTheInfoSPAN(t *testing.T) {
	got, err := InfoHash([]byte(oneFile))
	if err != nil {
		t.Fatalf("InfoHash: %v", err)
	}

	// The info dict's exact bytes, located by hand: everything from the "d"
	// after "4:info" to its matching "e".
	start := strings.Index(oneFile, "4:info") + len("4:info")
	want := sha1sum(oneFile[start : len(oneFile)-1])
	if hex.EncodeToString(got[:]) != want {
		t.Errorf("InfoHash = %s, want %s (the SHA-1 of the raw info span)",
			hex.EncodeToString(got[:]), want)
	}
}

// THE property the whole scanner exists for. Two torrents whose info dicts are
// byte-identical but whose OUTER dicts differ (a different announce URL — which
// is exactly what baking a passkey in does) must produce the SAME info_hash.
// If this fails, every .torrent handed to a member points at a swarm the tracker
// does not recognise, and the feed importer dedups nothing.
func TestInfoHashIgnoresTheOuterDict(t *testing.T) {
	other := strings.Replace(oneFile, "19:http://tracker/annc", "29:http://tracker/annc?pk=abc123", 1)
	if other == oneFile {
		t.Fatal("the fixture did not change; the test is asserting nothing")
	}

	a, err := InfoHash([]byte(oneFile))
	if err != nil {
		t.Fatal(err)
	}
	b, err := InfoHash([]byte(other))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("info_hash changed when only the announce URL did: %x vs %x", a, b)
	}
}

// Malformed input is an error, not a panic and not a zero hash. The feed
// importer calls this on bytes fetched from a third-party RSS link, so the input
// is arbitrary by definition — a nil-hash-with-nil-error would silently dedup
// every broken download to the same key.
func TestInfoHashRejectsMalformedInput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":           "",
		"not a dict":      "i42e",
		"no info key":     "d8:announce5:hello e",
		"truncated":       "d4:infod6:lengthi10",
		"info not a dict": "d4:infoi5ee",
	} {
		t.Run(name, func(t *testing.T) {
			h, err := InfoHash([]byte(in))
			if err == nil {
				t.Errorf("accepted %q and returned %x", in, h)
			}
			if h != [20]byte{} {
				t.Errorf("returned a non-zero hash alongside an error: %x", h)
			}
		})
	}
}

// ScanTopDict reports spans into the ORIGINAL buffer, so a caller can hash or
// splice without a copy. Verify the spans actually point at the right bytes
// rather than merely existing.
func TestScanTopDictSpansPointAtTheOriginalBytes(t *testing.T) {
	m, err := ScanTopDict([]byte(oneFile))
	if err != nil {
		t.Fatalf("ScanTopDict: %v", err)
	}
	ann, ok := m["announce"]
	if !ok {
		t.Fatal("no announce span")
	}
	// The span covers the bencoded value, "20:http://tracker/annc".
	if got := oneFile[ann.Start:ann.End]; !strings.Contains(got, "http://tracker/annc") {
		t.Errorf("announce span = %q, which is not the announce value", got)
	}
	if info, ok := m["info"]; !ok {
		t.Error("no info span")
	} else if oneFile[info.Start] != 'd' {
		t.Errorf("info span starts at %q, want a dict opener", oneFile[info.Start])
	}
}

func sha1sum(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}
