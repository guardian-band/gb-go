package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
)

type mockNotificationProvider struct {
	sendFunc func(ctx context.Context, channel string, address string, payload map[string]interface{}) error
}

func (m *mockNotificationProvider) Send(ctx context.Context, channel string, address string, payload map[string]interface{}) error {
	if m.sendFunc != nil {
		return m.sendFunc(ctx, channel, address, payload)
	}
	return nil
}

func TestPutNotificationEndpointSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	endpointID := "00000000-0000-0000-0000-000000000001"

	reqBody := map[string]string{
		"channel": "push",
		"token":   "super-secret-token-value-here",
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPut, "/api/notification-endpoints/"+endpointID, bytes.NewBuffer(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("endpointId", endpointID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	// 1. Ownership check (no rows)
	mock.ExpectQuery("^SELECT user_id FROM notification_endpoints WHERE id = \\$1").
		WithArgs(endpointID).
		WillReturnError(sql.ErrNoRows)

	// 2. Count check
	mock.ExpectQuery("^SELECT COUNT\\(\\*\\) FROM notification_endpoints WHERE id = \\$1").
		WithArgs(endpointID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	// 3. Upsert (Insert since count = 0)
	mock.ExpectExec("^INSERT INTO notification_endpoints").
		WithArgs(endpointID, userID, "push", "super-secret-token-value-here").
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PutNotificationEndpointHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	// Token must be masked!
	token := resp["token"].(string)
	if token == "super-secret-token-value-here" {
		t.Error("expected masked token, got raw value")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestPutNotificationEndpointForbidden(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	wrongOwnerID := "wrong-user-uuid"
	endpointID := "00000000-0000-0000-0000-000000000001"

	reqBody := map[string]string{
		"channel": "push",
		"token":   "some-token",
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPut, "/api/notification-endpoints/"+endpointID, bytes.NewBuffer(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("endpointId", endpointID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	// Ownership check matches another user
	mock.ExpectQuery("^SELECT user_id FROM notification_endpoints WHERE id = \\$1").
		WithArgs(endpointID).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(wrongOwnerID))

	a.PutNotificationEndpointHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestDeleteNotificationEndpointSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	endpointID := "00000000-0000-0000-0000-000000000001"

	req := httptest.NewRequest(http.MethodDelete, "/api/notification-endpoints/"+endpointID, nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("endpointId", endpointID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT user_id FROM notification_endpoints WHERE id = \\$1").
		WithArgs(endpointID).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(userID))

	mock.ExpectExec("^DELETE FROM notification_endpoints WHERE id = \\$1").
		WithArgs(endpointID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.DeleteNotificationEndpointHandler(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestPostSOSIncidentTransactionalOutbox(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "patient-uuid-123"
	linkedUser := "linked-user-456"

	reqBody := map[string]interface{}{}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/sos/incidents", bytes.NewBuffer(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	// 1. Tx Begin
	mock.ExpectBegin()

	// 2. Insert incident
	mock.ExpectExec("^INSERT INTO sos_incidents").
		WithArgs(sqlmock.AnyArg(), userID, "active", nil, nil, nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// 3. Query emergency contacts
	mock.ExpectQuery("^SELECT display_name, COALESCE\\(phone, ''\\), linked_user_id FROM patient_links").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"display_name", "phone", "linked_user_id"}).
			AddRow("Mom", "+905550001122", linkedUser))

	// 4. Query recipient's active endpoints
	mock.ExpectQuery("^SELECT channel, token FROM notification_endpoints").
		WithArgs(linkedUser).
		WillReturnRows(sqlmock.NewRows([]string{"channel", "token"}).
			AddRow("push", "mom-device-token"))

	// Idempotency check
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM notification_outbox").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	// 5. Insert outbox record
	mock.ExpectExec("^INSERT INTO notification_outbox").
		WithArgs(sqlmock.AnyArg(), "sos", sqlmock.AnyArg(), linkedUser, "mom-device-token", "push", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// 6. Tx Commit
	mock.ExpectCommit()

	a.PostSOSIncidentHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d, body: %s", rec.Code, rec.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestNotificationWorkerExecution(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	sentCalled := false
	provider := &mockNotificationProvider{
		sendFunc: func(ctx context.Context, channel string, address string, payload map[string]interface{}) error {
			if address == "device-token-abc" && channel == "push" {
				sentCalled = true
			}
			return nil
		},
	}

	a := &API{db: db, notificationProvider: provider}
	outboxID := "outbox-uuid-111"

	mock.ExpectBegin()

	// Claim row using SELECT FOR UPDATE SKIP LOCKED
	mock.ExpectQuery("^SELECT id, event_type, incident_id, recipient_id, recipient_address, channel, payload, attempt_count FROM notification_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"id", "event_type", "incident_id", "recipient_id", "recipient_address", "channel", "payload", "attempt_count"}).
			AddRow(outboxID, "sos", "incident-123", "recipient-123", "device-token-abc", "push", []byte(`{"incidentId":"incident-123"}`), 0))

	// Update outbox to sent
	mock.ExpectExec("^UPDATE notification_outbox SET status = 'sent'").
		WithArgs(outboxID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	err = a.RunNotificationWorkerOnce(context.Background())
	if err != nil {
		t.Fatalf("worker error: %v", err)
	}

	if !sentCalled {
		t.Error("expected provider Send to be called")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestNotificationWorkerRetryExponentialBackoff(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	provider := &mockNotificationProvider{
		sendFunc: func(ctx context.Context, channel string, address string, payload map[string]interface{}) error {
			return errors.New("temporary network timeout error")
		},
	}

	a := &API{db: db, notificationProvider: provider}
	outboxID := "outbox-uuid-111"

	mock.ExpectBegin()

	mock.ExpectQuery("^SELECT id, event_type, incident_id, recipient_id, recipient_address, channel, payload, attempt_count FROM notification_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"id", "event_type", "incident_id", "recipient_id", "recipient_address", "channel", "payload", "attempt_count"}).
			AddRow(outboxID, "sos", "incident-123", "recipient-123", "device-token-abc", "push", []byte(`{"incidentId":"incident-123"}`), 1))

	// Update outbox status to failed, increment attempt_count to 2, next_attempt = time.Now() + 60s (15s * 2^2)
	mock.ExpectExec("^UPDATE notification_outbox SET status = 'failed'").
		WithArgs(outboxID, 2, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	err = a.RunNotificationWorkerOnce(context.Background())
	if err != nil {
		t.Fatalf("worker error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestNotificationWorkerTerminalFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	provider := &mockNotificationProvider{
		sendFunc: func(ctx context.Context, channel string, address string, payload map[string]interface{}) error {
			return &TerminalError{Err: errors.New("invalid or expired push token")}
		},
	}

	a := &API{db: db, notificationProvider: provider}
	outboxID := "outbox-uuid-111"
	token := "invalid-token-xyz"

	mock.ExpectBegin()

	mock.ExpectQuery("^SELECT id, event_type, incident_id, recipient_id, recipient_address, channel, payload, attempt_count FROM notification_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"id", "event_type", "incident_id", "recipient_id", "recipient_address", "channel", "payload", "attempt_count"}).
			AddRow(outboxID, "sos", "incident-123", "recipient-123", token, "push", []byte(`{"incidentId":"incident-123"}`), 0))

	// Update outbox status to terminal_failed
	mock.ExpectExec("^UPDATE notification_outbox SET status = 'terminal_failed'").
		WithArgs(outboxID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Deactivate the endpoint
	mock.ExpectExec("^UPDATE notification_endpoints SET active = false").
		WithArgs(token).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	err = a.RunNotificationWorkerOnce(context.Background())
	if err != nil {
		t.Fatalf("worker error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}
