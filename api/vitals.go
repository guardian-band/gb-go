package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

// VitalPacket represents the vitals data packet sent from the device.
type VitalPacket struct {
	UserID      string    `json:"userId,omitempty"`
	HeartRate   int       `json:"heartRate"`
	BloodOxygen int       `json:"bloodOxygen"`
	RecordedAt  time.Time `json:"recordedAt"`
	DeviceSN    string    `json:"deviceSn,omitempty"`
	ReadingID   string    `json:"readingId,omitempty"`
}

// VitalsPostHandler stores a vital packet in Redis cache and pushes it to guardianband:telemetry Stream.
func (a *API) VitalsPostHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var packet VitalPacket
	if err := json.NewDecoder(r.Body).Decode(&packet); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 1. Validations
	if packet.HeartRate == 0 && packet.BloodOxygen == 0 {
		http.Error(w, "at least heartRate or bloodOxygen must be provided", http.StatusBadRequest)
		return
	}

	if packet.HeartRate != 0 {
		if packet.HeartRate < 20 || packet.HeartRate > 300 {
			http.Error(w, "heartRate must be between 20 and 300 bpm", http.StatusBadRequest)
			return
		}
	}

	if packet.BloodOxygen != 0 {
		if packet.BloodOxygen < 0 || packet.BloodOxygen > 100 {
			http.Error(w, "bloodOxygen must be between 0 and 100 percent", http.StatusBadRequest)
			return
		}
	}

	if packet.RecordedAt.IsZero() {
		http.Error(w, "missing recordedAt timestamp", http.StatusBadRequest)
		return
	}

	// 2. Device validation if deviceSn is provided
	if packet.DeviceSN != "" {
		var bandIdentifier sql.NullString
		err := a.db.QueryRowContext(r.Context(),
			"SELECT band_identifier FROM patient_profiles WHERE user_id = $1",
			userID).Scan(&bandIdentifier)

		if err == sql.ErrNoRows {
			http.Error(w, "patient profile not found", http.StatusForbidden)
			return
		} else if err != nil {
			http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if !bandIdentifier.Valid || bandIdentifier.String != packet.DeviceSN {
			http.Error(w, "forbidden: device does not belong to the patient", http.StatusForbidden)
			return
		}
	}

	// 3. Write raw event to Redis Stream (guardianband:telemetry)
	streamArgs := map[string]interface{}{
		"patient_id":  userID,
		"recorded_at": packet.RecordedAt.Format(time.RFC3339),
	}
	if packet.HeartRate != 0 {
		streamArgs["heart_rate"] = strconv.Itoa(packet.HeartRate)
	}
	if packet.BloodOxygen != 0 {
		streamArgs["blood_oxygen"] = strconv.Itoa(packet.BloodOxygen)
	}
	if packet.DeviceSN != "" {
		streamArgs["device_sn"] = packet.DeviceSN
	}
	if packet.ReadingID != "" {
		streamArgs["reading_id"] = packet.ReadingID
	}

	err := a.redis.XAdd(r.Context(), &redis.XAddArgs{
		Stream: "guardianband:telemetry",
		Values: streamArgs,
	}).Err()
	if err != nil {
		http.Error(w, "failed to push vitals to stream: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 4. Update the latest vitals cache for compatibility
	packet.UserID = userID
	packetJSON, err := json.Marshal(packet)
	if err != nil {
		http.Error(w, "failed to serialize vital data", http.StatusInternalServerError)
		return
	}

	redisKey := "vitals:" + userID + ":latest"
	err = a.redis.Set(r.Context(), redisKey, packetJSON, 0).Err()
	if err != nil {
		http.Error(w, "failed to save vitals to cache: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// VitalsGetLatestHandler retrieves the latest vital packet stored in Redis.
func (a *API) VitalsGetLatestHandler(w http.ResponseWriter, r *http.Request) {
	authenticatedUserID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || authenticatedUserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	userID := chi.URLParam(r, "userId")
	if userID == "" {
		http.Error(w, "missing userId path parameter", http.StatusBadRequest)
		return
	}

	// 1. Authorization checks
	if userID != authenticatedUserID {
		var isAuthorized bool
		err := a.db.QueryRowContext(r.Context(), `
			SELECT EXISTS(
				SELECT 1 FROM patient_links
				WHERE patient_id = $1 AND linked_user_id = $2 AND can_monitor = true
			)
		`, userID, authenticatedUserID).Scan(&isAuthorized)
		if err != nil {
			http.Error(w, "database query error", http.StatusInternalServerError)
			return
		}
		if !isAuthorized {
			http.Error(w, "patient not found", http.StatusNotFound)
			return
		}
	}

	// 2. Fetch from Redis cache
	redisKey := "vitals:" + userID + ":latest"
	packetJSON, err := a.redis.Get(r.Context(), redisKey).Result()
	if err == redis.Nil {
		http.Error(w, "patient not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "failed to retrieve vitals: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(packetJSON))
}
