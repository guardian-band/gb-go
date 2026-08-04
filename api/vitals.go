package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

// VitalPacket represents the vitals data packet sent from the device.
type VitalPacket struct {
	UserID      string    `json:"userId"`
	HeartRate   int       `json:"heartRate"`
	BloodOxygen int       `json:"bloodOxygen"`
	RecordedAt  time.Time `json:"recordedAt"`
}

// VitalsPostHandler stores a vital packet in Redis for a specific user.
func (a *API) VitalsPostHandler(w http.ResponseWriter, r *http.Request) {
	var packet VitalPacket
	if err := json.NewDecoder(r.Body).Decode(&packet); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if packet.UserID == "" {
		http.Error(w, "missing userId", http.StatusBadRequest)
		return
	}

	// Validation constraints
	if packet.HeartRate < 20 || packet.HeartRate > 300 {
		http.Error(w, "heartRate must be between 20 and 300 bpm", http.StatusBadRequest)
		return
	}

	if packet.BloodOxygen < 0 || packet.BloodOxygen > 100 {
		http.Error(w, "bloodOxygen must be between 0 and 100 percent", http.StatusBadRequest)
		return
	}

	if packet.RecordedAt.IsZero() {
		http.Error(w, "missing recordedAt timestamp", http.StatusBadRequest)
		return
	}

	// Serialize packet to JSON
	packetJSON, err := json.Marshal(packet)
	if err != nil {
		http.Error(w, "failed to serialize vital data", http.StatusInternalServerError)
		return
	}

	// Save to Redis (no expiration)
	redisKey := "vitals:" + packet.UserID + ":latest"
	err = a.redis.Set(r.Context(), redisKey, packetJSON, 0).Err()
	if err != nil {
		http.Error(w, "failed to save vitals to cache: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// VitalsGetLatestHandler retrieves the latest vital packet stored in Redis.
func (a *API) VitalsGetLatestHandler(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userId")
	if userID == "" {
		http.Error(w, "missing userId path parameter", http.StatusBadRequest)
		return
	}

	redisKey := "vitals:" + userID + ":latest"
	packetJSON, err := a.redis.Get(r.Context(), redisKey).Result()
	if err == redis.Nil {
		http.Error(w, "no vitals found for the user", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "failed to retrieve vitals: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(packetJSON))
}
