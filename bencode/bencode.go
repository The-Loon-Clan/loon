// Package bencode decodes just enough bencode to identify a .torrent.
//
// It is a SCANNER, not a parser: values are located as byte spans into the
// original buffer and never decoded into Go values. That is the point. A
// torrent's identity is the SHA-1 of the raw `info` dict bytes, and once
// `info` has been re-emitted from parsed values its byte layout (key order,
// int encoding, string escaping) can diverge from the original — which
// changes the hash, which changes the torrent. So the info dict is hashed in
// place and never round-tripped.
//
// The encoder half is deliberately narrow: BuildOuterDict rebuilds only the
// OUTER dict and splices `info` in verbatim. There is no general-purpose
// bencode marshaller here, and there should not be one — the moment a caller
// can re-emit an info dict from parsed values, it can change a torrent's
// identity by accident.
//
// History: this lived in the ameNZB host as pkg/tracker, then pkg/bencode,
// serving the RSS feed importer's info_hash dedup. It moved here when that
// importer became the feeds plugin, because a plugin cannot import host
// packages and vendoring a hash function per consumer is how the `d4:infoi5ee`
// bug shipped twice — the same gap, found and fixed separately in two copies.
// The tracker plugin carried the second copy, scanner and encoder both, until
// it was folded in here. One canonical encoder is not tidiness: two encoders
// that disagree on key order produce different SHA-1s for the same torrent,
// and the disagreement surfaces as a swarm that rejects a .torrent this code
// swears is correct.
package bencode

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

type Span struct {
	Start, End int
}

// Len returns the number of bytes covered by the span.
func (s Span) Len() int { return s.End - s.Start }

// Bytes returns the sub-slice the span covers.
func (s Span) Bytes(b []byte) []byte { return b[s.Start:s.End] }

type bscan struct {
	b []byte
	p int
}

func (d *bscan) peek() (byte, error) {
	if d.p >= len(d.b) {
		return 0, errors.New("bencode: unexpected EOF")
	}
	return d.b[d.p], nil
}

// skipValue advances past exactly one bencoded value and returns its span.
func (d *bscan) skipValue() (Span, error) {
	start := d.p
	c, err := d.peek()
	if err != nil {
		return Span{}, err
	}
	switch {
	case c == 'i':
		d.p++
		for d.p < len(d.b) && d.b[d.p] != 'e' {
			d.p++
		}
		if d.p >= len(d.b) {
			return Span{}, errors.New("bencode: unterminated int")
		}
		d.p++
	case c >= '0' && c <= '9':
		colon := -1
		for i := d.p; i < len(d.b); i++ {
			if d.b[i] == ':' {
				colon = i
				break
			}
			if d.b[i] < '0' || d.b[i] > '9' {
				return Span{}, fmt.Errorf("bencode: bad length digit at %d", i)
			}
		}
		if colon < 0 {
			return Span{}, errors.New("bencode: no colon in string")
		}
		n, err := strconv.Atoi(string(d.b[d.p:colon]))
		if err != nil || n < 0 {
			return Span{}, fmt.Errorf("bencode: bad length: %v", err)
		}
		d.p = colon + 1 + n
		if d.p > len(d.b) {
			return Span{}, errors.New("bencode: string truncated")
		}
	case c == 'l':
		d.p++
		for {
			c2, err := d.peek()
			if err != nil {
				return Span{}, err
			}
			if c2 == 'e' {
				d.p++
				break
			}
			if _, err := d.skipValue(); err != nil {
				return Span{}, err
			}
		}
	case c == 'd':
		d.p++
		for {
			c2, err := d.peek()
			if err != nil {
				return Span{}, err
			}
			if c2 == 'e' {
				d.p++
				break
			}
			if _, err := d.skipValue(); err != nil { // key
				return Span{}, err
			}
			if _, err := d.skipValue(); err != nil { // value
				return Span{}, err
			}
		}
	default:
		return Span{}, fmt.Errorf("bencode: unknown type %q at %d", c, d.p)
	}
	return Span{Start: start, End: d.p}, nil
}

