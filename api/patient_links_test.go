package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostPatientLinkHandler_RegisteredUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create sqlmock: %v", err)
	}
	defer db.Close()

	api := &API{db: db}
	
	reqBody := CreatePatientLinkRequest{
		Phone:        "+905551234567",
		Relationship: "Spouse",
		Kind:         "family",
		CanMonitor:   true,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/patient-links", bytes.NewBuffer(bodyBytes))
	
	// Mock JWT context
	patientID := "test-patient-id"
	ctx := context.WithValue(req.Context(), UserContextKey, patientID)
	req = req.WithContext(ctx)

	// Expect check if user exists
	mock.ExpectQuery("^SELECT id FROM users WHERE phone_number = \\$1").
		WithArgs(reqBody.Phone).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("registered-user-id"))

	// Expect check for duplicate
	mock.ExpectQuery("^SELECT EXISTS\\(SELECT 1 FROM patient_links").
		WithArgs(patientID, "registered-user-id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	// Expect insert
	mock.ExpectExec("^INSERT INTO patient_links").
		WithArgs(sqlmock.AnyArg(), patientID, "registered-user-id", reqBody.Relationship, reqBody.Kind, reqBody.CanMonitor, reqBody.IsEmergencyContact, reqBody.IsPrimary).
		WillReturnResult(sqlmock.NewResult(1, 1))

	rr := httptest.NewRecorder()
	api.PostPatientLinkHandler(rr, req)

	if status := rr.Code; status != http.StatusCreated {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusCreated)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("there were unfulfilled expectations: %s", err)
	}
}

func TestPostPatientLinkHandler_ExternalContactRejectMonitor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create sqlmock: %v", err)
	}
	defer db.Close()

	api := &API{db: db}
	
	reqBody := CreatePatientLinkRequest{
		Phone:        "+905550000000",
		Relationship: "Neighbor",
		Kind:         "other",
		CanMonitor:   true, // Should be rejected for external contact
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/patient-links", bytes.NewBuffer(bodyBytes))
	
	patientID := "test-patient-id"
	ctx := context.WithValue(req.Context(), UserContextKey, patientID)
	req = req.WithContext(ctx)

	// User not found
	mock.ExpectQuery("^SELECT id FROM users WHERE phone_number = \\$1").
		WithArgs(reqBody.Phone).
		WillReturnError(sql.ErrNoRows)

	rr := httptest.NewRecorder()
	api.PostPatientLinkHandler(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}
}
