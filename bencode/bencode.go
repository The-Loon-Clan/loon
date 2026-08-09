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
// History: this lived in the ameNZB host as pkg/tracker, then pkg/bencode,
// serving the RSS feed importer's info_hash dedup. It moved here when that
// importer became the feeds plugin, because a plugin cannot import host
// packages and vendoring a hash function per consumer is how the `d4:infoi5ee`
// bug shipped twice. The tracker plugin still carries the encoder half
// (outer-dict splicing) alongside its own copy of this scanner; consolidating
// that onto this package is an open follow-up.
package bencode

import (
	"crypto/sha1"
	"errors"
	"fmt"
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
