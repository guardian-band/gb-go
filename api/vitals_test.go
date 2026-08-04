package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

	packet := VitalPacket{
		UserID:      "user-123",
		HeartRate:   72,
		BloodOxygen: 98,
		RecordedAt:  time.Now().UTC(),
	}

	body, _ := json.Marshal(packet)
	req := httptest.NewRequest(http.MethodPost, "/api/vitals", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	a.VitalsPostHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}

	// Verify data is stored in miniredis
	redisKey := "vitals:" + packet.UserID + ":latest"
	storedJSON, err := mr.Get(redisKey)
	if err != nil {
		t.Fatalf("vitals not found in redis: %v", err)
	}

	var storedPacket VitalPacket
	if err := json.Unmarshal([]byte(storedJSON), &storedPacket); err != nil {
		t.Fatalf("failed to deserialize packet: %v", err)
	}

	if storedPacket.HeartRate != packet.HeartRate || storedPacket.BloodOxygen != packet.BloodOxygen {
		t.Errorf("stored packet data mismatch, got %+v", storedPacket)
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

	invalidPackets := []VitalPacket{
		{UserID: "user-123", HeartRate: 19, BloodOxygen: 98, RecordedAt: time.Now()},  // low heartRate
		{UserID: "user-123", HeartRate: 301, BloodOxygen: 98, RecordedAt: time.Now()}, // high heartRate
		{UserID: "user-123", HeartRate: 72, BloodOxygen: -1, RecordedAt: time.Now()},  // low oxygen
		{UserID: "user-123", HeartRate: 72, BloodOxygen: 101, RecordedAt: time.Now()}, // high oxygen
		{UserID: "", HeartRate: 72, BloodOxygen: 98, RecordedAt: time.Now()},          // missing userId
		{UserID: "user-123", HeartRate: 72, BloodOxygen: 98},                          // missing recordedAt
	}

	for _, packet := range invalidPackets {
		body, _ := json.Marshal(packet)
		req := httptest.NewRequest(http.MethodPost, "/api/vitals", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		a.VitalsPostHandler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("for packet %+v: expected status 400, got %d", packet, rec.Code)
		}
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
