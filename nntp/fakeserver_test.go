package nntp

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeNNTP is a minimal scripted NNTP server: it answers a fixed line per verb
// and counts what it was asked.
//
// Written for the overview-verb tests, where the thing under test is WHICH
// COMMANDS GO ON THE WIRE and how many times. That is not observable from the
// returned data, so it cannot be asserted any other way — and it is exactly
// where the bug was: a probe repeated per batch, against a metered provider.
type fakeNNTP struct {
	ln      net.Listener
	replies map[string]string

	mu     sync.Mutex
	counts map[string]int
}

func newFakeNNTP(t *testing.T, replies map[string]string) *fakeNNTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeNNTP{ln: ln, replies: replies, counts: map[string]int{}}
	go s.serve()
	return s
}

func (s *fakeNNTP) Close() { _ = s.ln.Close() }

func (s *fakeNNTP) count(verb string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[verb]
}

func (s *fakeNNTP) dial(t *testing.T) *Conn {
	t.Helper()
	c, err := Dial("tcp", s.ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Quit() })
	return c
}

func (s *fakeNNTP) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeNNTP) handle(conn net.Conn) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	_, _ = w.WriteString("200 fake ready\r\n")
	_ = w.Flush()

	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.TrimSpace(line))
		if i := strings.IndexByte(verb, ' '); i >= 0 {
			verb = verb[:i]
		}

		s.mu.Lock()
		s.counts[verb]++
		s.mu.Unlock()

		switch verb {
		case "QUIT":
			_, _ = w.WriteString("205 bye\r\n")
			_ = w.Flush()
			return
		case "MODE":
			_, _ = w.WriteString("201 posting prohibited\r\n")
		default:
			reply, ok := s.replies[verb]
			if !ok {
				reply = "500 command unimplemented"
			}
			_, _ = w.WriteString(reply + "\r\n")
			// A 224 opens a multi-line block, which must be terminated or the
			// client blocks forever waiting for the dot.
			if strings.HasPrefix(reply, "224") {
				_, _ = w.WriteString(".\r\n")
			}
		}
		_ = w.Flush()
	}
}
