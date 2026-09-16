// Package logbuf keeps recent log lines for the web UI while passing them on to another handler.
package logbuf

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Time  int64  `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
	// Radio and Identity say which radio and identity (node ID) a line is about, when it is.
	Radio    string `json:"radio,omitempty"`
	Identity string `json:"identity,omitempty"`
}

// Buffer is a ring of log entries with subscribers.
type Buffer struct {
	mu      sync.Mutex
	entries []Entry
	next    int
	full    bool
	notify  func(Entry)
}

func New(n int) *Buffer { return &Buffer{entries: make([]Entry, n)} }

// OnEntry registers a callback for new entries (e.g. publish to the event bus).
func (b *Buffer) OnEntry(fn func(Entry)) {
	b.mu.Lock()
	b.notify = fn
	b.mu.Unlock()
}

func (b *Buffer) add(e Entry) {
	b.mu.Lock()
	b.entries[b.next] = e
	b.next = (b.next + 1) % len(b.entries)
	if b.next == 0 {
		b.full = true
	}
	fn := b.notify
	b.mu.Unlock()
	if fn != nil {
		fn(e)
	}
}

// Recent returns up to limit entries, oldest first.
func (b *Buffer) Recent(limit int) []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.next
	if b.full {
		n = len(b.entries)
	}
	if limit > n {
		limit = n
	}
	out := make([]Entry, 0, limit)
	for i := limit; i > 0; i-- {
		out = append(out, b.entries[(b.next-i+len(b.entries))%len(b.entries)])
	}
	return out
}

// Handler tees records into the buffer.
type Handler struct {
	next  slog.Handler
	buf   *Buffer
	attrs []slog.Attr
	group string
}

func NewHandler(next slog.Handler, buf *Buffer) *Handler { return &Handler{next: next, buf: buf} }

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Message)
	e := Entry{Level: strings.ToLower(r.Level.String())}
	write := func(a slog.Attr) bool {
		// Radio and identity get their own fields (the last one given wins).
		switch v := a.Value.String(); a.Key {
		case "radio":
			e.Radio = v
			return true
		case "identity":
			e.Identity = v
			return true
		}
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value.Any())
		return true
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	e.Time, e.Msg = t.UnixMilli(), sb.String()
	h.buf.add(e)
	return h.next.Handle(ctx, r)
}

func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	return &Handler{next: h.next.WithAttrs(as), buf: h.buf, attrs: append(append([]slog.Attr{}, h.attrs...), as...), group: h.group}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{next: h.next.WithGroup(name), buf: h.buf, attrs: h.attrs, group: name}
}
