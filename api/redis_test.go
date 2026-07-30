package api

import "testing"

func TestRedisSchemaKeys(t *testing.T) {
	if telemetryStreamKey != "guardianband:telemetry" {
		t.Fatalf("telemetry stream key = %q", telemetryStreamKey)
	}

	if telemetryArchiveGroup != "guardianband:telemetry-archive" {
		t.Fatalf("telemetry archive group = %q", telemetryArchiveGroup)
	}

	if got := patientHealthKey("patient-123"); got != "guardianband:patient:patient-123:health" {
		t.Fatalf("patient health key = %q", got)
	}

	if got := patientPresenceKey("patient-123"); got != "guardianband:patient:patient-123:presence" {
		t.Fatalf("patient presence key = %q", got)
	}
}
