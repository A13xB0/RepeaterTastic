package logbuf

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRadioAndIdentityFields(t *testing.T) {
	b := New(4)
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), b)).With("radio", "mf")
	log.With("identity", "!0badcafe").Warn("node disconnected", "addr", "127.0.0.1:4801")
	log.Info("radio configured")
	got := b.Recent(2)
	if e := got[0]; e.Radio != "mf" || e.Identity != "!0badcafe" || e.Level != "warn" || e.Msg != "node disconnected addr=127.0.0.1:4801" {
		t.Fatalf("entry %+v", e)
	}
	if e := got[1]; e.Radio != "mf" || e.Identity != "" || e.Msg != "radio configured" {
		t.Fatalf("entry %+v", e)
	}
}

func TestRecentWrapsAndLimits(t *testing.T) {
	b := New(3)
	if got := b.Recent(10); len(got) != 0 {
		t.Fatalf("empty buffer returned %v", got)
	}
	for _, m := range []string{"a", "b"} {
		b.add(Entry{Msg: m})
	}
	if got := msgs(b.Recent(10)); got != "ab" {
		t.Fatalf("before wrap: %q", got)
	}
	for _, m := range []string{"c", "d", "e"} {
		b.add(Entry{Msg: m})
	}
	if got := msgs(b.Recent(10)); got != "cde" {
		t.Fatalf("after wrap: %q", got)
	}
	if got := msgs(b.Recent(2)); got != "de" {
		t.Fatalf("limited: %q", got)
	}
}

func msgs(es []Entry) string {
	s := ""
	for _, e := range es {
		s += e.Msg
	}
	return s
}

func TestOnEntryNotifies(t *testing.T) {
	b := New(2)
	var seen []Entry
	b.OnEntry(func(e Entry) { seen = append(seen, e) })
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), b))
	log.Error("boom", "code", 7)
	if len(seen) != 1 || seen[0].Msg != "boom code=7" || seen[0].Level != "error" || seen[0].Time == 0 {
		t.Fatalf("notified %+v", seen)
	}
}

func TestHandleZeroTimeUsesNow(t *testing.T) {
	b := New(1)
	h := NewHandler(slog.NewTextHandler(io.Discard, nil), b)
	before := time.Now().UnixMilli()
	if err := h.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelDebug, "zero", 0)); err != nil {
		t.Fatal(err)
	}
	if e := b.Recent(1)[0]; e.Time < before || e.Level != "debug" {
		t.Fatalf("entry %+v, want time >= %d", e, before)
	}
}

func TestWithGroupAndEnabled(t *testing.T) {
	b := New(2)
	var out strings.Builder
	next := slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo})
	h := NewHandler(next, b)
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug should be disabled")
	}
	log := slog.New(h).With("radio", "lf").WithGroup("g")
	log.Info("grouped", "k", "v")
	if e := b.Recent(1)[0]; e.Radio != "lf" || e.Msg != "grouped k=v" {
		t.Fatalf("entry %+v", e)
	}
	if !strings.Contains(out.String(), "g.k=v") {
		t.Fatalf("next handler did not get the group: %q", out.String())
	}
}
