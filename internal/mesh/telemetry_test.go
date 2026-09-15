package mesh

import (
	"testing"
	"time"
)

func TestDeviceMetricsReportsExternalPower(t *testing.T) {
	h := &Host{Air: NewAirtime(), started: time.Now().Add(-90 * time.Second)}
	dm := h.deviceMetrics(time.Now()).GetDeviceMetrics()
	if dm.GetBatteryLevel() != 101 || dm.GetUptimeSeconds() < 89 {
		t.Fatalf("device metrics = %v", dm)
	}
}
