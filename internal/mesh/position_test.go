package mesh

import (
	"testing"
	"time"

	"github.com/A13xB0/RepeaterTastic/pb"
)

func TestHardwareFromModem(t *testing.T) {
	for name, want := range map[string]pb.HardwareModel{
		"Heltec V3": pb.HardwareModel_HELTEC_V3, "Heltec V4": pb.HardwareModel_HELTEC_V4,
		"RAK4631": pb.HardwareModel_RAK4631, "XIAO nRF52840": pb.HardwareModel_XIAO_NRF52_KIT,
		"": pb.HardwareModel_PORTDUINO, "Mystery Board": pb.HardwareModel_PORTDUINO,
	} {
		if got := hardwareFromModem(name); got != want {
			t.Errorf("hardwareFromModem(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFixedPositionProto(t *testing.T) {
	pole := FixedPosition{Latitude: 56.20557319333596, Longitude: -3.1618580230252453, Altitude: 180}
	exact := pole.proto(time.Unix(1_789_000_000, 0))
	if exact.GetLatitudeI() != 562055732 || exact.GetLongitudeI() != -31618580 || exact.GetAltitude() != 180 ||
		exact.PrecisionBits != 32 || exact.LocationSource != pb.Position_LOC_MANUAL || exact.Time != 1_789_000_000 {
		t.Fatalf("exact position = %v", exact)
	}
	pole.PrecisionBits = 13
	coarse := pole.proto(time.Now())
	if coarse.PrecisionBits != 13 || coarse.GetLatitudeI() == exact.GetLatitudeI() {
		t.Fatalf("13-bit position not coarsened: %v", coarse)
	}
	if d := coarse.GetLatitudeI() - exact.GetLatitudeI(); d > 1<<19 || d < -(1<<19) {
		t.Fatalf("13-bit latitude moved %d, more than half a cell", d)
	}
	if (FixedPosition{Interval: time.Minute}).interval() != positionMinInterval {
		t.Fatal("interval below the minimum should be raised")
	}
}
