package api

import (
	"bytes"
	"context"
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

	ns := &mockNotificationService{}
	a := &API{db: db, notificationService: ns}
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

	mock.ExpectExec("INSERT INTO sos_incidents").
		WithArgs(sqlmock.AnyArg(), userID, "active", lat, lon, accuracy, address, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectQuery("SELECT display_name, phone FROM patient_links").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"display_name", "phone"}).
			AddRow("Ahmet Veli", "+905551111111").
			AddRow("Ayşe Yılmaz", "+905552222222"))

	a.PostSOSIncidentHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}

	var resp SOSIncidentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "active" || *resp.Latitude != lat {
		t.Errorf("unexpected incident details: %+v", resp)
	}

	// Give goroutine a moment to run SendSOSAlert
	time.Sleep(10 * time.Millisecond)

	if !ns.sosCalled {
		t.Error("expected notification service SendSOSAlert to be called")
	}
	if ns.recipientCount != 2 {
		t.Errorf("expected 2 emergency contacts notified, got %d", ns.recipientCount)
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

func TestPostSOSCancelSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	ns := &mockNotificationService{}
	a := &API{db: db, notificationService: ns}
	userID := "user-uuid-123"
	incidentID := "incident-uuid-abc"

	req := httptest.NewRequest(http.MethodPost, "/api/sos/incidents/"+incidentID+"/cancel", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("incidentId", incidentID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	startedAt := time.Now().Add(-1 * time.Hour) // 1 hour ago (valid, since no time limit now)

	mock.ExpectQuery("^SELECT patient_id, started_at, status FROM sos_incidents WHERE id = \\$1").
		WithArgs(incidentID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "started_at", "status"}).
			AddRow(userID, startedAt, "active"))

	mock.ExpectExec("UPDATE sos_incidents SET status = 'cancelled'").
		WithArgs(incidentID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectQuery("SELECT display_name, phone FROM patient_links").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"display_name", "phone"}).
			AddRow("Ahmet Veli", "+905551111111"))

	a.PostSOSCancelHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Give goroutine a moment to run SendAllClearAlert
	time.Sleep(10 * time.Millisecond)

	if !ns.allClearCalled {
		t.Error("expected notification service SendAllClearAlert to be called")
	}
	if ns.recipientCount != 1 {
		t.Errorf("expected 1 emergency contact notified, got %d", ns.recipientCount)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostSOSCancelForbidden(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	otherUserID := "user-uuid-999"
	incidentID := "incident-uuid-abc"

	req := httptest.NewRequest(http.MethodPost, "/api/sos/incidents/"+incidentID+"/cancel", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("incidentId", incidentID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	startedAt := time.Now().Add(-10 * time.Second)

	// Return otherUserID as patient_id
	mock.ExpectQuery("^SELECT patient_id, started_at, status FROM sos_incidents WHERE id = \\$1").
		WithArgs(incidentID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "started_at", "status"}).
			AddRow(otherUserID, startedAt, "active"))

	a.PostSOSCancelHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}
