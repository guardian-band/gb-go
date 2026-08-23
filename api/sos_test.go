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

type mockNotificationService struct {
	sosCalled      bool
	allClearCalled bool
	recipientCount int
	lastIncidentID string
}

func (m *mockNotificationService) SendSOSAlert(patientID string, contacts []EmergencyContact, incidentID string) error {
	m.sosCalled = true
	m.recipientCount = len(contacts)
	m.lastIncidentID = incidentID
	return nil
}

func (m *mockNotificationService) SendAllClearAlert(patientID string, contacts []EmergencyContact, incidentID string) error {
	m.allClearCalled = true
	m.recipientCount = len(contacts)
	m.lastIncidentID = incidentID
	return nil
}

func TestPostSOSIncidentSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	lat := 41.0082
	lon := 28.9784
	accuracy := 15.0
	address := "Kadıköy, İstanbul"

	reqBody := SOSIncidentRequest{
		Latitude:       &lat,
		Longitude:      &lon,
		AccuracyMeters: &accuracy,
		Address:        &address,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/sos/incidents", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	// Patient profile existence check mock
	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))

	mock.ExpectBegin()

	mock.ExpectExec("INSERT INTO sos_incidents").
		WithArgs(sqlmock.AnyArg(), userID, "active", lat, lon, accuracy, address, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectQuery("(?s)SELECT display_name, phone, NULL::uuid AS linked_user_id.*patient_relationships.*pr\\.active = TRUE.*pr\\.revoked_at IS NULL").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"display_name", "phone", "linked_user_id"}).
			AddRow("Ahmet Veli", "+905551111111", nil))

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM notification_outbox").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec("INSERT INTO notification_outbox").
		WithArgs(sqlmock.AnyArg(), "sos", sqlmock.AnyArg(), nil, "+905551111111", "sms", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	a.PostSOSIncidentHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp SOSIncidentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "active" || *resp.Latitude != lat {
		t.Errorf("unexpected incident details: %+v", resp)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostSOSIncidentInvalidCoords(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	lat := 95.0 // invalid latitude (> 90)
	reqBody := SOSIncidentRequest{
		Latitude: &lat,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/sos/incidents", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	a.PostSOSIncidentHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid latitude, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostSOSAllClearSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	incidentID := "incident-uuid-abc"

	req := httptest.NewRequest(http.MethodPost, "/api/sos/incidents/"+incidentID+"/all-clear", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("incidentId", incidentID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT patient_id, status, all_clear_at FROM sos_incidents WHERE id = \\$1").
		WithArgs(incidentID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "status", "all_clear_at"}).
			AddRow(userID, "active", nil))

	mock.ExpectBegin()

	mock.ExpectExec("UPDATE sos_incidents SET all_clear_at = \\$2 WHERE id = \\$1").
		WithArgs(incidentID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectQuery("(?s)SELECT display_name, phone, NULL::uuid AS linked_user_id.*patient_relationships.*pr\\.active = TRUE.*pr\\.revoked_at IS NULL").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"display_name", "phone", "linked_user_id"}).
			AddRow("Ahmet Veli", "+905551111111", nil))

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM notification_outbox").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec("INSERT INTO notification_outbox").
		WithArgs(sqlmock.AnyArg(), "all_clear", sqlmock.AnyArg(), nil, "+905551111111", "sms", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	a.PostSOSAllClearHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostSOSAllClearIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	incidentID := "incident-uuid-abc"

	req := httptest.NewRequest(http.MethodPost, "/api/sos/incidents/"+incidentID+"/all-clear", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("incidentId", incidentID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	alreadyClearedAt := time.Now().Add(-10 * time.Second)

	mock.ExpectQuery("^SELECT patient_id, status, all_clear_at FROM sos_incidents WHERE id = \\$1").
		WithArgs(incidentID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "status", "all_clear_at"}).
			AddRow(userID, "active", alreadyClearedAt))

	a.PostSOSAllClearHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostSOSAllClearForbidden(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	otherUserID := "user-uuid-999"
	incidentID := "incident-uuid-abc"

	req := httptest.NewRequest(http.MethodPost, "/api/sos/incidents/"+incidentID+"/all-clear", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("incidentId", incidentID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	// Return otherUserID as patient_id
	mock.ExpectQuery("^SELECT patient_id, status, all_clear_at FROM sos_incidents WHERE id = \\$1").
		WithArgs(incidentID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "status", "all_clear_at"}).
			AddRow(otherUserID, "active", nil))

	a.PostSOSAllClearHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostSOSIncidentProfileNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	reqBody := SOSIncidentRequest{}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/sos/incidents", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1").
		WithArgs(userID).
		WillReturnError(sql.ErrNoRows)

	a.PostSOSIncidentHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["message"] != "patient profile not found" {
		t.Errorf("expected 'patient profile not found', got %q", resp["message"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}
