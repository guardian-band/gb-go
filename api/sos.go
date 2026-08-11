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

// SOSAllClearResponse represents the response when an SOS incident all-clear is processed.
type SOSAllClearResponse struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	AllClearAt *time.Time `json:"allClearAt"`
}

// PostSOSAllClearHandler handles "all clear" signal, updates all_clear_at, and sends "all clear" notification to contacts.
func (a *API) PostSOSAllClearHandler(w http.ResponseWriter, r *http.Request) {
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
	var status string
	var allClearAtNull sql.NullTime

	err := a.db.QueryRowContext(r.Context(),
		"SELECT patient_id, status, all_clear_at FROM sos_incidents WHERE id = $1",
		incidentID).Scan(&patientID, &status, &allClearAtNull)

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

	// Idempotency: check if all-clear is already set
	if allClearAtNull.Valid {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SOSAllClearResponse{
			ID:         incidentID,
			Status:     status,
			AllClearAt: &allClearAtNull.Time,
		})
		return
	}

	now := time.Now()

	// Update SOS incident all_clear_at in database (keeping the status intact)
	_, err = a.db.ExecContext(r.Context(),
		"UPDATE sos_incidents SET all_clear_at = $2 WHERE id = $1",
		incidentID, now)
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
		println("Warning fetching emergency contacts for all clear:", err.Error())
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
	json.NewEncoder(w).Encode(SOSAllClearResponse{
		ID:         incidentID,
		Status:     status,
		AllClearAt: &now,
	})
}