// readStringInto reads a bencoded byte string starting at d.p, advances the
// cursor past it, and returns the raw bytes plus the span.
func (d *bscan) readStringInto() ([]byte, Span, error) {
	start := d.p
	c, err := d.peek()
	if err != nil {
		return nil, Span{}, err
	}
	if c < '0' || c > '9' {
		return nil, Span{}, fmt.Errorf("bencode: expected string at %d", d.p)
	}
	colon := -1
	for i := d.p; i < len(d.b); i++ {
		if d.b[i] == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return nil, Span{}, errors.New("bencode: no colon in string")
	}
	n, _ := strconv.Atoi(string(d.b[d.p:colon]))
	if n < 0 || colon+1+n > len(d.b) {
		return nil, Span{}, errors.New("bencode: string truncated")
	}
	val := d.b[colon+1 : colon+1+n]
	d.p = colon + 1 + n
	return val, Span{Start: start, End: d.p}, nil
}

// ScanTopDict returns key → raw-value-span for a top-level bencoded dict.
// Spans are absolute within b so callers can splice fields back untouched.
func ScanTopDict(b []byte) (map[string]Span, error) {
	d := &bscan{b: b}
	c, err := d.peek()
	if err != nil {
		return nil, err
	}
	if c != 'd' {
		return nil, errors.New("bencode: top-level must be a dict")
	}
	d.p++
	m := make(map[string]Span)
	for {
		c, err := d.peek()
		if err != nil {
			return nil, err
		}
		if c == 'e' {
			return m, nil
		}
		key, _, err := d.readStringInto()
		if err != nil {
			return nil, err
		}
		v, err := d.skipValue()
		if err != nil {
			return nil, err
		}
		m[string(key)] = v
	}
}

// InfoHash returns the SHA-1 of the raw `info` dict bytes — stable as long
// as the info span is spliced rather than re-encoded.
func InfoHash(b []byte) ([20]byte, error) {
	m, err := ScanTopDict(b)
	if err != nil {
		return [20]byte{}, err
	}
	info, ok := m["info"]
	if !ok {
		return [20]byte{}, errors.New("bencode: missing info dict")
	}
	// The span must actually BE a dict. The version this was lifted from hashed
	// whatever `info` pointed at, so `d4:infoi5ee` produced a confident-looking
	// hash of an integer. That matters here specifically: the caller is the RSS
	// importer, running this over bytes fetched from a third-party link, and a
	// plausible hash for junk input dedups two unrelated downloads onto one key.
	// A torrent whose info is not a dict has no info_hash, so say so.
	if info.Len() == 0 || b[info.Start] != 'd' {
		return [20]byte{}, errors.New("bencode: info is not a dict")
	}
	return sha1.Sum(b[info.Start:info.End]), nil
}

// ScanDict parses the dict that span covers (span must point at `d...e`) and
// returns key → value-span in absolute coordinates within b.
func ScanDict(b []byte, span Span) (map[string]Span, error) {
	if span.Len() < 2 || b[span.Start] != 'd' {
		return nil, errors.New("bencode: span is not a dict")
	}
	sub := b[span.Start:span.End]
	m, err := ScanTopDict(sub)
	if err != nil {
		return nil, err
	}
	shifted := make(map[string]Span, len(m))
	for k, v := range m {
		shifted[k] = Span{Start: v.Start + span.Start, End: v.End + span.Start}
	}
	return shifted, nil
}

// DecodeString decodes the bencoded string at span and returns its payload.
func DecodeString(b []byte, span Span) ([]byte, error) {
	d := &bscan{b: b, p: span.Start}
	v, _, err := d.readStringInto()
	if err != nil {
		return nil, err
	}
	return v, nil
}

// DecodeInt decodes the bencoded int at span.
func DecodeInt(b []byte, span Span) (int64, error) {
	if span.Len() < 3 || b[span.Start] != 'i' || b[span.End-1] != 'e' {
		return 0, errors.New("bencode: span is not an int")
	}
	return strconv.ParseInt(string(b[span.Start+1:span.End-1]), 10, 64)
}

