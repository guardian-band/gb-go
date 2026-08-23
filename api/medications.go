package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// MedicationCatalogItem represents an item in the medication catalog.
type MedicationCatalogItem struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Strength    string                 `json:"strength"`
	Ingredients []MedicationIngredient `json:"ingredients"`
}

// MedicationIngredient identifies an active ingredient understood by the AI model.
type MedicationIngredient struct {
	Name        string `json:"name"`
	DrugBankID  string `json:"drugBankId"`
	AISupported bool   `json:"aiSupported"`
}

// CreateMedicationRequest represents the payload for creating a new medication plan.
type CreateMedicationRequest struct {
	CatalogItemID          *string `json:"catalogItemId"`
	Name                   string  `json:"name"`
	Strength               string  `json:"strength"`
	Instructions           string  `json:"instructions"`
	FrequencyHours         int     `json:"frequencyHours"`
	PrescriptionDocumentID *string `json:"prescriptionDocumentId"`
}

// MedicationResponse represents the response details of a patient's medication plan.
type MedicationResponse struct {
	ID               string     `json:"id"`
	CatalogItemID    *string    `json:"catalogItemId"`
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

	const catalogQuery = `
		SELECT c.id, c.name, c.strength,
		       i.ingredient_name, i.drugbank_id, i.ai_supported
		FROM medication_catalog c
		LEFT JOIN medication_catalog_ingredients i ON i.catalog_item_id = c.id`
	if searchQuery != "" {
		rows, err = a.db.QueryContext(r.Context(),
			catalogQuery+" WHERE c.name ILIKE $1 ORDER BY c.name ASC, i.ingredient_name ASC",
			"%"+searchQuery+"%")
	} else {
		rows, err = a.db.QueryContext(r.Context(),
			catalogQuery+" ORDER BY c.name ASC, i.ingredient_name ASC")
	}

	if err != nil {
		http.Error(w, "failed to query medication catalog: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	catalog := make([]MedicationCatalogItem, 0)
	itemsByID := make(map[string]int)
	for rows.Next() {
		var item MedicationCatalogItem
		var ingredientName, drugBankID sql.NullString
		var aiSupported sql.NullBool
		if err := rows.Scan(&item.ID, &item.Name, &item.Strength, &ingredientName, &drugBankID, &aiSupported); err != nil {
			http.Error(w, "failed to scan catalog item: "+err.Error(), http.StatusInternalServerError)
			return
		}
		index, exists := itemsByID[item.ID]
		if !exists {
			item.Ingredients = make([]MedicationIngredient, 0)
			catalog = append(catalog, item)
			index = len(catalog) - 1
			itemsByID[item.ID] = index
		}
		if ingredientName.Valid && drugBankID.Valid {
			catalog[index].Ingredients = append(catalog[index].Ingredients, MedicationIngredient{
				Name: ingredientName.String, DrugBankID: drugBankID.String, AISupported: aiSupported.Bool,
			})
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read medication catalog: "+err.Error(), http.StatusInternalServerError)
		return
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
	if req.Name == "" && (req.CatalogItemID == nil || *req.CatalogItemID == "") {
		http.Error(w, "medication name is required", http.StatusBadRequest)
		return
	}

	if req.FrequencyHours != 6 && req.FrequencyHours != 8 && req.FrequencyHours != 12 && req.FrequencyHours != 24 {
		http.Error(w, "invalid frequencyHours, must be 6, 8, 12, or 24 hours", http.StatusBadRequest)
		return
	}

	// Validate patient profile exists
	var exists int
	err := a.db.QueryRowContext(r.Context(), "SELECT 1 FROM patient_profiles WHERE user_id = $1", userID).Scan(&exists)
	if err == sql.ErrNoRows {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": "patient profile not found"})
		return
	} else if err != nil {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var catalogID sql.NullString
	if req.CatalogItemID != nil && *req.CatalogItemID != "" {
		if _, err := uuid.Parse(*req.CatalogItemID); err != nil {
			http.Error(w, "invalid catalogItemId", http.StatusBadRequest)
			return
		}
		err := a.db.QueryRowContext(r.Context(),
			"SELECT name, strength FROM medication_catalog WHERE id = $1",
			*req.CatalogItemID).Scan(&req.Name, &req.Strength)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "medication catalog item not found", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		catalogID = sql.NullString{String: *req.CatalogItemID, Valid: true}
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
		INSERT INTO medications (id, patient_id, prescription_document_id, catalog_item_id, name, strength, instructions, schedule, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		medID, userID, docID, catalogID, req.Name, req.Strength, req.Instructions, scheduleJSON, true)

	if err != nil {
		http.Error(w, "failed to create medication plan: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(MedicationResponse{
		ID:             medID,
		CatalogItemID:  req.CatalogItemID,
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
		SELECT m.id, m.catalog_item_id, m.name, m.strength, m.instructions, m.schedule,
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
		var catalogItemID sql.NullString
		var scheduleBytes []byte
		var lastTaken sql.NullTime

		err := rows.Scan(&med.ID, &catalogItemID, &med.Name, &med.Strength, &med.Instructions, &scheduleBytes, &lastTaken)
		if err != nil {
			http.Error(w, "failed to scan medication row: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if catalogItemID.Valid {
			med.CatalogItemID = &catalogItemID.String
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

// DeleteMedicationHandler soft-deletes an active medication plan for the authenticated user.
func (a *API) DeleteMedicationHandler(w http.ResponseWriter, r *http.Request) {
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
	err := a.db.QueryRowContext(r.Context(),
		"SELECT patient_id FROM medications WHERE id = $1 AND active = true",
		medicationID).Scan(&patientID)

	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "medication plan not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if patientID != userID {
		http.Error(w, "forbidden: medication plan belongs to another patient", http.StatusForbidden)
		return
	}

	_, err = a.db.ExecContext(r.Context(),
		"UPDATE medications SET active = false WHERE id = $1 AND patient_id = $2",
		medicationID, userID)
	if err != nil {
		http.Error(w, "failed to soft-delete medication plan: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
