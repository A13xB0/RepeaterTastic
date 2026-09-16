package logbuf

import (
	"io"
	"log/slog"
	"testing"
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