// DecodeList decodes the bencoded list at span, returning each element's
// span in absolute coordinates. Empty lists return a nil slice.
func DecodeList(b []byte, span Span) ([]Span, error) {
	if span.Len() < 2 || b[span.Start] != 'l' || b[span.End-1] != 'e' {
		return nil, errors.New("bencode: span is not a list")
	}
	d := &bscan{b: b, p: span.Start + 1}
	var out []Span
	for d.p < span.End-1 {
		v, err := d.skipValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// --- Encoder (outer wrapper only — never touch the info span) ----------------

// Writer is a minimal streaming bencode writer, for response bodies whose
// shape is fixed and nested (a tracker's announce and scrape replies) rather
// than a flat dict BuildOuterDict can assemble.
//
// It does NOT sort keys — it cannot, having no idea when a dict is finished.
// BEP-3 requires bytewise-sorted keys, so a caller emitting them by hand must
// keep them in order; where the order is a wire requirement rather than an
// accident, pin it with a test. Prefer BuildOuterDict whenever the dict is
// flat, because that one sorts for you.
type Writer struct{ buf []byte }

// BeginDict/BeginList open a container; End closes either one (bencode
// terminates both with 'e').
func (w *Writer) BeginDict() { w.buf = append(w.buf, 'd') }
func (w *Writer) BeginList() { w.buf = append(w.buf, 'l') }
func (w *Writer) End()       { w.buf = append(w.buf, 'e') }

// Str writes a bencoded byte string. Used for keys as well as values.
func (w *Writer) Str(s string) {
	w.buf = strconv.AppendInt(w.buf, int64(len(s)), 10)
	w.buf = append(w.buf, ':')
	w.buf = append(w.buf, s...)
}

// Bytes writes a bencoded byte string from raw bytes — compact peer lists and
// 20-byte info hashes are not valid UTF-8 and must not round-trip a string
// conversion.
func (w *Writer) Bytes(b []byte) {
	w.buf = strconv.AppendInt(w.buf, int64(len(b)), 10)
	w.buf = append(w.buf, ':')
	w.buf = append(w.buf, b...)
}

// Int writes a bencoded integer.
func (w *Writer) Int(n int64) {
	w.buf = append(w.buf, 'i')
	w.buf = strconv.AppendInt(w.buf, n, 10)
	w.buf = append(w.buf, 'e')
}

// Raw splices already-bencoded bytes in verbatim.
func (w *Writer) Raw(b []byte) { w.buf = append(w.buf, b...) }

// Out returns the accumulated bytes. The Writer keeps ownership; callers that
// hold on to the slice past further writes must copy it.
func (w *Writer) Out() []byte { return w.buf }

// OuterField is one key of a rebuilt outer dict. Exactly one of Raw/Str/Int
// carries the value: Raw is already-bencoded bytes spliced in verbatim (this
// is how `info` survives a rebuild with its hash intact), Str and Int are
// encoded here.
type OuterField struct {
	Key string
	Raw []byte // already bencoded
	Str string
	Int *int64
}

// BuildOuterDict produces d<sorted key/value pairs>e.
//
// The sort is not cosmetic: BEP-3 requires dict keys in bytewise order, and a
// client that re-hashes what it received will disagree with an unsorted
// encoder. Sorting a COPY matters too — callers build their field slice once
// and reuse it, and reordering their slice underneath them is the kind of
// aliasing bug that only shows up on the second call.
func BuildOuterDict(fields []OuterField) []byte {
	sorted := make([]OuterField, len(fields))
	copy(sorted, fields)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	var w Writer
	w.BeginDict()
	for _, f := range sorted {
		w.Str(f.Key)
		switch {
		case f.Raw != nil:
			w.Raw(f.Raw)
		case f.Int != nil:
			w.Int(*f.Int)
		default:
			w.Str(f.Str)
		}
	}
	w.End()
	return w.Out()
}
