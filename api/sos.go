package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// SOSIncidentRequest represents the payload for creating a new SOS incident.
type SOSIncidentRequest struct {
	Latitude       *float64 `json:"latitude"`
	Longitude      *float64 `json:"longitude"`
	AccuracyMeters *float64 `json:"accuracyMeters"`
	Address        *string  `json:"address"`
}

// SOSIncidentResponse represents the response when an SOS incident is successfully created.
type SOSIncidentResponse struct {
	ID             string    `json:"id"`
	PatientID      string    `json:"patientId"`
	Status         string    `json:"status"`
	Latitude       *float64  `json:"latitude"`
	Longitude      *float64  `json:"longitude"`
	AccuracyMeters *float64  `json:"accuracyMeters"`
	Address        *string   `json:"address"`
	StartedAt      time.Time `json:"startedAt"`
}

// PostSOSIncidentHandler triggers a new SOS incident and alerts emergency contacts.
func (a *API) PostSOSIncidentHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req SOSIncidentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 1. Validations
	if req.Latitude != nil {
		if *req.Latitude < -90 || *req.Latitude > 90 {
			http.Error(w, "invalid latitude, must be between -90 and 90", http.StatusBadRequest)
			return
		}
	}

	if req.Longitude != nil {
		if *req.Longitude < -180 || *req.Longitude > 180 {
			http.Error(w, "invalid longitude, must be between -180 and 180", http.StatusBadRequest)
			return
		}
	}

	if req.AccuracyMeters != nil {
		if *req.AccuracyMeters < 0 {
			http.Error(w, "invalid accuracyMeters, cannot be negative", http.StatusBadRequest)
			return
		}
	}

	incidentID := uuid.New().String()
	startedAt := time.Now()

	// Insert active SOS incident into database
	_, err := a.db.ExecContext(r.Context(), `
		INSERT INTO sos_incidents (id, patient_id, status, latitude, longitude, accuracy_meters, address, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		incidentID, userID, "active", req.Latitude, req.Longitude, req.AccuracyMeters, req.Address, startedAt)

	if err != nil {
		http.Error(w, "failed to create SOS incident: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Fetch patient emergency contacts from patient_links
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT display_name, phone 
		FROM patient_links 
		WHERE patient_id = $1 AND is_emergency_contact = true`,
		userID)

	if err != nil {
		// Log warning, but still return success for SOS creation since incident is recorded
		println("Warning fetching emergency contacts:", err.Error())
	} else {
		defer rows.Close()
		contacts := make([]EmergencyContact, 0)
		for rows.Next() {
			var displayName sql.NullString
			var phone sql.NullString
			if err := rows.Scan(&displayName, &phone); err == nil {
				contacts = append(contacts, EmergencyContact{
					DisplayName: displayName.String,
					Phone:       phone.String,
				})
			}
		}

		// Dispatch SOS alerts asynchronously
		if len(contacts) > 0 {
			go func() {
				_ = a.notificationService.SendSOSAlert(userID, contacts, incidentID)
			}()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SOSIncidentResponse{
		ID:             incidentID,
		PatientID:      userID,
		Status:         "active",
		Latitude:       req.Latitude,
		Longitude:      req.Longitude,
		AccuracyMeters: req.AccuracyMeters,
		Address:        req.Address,
		StartedAt:      startedAt,
	})
}

// PostSOSCancelHandler handles cancellation signal, changes state to cancelled, and sends "all clear" follow-up.
func (a *API) PostSOSCancelHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	incidentID := chi.URLParam(r, "incidentId")
	if incidentID == "" {
		http.Error(w, "missing incidentId parameter", http.StatusBadRequest)
		return
	}

	var patientID string
	var startedAt time.Time
	var status string

	err := a.db.QueryRowContext(r.Context(),
		"SELECT patient_id, started_at, status FROM sos_incidents WHERE id = $1",
		incidentID).Scan(&patientID, &startedAt, &status)

	if err == sql.ErrNoRows {
		http.Error(w, "SOS incident not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 403 Forbidden check: Validate SOS incident ownership
	if patientID != userID {
		http.Error(w, "forbidden: access to this SOS incident is denied", http.StatusForbidden)
		return
	}

	// Update SOS incident status in database
	_, err = a.db.ExecContext(r.Context(),
		"UPDATE sos_incidents SET status = 'cancelled', cancelled_at = $2 WHERE id = $1",
		incidentID, time.Now())
	if err != nil {
		http.Error(w, "failed to update SOS incident: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Fetch emergency contacts to send "all clear" notification
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT display_name, phone 
		FROM patient_links 
		WHERE patient_id = $1 AND is_emergency_contact = true`,
		userID)

	if err != nil {
		println("Warning fetching emergency contacts for cancel all clear:", err.Error())
	} else {
		defer rows.Close()
		contacts := make([]EmergencyContact, 0)
		for rows.Next() {
			var displayName sql.NullString
			var phone sql.NullString
			if err := rows.Scan(&displayName, &phone); err == nil {
				contacts = append(contacts, EmergencyContact{
					DisplayName: displayName.String,
					Phone:       phone.String,
				})
			}
		}

		// Dispatch All Clear alerts asynchronously
		if len(contacts) > 0 {
			go func() {
				_ = a.notificationService.SendAllClearAlert(userID, contacts, incidentID)
			}()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "cancellation request processed, all clear alert sent",
	})
}
