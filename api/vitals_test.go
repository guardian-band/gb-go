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
	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

func TestVitalsPostSuccess(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer redisClient.Close()

	a := &API{redis: redisClient}
	userID := "user-123"

	packet := VitalPacket{
		HeartRate:   72,
		BloodOxygen: 98,
		RecordedAt:  time.Now().UTC(),
	}

	body, _ := json.Marshal(packet)
	req := httptest.NewRequest(http.MethodPost, "/api/vitals", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	a.VitalsPostHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}

	// Verify data is stored in latest key
	redisKey := "vitals:" + userID + ":latest"
	storedJSON, err := mr.Get(redisKey)
	if err != nil {
		t.Fatalf("vitals not found in redis cache: %v", err)
	}

	var storedPacket VitalPacket
	if err := json.Unmarshal([]byte(storedJSON), &storedPacket); err != nil {
		t.Fatalf("failed to deserialize packet: %v", err)
	}

	if storedPacket.HeartRate != packet.HeartRate || storedPacket.BloodOxygen != packet.BloodOxygen {
		t.Errorf("stored packet data mismatch, got %+v", storedPacket)
	}

	// Verify data is in stream
	streamLen, err := redisClient.XLen(context.Background(), "guardianband:telemetry").Result()
	if err != nil || streamLen != 1 {
		t.Errorf("expected stream length of 1, got %d, err: %v", streamLen, err)
	}
}

func TestVitalsPostInvalidRange(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer redisClient.Close()

	a := &API{redis: redisClient}
	userID := "user-123"

	invalidPackets := []VitalPacket{
		{HeartRate: 19, BloodOxygen: 98, RecordedAt: time.Now()},  // low heartRate
		{HeartRate: 301, BloodOxygen: 98, RecordedAt: time.Now()}, // high heartRate
		{HeartRate: 72, BloodOxygen: -1, RecordedAt: time.Now()},  // low oxygen
		{HeartRate: 72, BloodOxygen: 101, RecordedAt: time.Now()}, // high oxygen
		{HeartRate: 0, BloodOxygen: 0, RecordedAt: time.Now()},    // both zero
		{HeartRate: 72, BloodOxygen: 98},                          // missing recordedAt
	}

	for _, packet := range invalidPackets {
		body, _ := json.Marshal(packet)
		req := httptest.NewRequest(http.MethodPost, "/api/vitals", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
		rec := httptest.NewRecorder()

		a.VitalsPostHandler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("for packet %+v: expected status 400, got %d", packet, rec.Code)
		}
	}
}

func TestVitalsPostUnauthorizedAndDeviceMismatch(t *testing.T) {
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

	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer redisClient.Close()

	a := &API{db: db, redis: redisClient}
	userID := "user-123"

	packet := VitalPacket{
		HeartRate:   72,
		BloodOxygen: 98,
		RecordedAt:  time.Now().UTC(),
		DeviceSN:    "band-sn-456",
	}
	body, _ := json.Marshal(packet)

	// 1. Test unauthorized (missing token)
	req1 := httptest.NewRequest(http.MethodPost, "/api/vitals", bytes.NewReader(body))
	rec1 := httptest.NewRecorder()
	a.VitalsPostHandler(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec1.Code)
	}

	// 2. Test device SN mismatch (Forbidden)
	req2 := httptest.NewRequest(http.MethodPost, "/api/vitals", bytes.NewReader(body))
	req2 = req2.WithContext(context.WithValue(req2.Context(), UserContextKey, userID))
	rec2 := httptest.NewRecorder()

	// Return a mismatching band_identifier
	mock.ExpectQuery("^SELECT band_identifier FROM patient_profiles").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"band_identifier"}).
			AddRow("band-sn-different"))

	a.VitalsPostHandler(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec2.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestVitalsGetLatestSuccess(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer redisClient.Close()

	a := &API{redis: redisClient}

	packet := VitalPacket{
		UserID:      "user-123",
		HeartRate:   80,
		BloodOxygen: 95,
		RecordedAt:  time.Now().UTC(),
	}
	packetJSON, _ := json.Marshal(packet)

	// Pre-seed redis key
	redisKey := "vitals:" + packet.UserID + ":latest"
	mr.Set(redisKey, string(packetJSON))

	req := httptest.NewRequest(http.MethodGet, "/api/vitals/user-123/latest", nil)
	rec := httptest.NewRecorder()

	// Setup chi context for url parameters
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("userId", "user-123")
	
	// Inject Chi Context
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	a.VitalsGetLatestHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp VitalPacket
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.UserID != packet.UserID || resp.HeartRate != packet.HeartRate || resp.BloodOxygen != packet.BloodOxygen {
		t.Errorf("response data mismatch, got %+v", resp)
	}
}

func TestVitalsGetLatestNotFound(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer redisClient.Close()

	a := &API{redis: redisClient}

	req := httptest.NewRequest(http.MethodGet, "/api/vitals/non-existent/latest", nil)
	rec := httptest.NewRecorder()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("userId", "non-existent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	a.VitalsGetLatestHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}
