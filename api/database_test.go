package api

import (
	"strings"
	"testing"
)

func TestDatabaseSchemaContainsDesignedTables(t *testing.T) {
	tables := []string{
		"users",
		"patient_profiles",
		"patient_relationships",
		"emergency_contacts",
		"patient_link_invitations",
		"documents",
		"medications",
		"medication_events",
		"health_state",
		"sos_incidents",
		"emergency_access_sessions",
		"medication_catalog",
		"notification_endpoints",
		"notification_outbox",
	}

	for _, table := range tables {
		statement := "CREATE TABLE IF NOT EXISTS " + table
		if !strings.Contains(databaseSchema, statement) {
			t.Errorf("database schema does not create %s", table)
		}
	}

	if got := strings.Count(databaseSchema, "CREATE TABLE IF NOT EXISTS "); got != len(tables) {
		t.Fatalf("database schema creates %d tables, want %d", got, len(tables))
	}

	if !strings.Contains(databaseSchema, "health_state_value_matches_type") {
		t.Error("database schema does not constrain health-state values by type")
	}
}
