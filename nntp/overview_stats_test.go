package nntp

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
)

// A server whose overview response mixes one good line with each malformed
// shape. The four drop reasons used to be bare continues — this pins that
// every one is now counted, because a consumer that records coverage after a
// fetch treats uncounted drops as offered-and-absent forever.
func TestOverviewWithStatsCountsEveryDrop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		fmt.Fprint(c, "200 Welcome\r\n")
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.Fields(strings.TrimSpace(line))[0])
			switch cmd {
			case "OVER", "XOVER":
				fmt.Fprint(c, "224 Overview follows\r\n")
				// good
				fmt.Fprint(c, "1\tGood Subject\tp@x\tMon, 02 Jan 2006 15:04:05 -0700\t<a1@x>\t\t1000\t10\r\n")
				// too few fields
				fmt.Fprint(c, "2\tShort Line\r\n")
				// bad message number
				fmt.Fprint(c, "abc\tBad Number\tp@x\tMon, 02 Jan 2006 15:04:05 -0700\t<a2@x>\t\t1000\t10\r\n")
				// empty message-id
				fmt.Fprint(c, "3\tNo ID\tp@x\tMon, 02 Jan 2006 15:04:05 -0700\t\t\t1000\t10\r\n")
				// bad byte count
				fmt.Fprint(c, "4\tBad Bytes\tp@x\tMon, 02 Jan 2006 15:04:05 -0700\t<a4@x>\t\tNaN\t10\r\n")
				fmt.Fprint(c, ".\r\n")
			case "QUIT":
				fmt.Fprint(c, "205 Bye\r\n")
				return
			default:
				fmt.Fprint(c, "500 Unknown\r\n")
			}
		}
	}()

	conn, err := Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Quit()

	ovs, _, stats, err := conn.OverviewWithStats(1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ovs) != 1 || stats.Parsed != 1 {
		t.Errorf("parsed %d/%d, want exactly the one good line", len(ovs), stats.Parsed)
	}
	if stats.Lines != 5 {
		t.Errorf("lines = %d, want 5", stats.Lines)
	}
	if stats.DroppedFields != 1 || stats.DroppedNumber != 1 || stats.DroppedNoID != 1 || stats.DroppedBytes != 1 {
		t.Errorf("drop accounting = %+v, want one of each reason", stats)
	}
	if stats.Lines != stats.Parsed+stats.Dropped() {
		t.Errorf("accounting does not balance: %d lines vs %d parsed + %d dropped",
			stats.Lines, stats.Parsed, stats.Dropped())
	}
}
