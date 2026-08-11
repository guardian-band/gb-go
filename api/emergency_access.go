package api

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type EmergencyMedicalCard struct {
	BloodType     string                  `json:"bloodType"`
	CriticalFacts json.RawMessage         `json:"criticalFacts"`
	Medications   []EmergencyMedication   `json:"medications"`
	LatestVitals  *EmergencyLatestVitals  `json:"latestVitals"`
}

type EmergencyMedication struct {
	Name         string `json:"name"`
	Strength     string `json:"strength"`
	Instructions string `json:"instructions"`
}

type EmergencyLatestVitals struct {
	HeartRate   int       `json:"heartRate"`
	BloodOxygen int       `json:"bloodOxygen"`
	RecordedAt  time.Time `json:"recordedAt"`
}

type EmergencyAuditLog struct {
	Event       string    `json:"event"`
	ResponderID string    `json:"responderId"`
	PatientID   string    `json:"patientId"`
	SessionID   string    `json:"sessionId"`
	Timestamp   time.Time `json:"timestamp"`
	Allowed     bool      `json:"allowed"`
	Reason      string    `json:"reason,omitempty"`
}

// PostEmergencyAccessTokenHandler generates a short-lived, one-time QR token for the patient.
func (a *API) PostEmergencyAccessTokenHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Validate patient_profile exists
	var dummy string
	err := a.db.QueryRowContext(r.Context(), "SELECT user_id FROM patient_profiles WHERE user_id = $1", userID).Scan(&dummy)
	if err == sql.ErrNoRows {
		http.Error(w, "patient profile not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Generate cryptographically secure random 32-byte token
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		http.Error(w, "failed to generate secure token", http.StatusInternalServerError)
		return
	}
	rawToken := hex.EncodeToString(rawBytes)

	// SHA-256 hash of raw token for database storage
	hashBytes := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hashBytes[:])

	// Token expires in 5 minutes
	expiresAt := time.Now().Add(5 * time.Minute)

	_, err = a.db.ExecContext(r.Context(), `
		UPDATE patient_profiles
		SET qr_token_hash = $1, qr_token_expires_at = $2
		WHERE user_id = $3
	`, tokenHash, expiresAt, userID)

	if err != nil {
		http.Error(w, "failed to save token hash: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"token":     rawToken,
		"expiresAt": expiresAt,
	})
}

