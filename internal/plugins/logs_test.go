package plugins

import (
	"strings"
	"testing"
)

func TestLogRingWraps(t *testing.T) {
	r := newLogRing(3)
	for _, m := range []string{"a", "b", "c", "d"} {
		r.add("info", "host", m)
	}
	var got []string
	for _, l := range r.list() {
		got = append(got, l.Message)
	}
	if strings.Join(got, "") != "bcd" {
		t.Fatalf("got %v", got)
	}
	r.add("info", "host", strings.Repeat("x", 2500))
	lines := r.list()
	if last := lines[len(lines)-1].Message; len(last) != 2000+len("…") || !strings.HasSuffix(last, "…") {
		t.Fatalf("long line not cut: %d", len(last))
	}
}

func TestLineWriter(t *testing.T) {
	r := newLogRing(10)
	w := &lineWriter{ring: r, source: "stderr"}
	for _, chunk := range []string{"par", "tial\r\n\nnext\n", "unterminated"} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	lines := r.list()
	if len(lines) != 2 || lines[0].Message != "partial" || lines[1].Message != "next" || lines[0].Level != "warn" || lines[0].Source != "stderr" {
		t.Fatalf("lines %+v", lines)
	}

	out := &lineWriter{ring: r, source: "stdout"}
	_, _ = out.Write([]byte(strings.Repeat("y", 9000)))
	lines = r.list()
	if last := lines[len(lines)-1]; last.Level != "info" || len(last.Message) != 2000+len("…") {
		t.Fatalf("overlong partial line: %s %d", last.Level, len(last.Message))
	}
	if len(out.part) != 0 {
		t.Fatal("partial buffer not reset")
	}
}
