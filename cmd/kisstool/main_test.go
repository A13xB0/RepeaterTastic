package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

func TestTextRoundTrip(t *testing.T) {
	rp, err := resolve("EU_868", "long_fast", 10)
	if err != nil {
		t.Fatal(err)
	}
	if c := radioConfig(rp); c.FrequencyHz != 869525000 || c.BandwidthHz != 250000 || c.SF != 11 || c.SyncWord != 0x2B || c.Preamble != 16 {
		t.Fatalf("config %+v", c)
	}
	frame, err := buildText(rp, 0xdeadbe00, 0x12345678, "hello")
	if err != nil {
		t.Fatal(err)
	}
	// to=broadcast, from, id, flags hop_start 3 | hop_limit 3, channel hash 0x08, next 0, relay 0xFF (0x00 is reserved).
	hdr := []byte{0xff, 0xff, 0xff, 0xff, 0x00, 0xbe, 0xad, 0xde, 0x78, 0x56, 0x34, 0x12, 0x63, 0x08, 0x00, 0xff}
	if !bytes.Equal(frame[:16], hdr) {
		t.Fatalf("header %x", frame[:16])
	}
	var out strings.Builder
	printFrame(&out, radio.Frame{Data: frame, RSSI: -90, SNR: 6.25}, 0x08)
	if s := out.String(); !strings.Contains(s, `TEXT_MESSAGE_APP, 5 byte payload: "hello"`) || !strings.Contains(s, "from !deadbe00") {
		t.Fatal(s)
	}
}