// PostEmergencyAccessRedeemHandler allows a responder to redeem an opaque QR token and establish a session.
func (a *API) PostEmergencyAccessRedeemHandler(w http.ResponseWriter, r *http.Request) {
	responderID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || responderID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		http.Error(w, "invalid or missing token in request body", http.StatusBadRequest)
		return
	}

	// SHA-256 hash to match DB records
	hashBytes := sha256.Sum256([]byte(req.Token))
	tokenHash := hex.EncodeToString(hashBytes[:])

	var patientID string
	var expiresAt time.Time
	err := a.db.QueryRowContext(r.Context(), `
		SELECT user_id, qr_token_expires_at
		FROM patient_profiles
		WHERE qr_token_hash = $1
	`, tokenHash).Scan(&patientID, &expiresAt)

	if err == sql.ErrNoRows {
		http.Error(w, "invalid or expired token", http.StatusBadRequest)
		return
	} else if err != nil {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Verify expiry
	if time.Now().After(expiresAt) {
		http.Error(w, "invalid or expired token", http.StatusBadRequest)
		return
	}

	// Enforce one-time use: invalidate token immediately
	_, err = a.db.ExecContext(r.Context(), `
		UPDATE patient_profiles
		SET qr_token_hash = NULL, qr_token_expires_at = NULL
		WHERE user_id = $1
	`, patientID)
	if err != nil {
		http.Error(w, "failed to invalidate token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create emergency session (lasts 15 minutes)
	sessionID := uuid.New().String()
	sessionExpiresAt := time.Now().Add(15 * time.Minute)

	_, err = a.db.ExecContext(r.Context(), `
		INSERT INTO emergency_access_sessions (id, patient_id, responder_id, created_at, expires_at)
		VALUES ($1, $2, $3, NOW(), $4)
	`, sessionID, patientID, responderID, sessionExpiresAt)

	if err != nil {
		http.Error(w, "failed to establish emergency session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"sessionId": sessionID,
		"expiresAt": sessionExpiresAt,
	})
}

// GetEmergencyAccessMedicalCardHandler returns the limited EmergencyMedicalCard for an active session.
func (a *API) GetEmergencyAccessMedicalCardHandler(w http.ResponseWriter, r *http.Request) {
	responderID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || responderID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		http.Error(w, "missing sessionId parameter", http.StatusBadRequest)
		return
	}

	var patientID string
	var sessionResponderID string
	var expiresAt time.Time
	var revokedAtNull sql.NullTime

	err := a.db.QueryRowContext(r.Context(), `
		SELECT patient_id, responder_id, expires_at, revoked_at
		FROM emergency_access_sessions
		WHERE id = $1
	`, sessionID).Scan(&patientID, &sessionResponderID, &expiresAt, &revokedAtNull)

	// Structured audit log helper
	logAudit := func(allowed bool, reason string) {
		audit := EmergencyAuditLog{
			Event:       "emergency_medical_card_accessed",
			ResponderID: responderID,
			PatientID:   patientID,
			SessionID:   sessionID,
			Timestamp:   time.Now(),
			Allowed:     allowed,
			Reason:      reason,
		}
		auditBytes, _ := json.Marshal(audit)
		log.Println(string(auditBytes)) // Emits structured audit log
	}

	if err == sql.ErrNoRows {
		logAudit(false, "session not found")
		http.Error(w, "emergency access session not found", http.StatusNotFound)
		return
	} else if err != nil {
		logAudit(false, "database error: "+err.Error())
		http.Error(w, "database query error", http.StatusInternalServerError)
		return
	}

	// Verify responder owns this session
	if sessionResponderID != responderID {
		logAudit(false, "forbidden: responder mismatch")
		http.Error(w, "forbidden: access to this emergency session is denied", http.StatusForbidden)
		return
	}

	// Verify session is active (not expired)
	if time.Now().After(expiresAt) {
		logAudit(false, "session expired")
		http.Error(w, "forbidden: emergency access session has expired", http.StatusForbidden)
		return
	}

	// Verify session is active (not revoked)
	if revokedAtNull.Valid && !revokedAtNull.Time.IsZero() {
		logAudit(false, "session revoked")
		http.Error(w, "forbidden: emergency access session was revoked", http.StatusForbidden)
		return
	}

	// Access allowed
	logAudit(true, "")

	// Query critical facts & blood type
	var bloodType sql.NullString
	var criticalFactsRaw []byte

	err = a.db.QueryRowContext(r.Context(), `
		SELECT blood_type, critical_facts
		FROM patient_profiles
		WHERE user_id = $1
	`, patientID).Scan(&bloodType, &criticalFactsRaw)
	if err != nil {
		http.Error(w, "failed to query patient profile info: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Query active medications
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT name, COALESCE(strength, ''), COALESCE(instructions, '')
		FROM medications
		WHERE patient_id = $1 AND active = true
	`, patientID)
	
	var medications []EmergencyMedication = []EmergencyMedication{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var med EmergencyMedication
			if err := rows.Scan(&med.Name, &med.Strength, &med.Instructions); err == nil {
				medications = append(medications, med)
			}
		}
	}

	// Fetch latest vitals from Redis
	var latestVitals *EmergencyLatestVitals
	redisKey := "vitals:" + patientID + ":latest"
	packetJSON, err := a.redis.Get(r.Context(), redisKey).Result()
	if err == nil {
		var vitalPacket VitalPacket
		if err := json.Unmarshal([]byte(packetJSON), &vitalPacket); err == nil {
			latestVitals = &EmergencyLatestVitals{
				HeartRate:   vitalPacket.HeartRate,
				BloodOxygen: vitalPacket.BloodOxygen,
				RecordedAt:  vitalPacket.RecordedAt,
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(EmergencyMedicalCard{
		BloodType:     bloodType.String,
		CriticalFacts: json.RawMessage(criticalFactsRaw),
		Medications:   medications,
		LatestVitals:  latestVitals,
	})
}

// DeleteEmergencyAccessSessionHandler allows the patient to revoke an active session.
func (a *API) DeleteEmergencyAccessSessionHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || patientID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		http.Error(w, "missing sessionId parameter", http.StatusBadRequest)
		return
	}

	var sessionPatientID string
	err := a.db.QueryRowContext(r.Context(), `
		SELECT patient_id FROM emergency_access_sessions WHERE id = $1
	`, sessionID).Scan(&sessionPatientID)

	if err == sql.ErrNoRows {
		http.Error(w, "emergency access session not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Verify caller is the patient of this session
	if sessionPatientID != patientID {
		http.Error(w, "forbidden: access to this emergency session is denied", http.StatusForbidden)
		return
	}

	_, err = a.db.ExecContext(r.Context(), `
		UPDATE emergency_access_sessions
		SET revoked_at = NOW()
		WHERE id = $1
	`, sessionID)

	if err != nil {
		http.Error(w, "failed to revoke session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "emergency access session revoked successfully",
	})
}
