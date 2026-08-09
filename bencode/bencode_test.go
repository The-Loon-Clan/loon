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

// The encoder and the extra decoders moved here from the tracker plugin, which
// carried a second copy of this whole package. These goldens are the exact
// bytes that implementation produced for the same inputs, captured before the
// move: two encoders that disagree on key order produce different SHA-1s for
// the same torrent, so "it still compiles" is not the bar — the bytes have to
// match, or every previously-built .torrent stops matching its swarm.
func TestBuildOuterDict_ByteIdenticalToTheImplementationItReplaced(t *testing.T) {
	priv := int64(1)
	build := func(info []byte, announce string) []byte {
		return BuildOuterDict([]OuterField{
			{Key: "announce", Str: announce},
			{Key: "info", Raw: info},
			{Key: "private", Int: &priv},
		})
	}

	single := []byte("d6:lengthi12345e4:name8:test.mkv12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae")
	got := hex.EncodeToString(build(single, "https://private.local/announce/PASSKEY"))
	const wantSingle = "64383a616e6e6f756e636533383a68747470733a2f2f707269766174652e6c6f63616c2f616e6e6f756e63652f504153534b4559343a696e666f64363a6c656e67746869313233343565343a6e616d65383a746573742e6d6b7631323a7069656365206c656e67746869313633383465363a70696563657332303a616161616161616161616161616161616161616165373a7072697661746569316565"
	if got != wantSingle {
		t.Errorf("single-file encoding drifted:\n got %s\nwant %s", got, wantSingle)
	}

	multi := []byte("d5:filesld6:lengthi100e4:pathl1:a1:beed6:lengthi200e4:pathl1:ceee4:name5:batch12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae")
	got = hex.EncodeToString(build(multi, "https://t/announce/K"))
	const wantMulti = "64383a616e6e6f756e636532303a68747470733a2f2f742f616e6e6f756e63652f4b343a696e666f64353a66696c65736c64363a6c656e6774686931303065343a706174686c313a61313a62656564363a6c656e6774686932303065343a706174686c313a63656565343a6e616d65353a626174636831323a7069656365206c656e67746869313633383465363a70696563657332303a616161616161616161616161616161616161616165373a7072697661746569316565"
	if got != wantMulti {
		t.Errorf("multi-file encoding drifted:\n got %s\nwant %s", got, wantMulti)
	}
}

// BEP-3 requires bytewise-sorted keys, and the caller's slice must survive the
// call: fields are built once and reused, so sorting in place would reorder
// somebody else's data.
func TestBuildOuterDict_SortsKeysWithoutMutatingTheCaller(t *testing.T) {
	n := int64(7)
	fields := []OuterField{
		{Key: "zebra", Str: "z"},
		{Key: "announce", Str: "a"},
		{Key: "middle", Int: &n},
	}
	out := string(BuildOuterDict(fields))
	if out != "d8:announce1:a6:middlei7e5:zebra1:ze" {
		t.Errorf("unsorted or malformed: %q", out)
	}
	if fields[0].Key != "zebra" {
		t.Errorf("caller's slice was reordered: %q is first", fields[0].Key)
	}
}

// An info dict spliced in as Raw must come out byte-for-byte, which is the
// whole reason the encoder refuses to marshal parsed values.
func TestBuildOuterDict_SplicedInfoSurvivesHashing(t *testing.T) {
	info := []byte("d6:lengthi5e4:name1:xe")
	out := BuildOuterDict([]OuterField{
		{Key: "announce", Str: "http://x/a"},
		{Key: "info", Raw: info},
	})
	h, err := InfoHash(out)
	if err != nil {
		t.Fatalf("InfoHash: %v", err)
	}
	if h != sha1.Sum(info) {
		t.Error("spliced info hashed differently than the raw bytes")
	}
}

func TestDecodersOverASpannedDict(t *testing.T) {
	buf := []byte("d4:infod6:lengthi100e4:name3:abc5:filesl1:a1:beee")
	top, err := ScanTopDict(buf)
	if err != nil {
		t.Fatal(err)
	}
	info, err := ScanDict(buf, top["info"])
	if err != nil {
		t.Fatalf("ScanDict: %v", err)
	}
	// ScanDict returns ABSOLUTE coordinates; decoding with the outer buffer is
	// the contract, and an off-by-span bug here reads a neighbouring field.
	if s, err := DecodeString(buf, info["name"]); err != nil || string(s) != "abc" {
		t.Errorf("DecodeString: %q err=%v", s, err)
	}
	if n, err := DecodeInt(buf, info["length"]); err != nil || n != 100 {
		t.Errorf("DecodeInt: %d err=%v", n, err)
	}
	elems, err := DecodeList(buf, info["files"])
	if err != nil || len(elems) != 2 {
		t.Fatalf("DecodeList: %d elems err=%v", len(elems), err)
	}
	if s, _ := DecodeString(buf, elems[1]); string(s) != "b" {
		t.Errorf("list element 1: %q", s)
	}

	// Type mismatches are errors, not silent zeros — a tracker that reads a
	// string as an int would store a length of 0 for a real file.
	if _, err := DecodeInt(buf, info["name"]); err == nil {
		t.Error("DecodeInt accepted a string span")
	}
	if _, err := DecodeList(buf, info["length"]); err == nil {
		t.Error("DecodeList accepted an int span")
	}
	if _, err := ScanDict(buf, info["files"]); err == nil {
		t.Error("ScanDict accepted a list span")
	}
}
