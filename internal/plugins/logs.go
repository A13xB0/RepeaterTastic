package plugins

import (
	"bytes"
	"strings"
	"sync"
	"time"
)

// LogLine is one line of a plugin's log: what it reported over the API, printed, or what the
// host did with it.
type LogLine struct {
	Time    int64  `json:"time"` // Unix ms
	Level   string `json:"level"`
	Source  string `json:"source"` // plugin, stdout, stderr, host
	Message string `json:"message"`
}

type logRing struct {
	mu   sync.Mutex
	buf  []LogLine
	next int
	full bool
}

func newLogRing(n int) *logRing { return &logRing{buf: make([]LogLine, n)} }

func (r *logRing) add(level, source, msg string) {
	if len(msg) > 2000 {
		msg = msg[:2000] + "…"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = LogLine{Time: time.Now().UnixMilli(), Level: level, Source: source, Message: msg}
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

func (r *logRing) list() []LogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]LogLine(nil), r.buf[:r.next]...)
	}
	return append(append([]LogLine(nil), r.buf[r.next:]...), r.buf[:r.next]...)
}

// lineWriter turns a process's output into log lines.
type lineWriter struct {
	ring   *logRing
	source string
	mu     sync.Mutex
	part   []byte
}

func (w *lineWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.part = append(w.part, b...)
	for {
		i := bytes.IndexByte(w.part, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimRight(string(w.part[:i]), "\r"); line != "" {
			level := "info"
			if w.source == "stderr" {
				level = "warn"
			}
			w.ring.add(level, w.source, line)
		}
		w.part = w.part[i+1:]
	}
	if len(w.part) > 8192 {
		w.ring.add("info", w.source, string(w.part))
		w.part = w.part[:0]
	}
	return len(b), nil
}
