package phoneapi

import (
	"io"
	"log/slog"
	"testing"

	"github.com/A13xB0/RepeaterTastic/pb"
)

func handshakeFrames(t *testing.T, nonce uint32) []*pb.FromRadio {
	t.Helper()
	h, id, _ := testServer(t)
	var got []*pb.FromRadio
	s := NewSession(h, id, slog.New(slog.NewTextHandler(io.Discard, nil)), func(fr *pb.FromRadio) bool {
		got = append(got, fr)
		return true
	})
	s.startConfig(nonce)
	return got
}

// The Android app's two-stage handshake: 69420 fetches config, then 69421 fetches nodes.
// The app treats any my_info as the start of a new handshake, so Stage 2 must not send one
// (the firmware jumps straight to the other nodes). Sending it made the app ignore the Stage 2
// config_complete, stall for 12 s, reconnect, and give up after three tries.
func TestNodesOnlyHandshakeSendsNoMyInfo(t *testing.T) {
	frames := handshakeFrames(t, nonceOnlyNodes)
	if len(frames) == 0 || frames[len(frames)-1].GetConfigCompleteId() != nonceOnlyNodes {
		t.Fatalf("stage 2 must end with config_complete_id %d", nonceOnlyNodes)
	}
	for _, fr := range frames {
		switch fr.PayloadVariant.(type) {
		case *pb.FromRadio_MyInfo, *pb.FromRadio_Config, *pb.FromRadio_Channel, *pb.FromRadio_Metadata, *pb.FromRadio_DeviceuiConfig:
			t.Fatalf("stage 2 sent %T; only node infos and config_complete belong there", fr.PayloadVariant)
		}
	}
}

func TestConfigOnlyHandshakeStartsWithMyInfo(t *testing.T) {
	frames := handshakeFrames(t, nonceOnlyConfig)
	if frames[0].GetMyInfo() == nil {
		t.Fatalf("stage 1 must start with my_info, got %T", frames[0].PayloadVariant)
	}
	if frames[len(frames)-1].GetConfigCompleteId() != nonceOnlyConfig {
		t.Fatal("stage 1 must end with its config_complete_id")
	}
}
