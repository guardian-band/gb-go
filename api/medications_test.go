package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
)

func TestGetMedicationCatalog(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	req := httptest.NewRequest(http.MethodGet, "/api/medication-catalog?search=Parol", nil)
	rec := httptest.NewRecorder()

	mock.ExpectQuery("SELECT id, name, strength FROM medication_catalog WHERE name ILIKE \\$1").
		WithArgs("%Parol%").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "strength"}).
			AddRow("med-1", "Parol", "500mg"))

	a.GetMedicationCatalogHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp []MedicationCatalogItem
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp) != 1 || resp[0].Name != "Parol" {
		t.Errorf("unexpected catalog items: %+v", resp)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	reqBody := CreateMedicationRequest{
		Name:           "Parol",
		Strength:       "500mg",
		Instructions:   "Günde 2 kez",
		FrequencyHours: 12,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	mock.ExpectExec("INSERT INTO medications").
		WithArgs(sqlmock.AnyArg(), userID, sqlmock.AnyArg(), reqBody.Name, reqBody.Strength, reqBody.Instructions, sqlmock.AnyArg(), true).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationInvalidFrequency(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	reqBody := CreateMedicationRequest{
		Name:           "Parol",
		Strength:       "500mg",
		Instructions:   "Günde 2 kez",
		FrequencyHours: 5, // invalid frequency
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationWithPrescriptionSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	prescriptionDocID := "doc-uuid-abc"

	reqBody := CreateMedicationRequest{
		Name:                   "Parol",
		Strength:               "500mg",
		Instructions:           "Günde 2 kez",
		FrequencyHours:         12,
		PrescriptionDocumentID: &prescriptionDocID,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	// Mock DB check for prescription
	mock.ExpectQuery("^SELECT patient_id, kind FROM documents WHERE id = \\$1").
		WithArgs(prescriptionDocID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "kind"}).
			AddRow(userID, "prescription"))

	mock.ExpectExec("INSERT INTO medications").
		WithArgs(sqlmock.AnyArg(), userID, prescriptionDocID, reqBody.Name, reqBody.Strength, reqBody.Instructions, sqlmock.AnyArg(), true).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationWithPrescriptionForeignOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	otherUserID := "user-uuid-999"
	prescriptionDocID := "doc-uuid-abc"

	reqBody := CreateMedicationRequest{
		Name:                   "Parol",
		Strength:               "500mg",
		Instructions:           "Günde 2 kez",
		FrequencyHours:         12,
		PrescriptionDocumentID: &prescriptionDocID,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	// Mock DB returning other user as owner
	mock.ExpectQuery("^SELECT patient_id, kind FROM documents WHERE id = \\$1").
		WithArgs(prescriptionDocID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "kind"}).
			AddRow(otherUserID, "prescription"))

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for foreign prescription, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestGetMedicationsList(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	req := httptest.NewRequest(http.MethodGet, "/api/medications", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	now := time.Now()
	lastTaken := now.Add(-6 * time.Hour) // 6 hours ago

	mock.ExpectQuery("SELECT m.id, m.name, m.strength, m.instructions, m.schedule").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "strength", "instructions", "schedule", "last_taken_at"}).
			AddRow("med-1", "Parol", "500mg", "Günde 2 kez", []byte(`{"frequency_hours": 12}`), lastTaken).
			AddRow("med-2", "Aspirin", "100mg", "Günde 1 kez", []byte(`{"frequency_hours": 24}`), sql.NullTime{}))

	a.GetMedicationsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp []MedicationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp) != 2 {
		t.Errorf("expected 2 plans, got %d", len(resp))
	}

	// Dynamic calculation verification
	// First item lastTakenAt = 6h ago, freq = 12h, nextScheduledFor should be lastTaken + 12h = now + 6h
	if resp[0].LastTakenAt == nil || resp[0].NextScheduledFor == nil {
		t.Error("expected LastTakenAt and NextScheduledFor to be populated")
	} else {
		expectedNext := lastTaken.Add(12 * time.Hour)
		if !resp[0].NextScheduledFor.Equal(expectedNext) {
			t.Errorf("unexpected nextScheduledFor calculation: got %v, expected %v", resp[0].NextScheduledFor, expectedNext)
		}
	}

	// Second item has not been taken yet, so NextScheduledFor and LastTakenAt should be nil
	if resp[1].LastTakenAt != nil || resp[1].NextScheduledFor != nil {
		t.Error("expected empty LastTakenAt and NextScheduledFor for untaken medication")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationTakenSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	medID := "med-uuid-abc"

	req := httptest.NewRequest(http.MethodPost, "/api/medications/"+medID+"/taken", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("medicationId", medID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT patient_id, schedule FROM medications WHERE id = \\$1 AND active = true").
		WithArgs(medID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "schedule"}).
			AddRow(userID, []byte(`{"frequency_hours": 12}`)))

	mock.ExpectExec("INSERT INTO medication_events").
		WithArgs(sqlmock.AnyArg(), medID, sqlmock.AnyArg(), "taken", sqlmock.AnyArg(), userID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PostMedicationTakenHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp TakenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// nextScheduledFor should be roughly NOW() + 12 hours
	expectedNextLower := time.Now().Add(11 * time.Hour)
	expectedNextUpper := time.Now().Add(13 * time.Hour)
	if resp.NextScheduledFor.Before(expectedNextLower) || resp.NextScheduledFor.After(expectedNextUpper) {
		t.Errorf("unexpected nextScheduledFor time calculation: %v", resp.NextScheduledFor)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationTakenForbidden(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	otherUserID := "user-uuid-999"
	medID := "med-uuid-abc"

	req := httptest.NewRequest(http.MethodPost, "/api/medications/"+medID+"/taken", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("medicationId", medID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	// Return otherUserID as patient_id
	mock.ExpectQuery("^SELECT patient_id, schedule FROM medications WHERE id = \\$1 AND active = true").
		WithArgs(medID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "schedule"}).
			AddRow(otherUserID, []byte(`{"frequency_hours": 12}`)))

	a.PostMedicationTakenHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}
