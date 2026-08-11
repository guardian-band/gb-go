package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

func TestPostEmergencyAccessTokenSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "patient-uuid-123"

	req := httptest.NewRequest(http.MethodPost, "/api/emergency-access/tokens", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT user_id FROM patient_profiles WHERE user_id = \\$1").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(userID))

	mock.ExpectExec("^UPDATE patient_profiles SET qr_token_hash = \\$1, qr_token_expires_at = \\$2 WHERE user_id = \\$3").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), userID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PostEmergencyAccessTokenHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	token, ok := resp["token"].(string)
	if !ok || len(token) != 64 { // 32 bytes hex encoded is 64 chars
		t.Errorf("expected 64 chars hex token, got %s", token)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestPostEmergencyAccessRedeemSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	responderID := "responder-uuid-456"
	patientID := "patient-uuid-123"
	rawToken := "abc123xyz456abc123xyz456abc123xy"
	hashBytes := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hashBytes[:])

	reqBody := map[string]string{"token": rawToken}
	bodyJSON, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/emergency-access/redeem", bytes.NewBuffer(bodyJSON))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, responderID))
	rec := httptest.NewRecorder()

	expiresAt := time.Now().Add(5 * time.Minute)

	mock.ExpectQuery("^SELECT user_id, qr_token_expires_at FROM patient_profiles WHERE qr_token_hash = \\$1").
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "qr_token_expires_at"}).AddRow(patientID, expiresAt))

	// Enforce one-time use invalidation
	mock.ExpectExec("^UPDATE patient_profiles SET qr_token_hash = NULL, qr_token_expires_at = NULL WHERE user_id = \\$1").
		WithArgs(patientID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Create emergency session
	mock.ExpectExec("^INSERT INTO emergency_access_sessions").
		WithArgs(sqlmock.AnyArg(), patientID, responderID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PostEmergencyAccessRedeemHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if _, ok := resp["sessionId"].(string); !ok {
		t.Error("expected sessionId in response")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestPostEmergencyAccessRedeemExpired(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	responderID := "responder-uuid-456"
	rawToken := "abc123xyz456abc123xyz456abc123xy"
	hashBytes := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hashBytes[:])

	reqBody := map[string]string{"token": rawToken}
	bodyJSON, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/emergency-access/redeem", bytes.NewBuffer(bodyJSON))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, responderID))
	rec := httptest.NewRecorder()

	// 10 minutes ago (expired)
	expiredAt := time.Now().Add(-10 * time.Minute)

	mock.ExpectQuery("^SELECT user_id, qr_token_expires_at FROM patient_profiles WHERE qr_token_hash = \\$1").
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "qr_token_expires_at"}).AddRow("patient-123", expiredAt))

	a.PostEmergencyAccessRedeemHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for expired token, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestGetEmergencyAccessMedicalCardSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	a := &API{db: db, redis: redisClient}
	responderID := "responder-uuid-456"
	patientID := "patient-uuid-123"
	sessionID := "session-uuid-abc"

	req := httptest.NewRequest(http.MethodGet, "/api/emergency-access/sessions/"+sessionID+"/medical-card", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, responderID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", sessionID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	expiresAt := time.Now().Add(10 * time.Minute)

	// Pre-seed Redis cache with latest vitals
	vitalPacket := map[string]interface{}{
		"userId":      patientID,
		"heartRate":   78,
		"bloodOxygen": 99,
		"recordedAt":  time.Now().Format(time.RFC3339),
	}
	packetJSON, _ := json.Marshal(vitalPacket)
	redisKey := "vitals:" + patientID + ":latest"
	_ = mr.Set(redisKey, string(packetJSON))

	// 1. Session lookup
	mock.ExpectQuery("^SELECT patient_id, responder_id, expires_at, revoked_at FROM emergency_access_sessions WHERE id = \\$1").
		WithArgs(sessionID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "responder_id", "expires_at", "revoked_at"}).
			AddRow(patientID, responderID, expiresAt, nil))

	// 2. Patient profile lookup
	mock.ExpectQuery("^SELECT blood_type, critical_facts FROM patient_profiles WHERE user_id = \\$1").
		WithArgs(patientID).
		WillReturnRows(sqlmock.NewRows([]string{"blood_type", "critical_facts"}).
			AddRow("AB-", `{"allergies":["Nuts"]}`))

	// 3. Active medications lookup
	mock.ExpectQuery("^SELECT name, COALESCE\\(strength, ''\\), COALESCE\\(instructions, ''\\) FROM medications").
		WithArgs(patientID).
		WillReturnRows(sqlmock.NewRows([]string{"name", "strength", "instructions"}).
			AddRow("Lisinopril", "10mg", "Once daily"))

	a.GetEmergencyAccessMedicalCardHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp EmergencyMedicalCard
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.BloodType != "AB-" {
		t.Errorf("expected bloodType 'AB-', got %s", resp.BloodType)
	}

	if len(resp.Medications) != 1 || resp.Medications[0].Name != "Lisinopril" {
		t.Errorf("unexpected medications returned: %+v", resp.Medications)
	}

	if resp.LatestVitals == nil || resp.LatestVitals.HeartRate != 78 || resp.LatestVitals.BloodOxygen != 99 {
		t.Errorf("unexpected vitals returned: %+v", resp.LatestVitals)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestGetEmergencyAccessMedicalCardForbidden(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	responderID := "responder-uuid-456"
	wrongResponderID := "wrong-responder-uuid"
	patientID := "patient-uuid-123"
	sessionID := "session-uuid-abc"

	// Subtest 1: Wrong Responder Mismatch
	t.Run("WrongResponder", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/emergency-access/sessions/"+sessionID+"/medical-card", nil)
		req = req.WithContext(context.WithValue(req.Context(), UserContextKey, wrongResponderID))

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("sessionId", sessionID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rec := httptest.NewRecorder()
		expiresAt := time.Now().Add(10 * time.Minute)

		mock.ExpectQuery("^SELECT patient_id, responder_id, expires_at, revoked_at FROM emergency_access_sessions WHERE id = \\$1").
			WithArgs(sessionID).
			WillReturnRows(sqlmock.NewRows([]string{"patient_id", "responder_id", "expires_at", "revoked_at"}).
				AddRow(patientID, responderID, expiresAt, nil))

		a.GetEmergencyAccessMedicalCardHandler(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected status 403, got %d", rec.Code)
		}
	})

	// Subtest 2: Revoked Session
	t.Run("RevokedSession", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/emergency-access/sessions/"+sessionID+"/medical-card", nil)
		req = req.WithContext(context.WithValue(req.Context(), UserContextKey, responderID))

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("sessionId", sessionID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rec := httptest.NewRecorder()
		expiresAt := time.Now().Add(10 * time.Minute)
		revokedAt := time.Now().Add(-1 * time.Minute)

		mock.ExpectQuery("^SELECT patient_id, responder_id, expires_at, revoked_at FROM emergency_access_sessions WHERE id = \\$1").
			WithArgs(sessionID).
			WillReturnRows(sqlmock.NewRows([]string{"patient_id", "responder_id", "expires_at", "revoked_at"}).
				AddRow(patientID, responderID, expiresAt, revokedAt))

		a.GetEmergencyAccessMedicalCardHandler(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected status 403, got %d", rec.Code)
		}
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestDeleteEmergencyAccessSessionSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	patientID := "patient-uuid-123"
	sessionID := "session-uuid-abc"

	req := httptest.NewRequest(http.MethodDelete, "/api/emergency-access/sessions/"+sessionID, nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, patientID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", sessionID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT patient_id FROM emergency_access_sessions WHERE id = \\$1").
		WithArgs(sessionID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id"}).AddRow(patientID))

	mock.ExpectExec("^UPDATE emergency_access_sessions SET revoked_at = NOW\\(\\) WHERE id = \\$1").
		WithArgs(sessionID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.DeleteEmergencyAccessSessionHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}
