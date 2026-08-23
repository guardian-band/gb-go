package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// MedicationCatalogItem represents an item in the medication catalog.
type MedicationCatalogItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Strength string `json:"strength"`
}

// CreateMedicationRequest represents the payload for creating a new medication plan.
type CreateMedicationRequest struct {
	Name                   string  `json:"name"`
	Strength               string  `json:"strength"`
	Instructions           string  `json:"instructions"`
	FrequencyHours         int     `json:"frequencyHours"`
	PrescriptionDocumentID *string `json:"prescriptionDocumentId"`
}

// MedicationResponse represents the response details of a patient's medication plan.
type MedicationResponse struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Strength         string     `json:"strength"`
	Instructions     string     `json:"instructions"`
	FrequencyHours   int        `json:"frequencyHours"`
	LastTakenAt      *time.Time `json:"lastTakenAt"`
	NextScheduledFor *time.Time `json:"nextScheduledFor"`
}

// TakenResponse represents the response after recording a dose action.
type TakenResponse struct {
	NextScheduledFor time.Time `json:"nextScheduledFor"`
}

// ScheduleDetails helper for JSONB schedule metadata.
type ScheduleDetails struct {
	FrequencyHours int `json:"frequency_hours"`
}

// GetMedicationCatalogHandler returns matching medication catalog items.
func (a *API) GetMedicationCatalogHandler(w http.ResponseWriter, r *http.Request) {
	searchQuery := r.URL.Query().Get("search")

	var rows *sql.Rows
	var err error

	if searchQuery != "" {
		rows, err = a.db.QueryContext(r.Context(),
			"SELECT id, name, strength FROM medication_catalog WHERE name ILIKE $1 ORDER BY name ASC",
			"%"+searchQuery+"%")
	} else {
		rows, err = a.db.QueryContext(r.Context(),
			"SELECT id, name, strength FROM medication_catalog ORDER BY name ASC")
	}

	if err != nil {
		http.Error(w, "failed to query medication catalog: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	catalog := make([]MedicationCatalogItem, 0)
	for rows.Next() {
		var item MedicationCatalogItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Strength); err != nil {
			http.Error(w, "failed to scan catalog item: "+err.Error(), http.StatusInternalServerError)
			return
		}
		catalog = append(catalog, item)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(catalog)
}

// PostMedicationHandler creates a new medication plan for the patient.
func (a *API) PostMedicationHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req CreateMedicationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 1. Validations
	if req.Name == "" {
		http.Error(w, "medication name is required", http.StatusBadRequest)
		return
	}

	if req.FrequencyHours != 6 && req.FrequencyHours != 8 && req.FrequencyHours != 12 && req.FrequencyHours != 24 {
		http.Error(w, "invalid frequencyHours, must be 6, 8, 12, or 24 hours", http.StatusBadRequest)
		return
	}

	var docID sql.NullString
	if req.PrescriptionDocumentID != nil && *req.PrescriptionDocumentID != "" {
		// Verify prescription document existence and patient ownership
		var docOwner string
		var docKind string
		err := a.db.QueryRowContext(r.Context(),
			"SELECT patient_id, kind FROM documents WHERE id = $1",
			*req.PrescriptionDocumentID).Scan(&docOwner, &docKind)

		if err == sql.ErrNoRows {
			http.Error(w, "prescription document not found", http.StatusBadRequest)
			return
		} else if err != nil {
			http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if docKind != "prescription" {
			http.Error(w, "provided document is not a prescription type", http.StatusBadRequest)
			return
		}

		if docOwner != userID {
			http.Error(w, "forbidden: prescription document belongs to another user", http.StatusForbidden)
			return
		}
		docID.String = *req.PrescriptionDocumentID
		docID.Valid = true
	}

	// Serialize schedule to JSONB
	scheduleJSON, err := json.Marshal(ScheduleDetails{FrequencyHours: req.FrequencyHours})
	if err != nil {
		http.Error(w, "failed to serialize schedule metadata", http.StatusInternalServerError)
		return
	}

	medID := uuid.New().String()

	_, err = a.db.ExecContext(r.Context(), `
		INSERT INTO medications (id, patient_id, prescription_document_id, name, strength, instructions, schedule, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		medID, userID, docID, req.Name, req.Strength, req.Instructions, scheduleJSON, true)

	if err != nil {
		http.Error(w, "failed to create medication plan: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(MedicationResponse{
		ID:             medID,
		Name:           req.Name,
		Strength:       req.Strength,
		Instructions:   req.Instructions,
		FrequencyHours: req.FrequencyHours,
	})
}

// GetMedicationsHandler lists all active medication plans for the authenticated user.
func (a *API) GetMedicationsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rows, err := a.db.QueryContext(r.Context(), `
		SELECT m.id, m.name, m.strength, m.instructions, m.schedule,
		       (SELECT max(taken_at) FROM medication_events WHERE medication_id = m.id AND status = 'taken') as last_taken_at
		FROM medications m
		WHERE m.patient_id = $1 AND m.active = true
		ORDER BY m.name ASC`,
		userID)

	if err != nil {
		http.Error(w, "failed to query medications: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	medications := make([]MedicationResponse, 0)
	for rows.Next() {
		var med MedicationResponse
		var scheduleBytes []byte
		var lastTaken sql.NullTime

		err := rows.Scan(&med.ID, &med.Name, &med.Strength, &med.Instructions, &scheduleBytes, &lastTaken)
		if err != nil {
			http.Error(w, "failed to scan medication row: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Parse schedule JSONB
		var sched ScheduleDetails
		if err := json.Unmarshal(scheduleBytes, &sched); err != nil {
			// default fallback
			sched.FrequencyHours = 24
		}
		med.FrequencyHours = sched.FrequencyHours

		if lastTaken.Valid {
			med.LastTakenAt = &lastTaken.Time
			nextTime := lastTaken.Time.Add(time.Duration(sched.FrequencyHours) * time.Hour)
			med.NextScheduledFor = &nextTime
		}

		medications = append(medications, med)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(medications)
}

// PostMedicationTakenHandler records that a dose has been taken.
func (a *API) PostMedicationTakenHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	medicationID := chi.URLParam(r, "medicationId")
	if medicationID == "" {
		http.Error(w, "missing medicationId parameter", http.StatusBadRequest)
		return
	}

	var patientID string
	var scheduleBytes []byte

	err := a.db.QueryRowContext(r.Context(),
		"SELECT patient_id, schedule FROM medications WHERE id = $1 AND active = true",
		medicationID).Scan(&patientID, &scheduleBytes)

	if err == sql.ErrNoRows {
		http.Error(w, "active medication plan not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 403 Forbidden check: Validate medication plan ownership
	if patientID != userID {
		http.Error(w, "forbidden: access to this medication plan is denied", http.StatusForbidden)
		return
	}

	var sched ScheduleDetails
	if err := json.Unmarshal(scheduleBytes, &sched); err != nil {
		sched.FrequencyHours = 24 // fallback
	}

	now := time.Now()
	eventID := uuid.New().String()

	// Insert "taken" event
	_, err = a.db.ExecContext(r.Context(), `
		INSERT INTO medication_events (id, medication_id, scheduled_for, status, taken_at, confirmed_by)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		eventID, medicationID, now, "taken", now, userID)

	if err != nil {
		// Check for duplicate key error just in case of multiple clicks/rapid triggers
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			http.Error(w, "dose already recorded for this time", http.StatusConflict)
			return
		}
		http.Error(w, "failed to record medication dose event: "+err.Error(), http.StatusInternalServerError)
		return
	}

	nextDose := now.Add(time.Duration(sched.FrequencyHours) * time.Hour)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(TakenResponse{NextScheduledFor: nextDose})
}
