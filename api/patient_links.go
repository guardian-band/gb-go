package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type PatientLink struct {
	ID                 string  `json:"id"`
	PatientID          string  `json:"patientId"`
	LinkedUserID       *string `json:"linkedUserId,omitempty"`
	DisplayName        *string `json:"displayName,omitempty"`
	Phone              *string `json:"phone,omitempty"`
	Relationship       string  `json:"relationship"`
	Kind               string  `json:"kind"`
	CanMonitor         bool    `json:"canMonitor"`
	IsEmergencyContact bool    `json:"isEmergencyContact"`
	IsPrimary          bool    `json:"isPrimary"`
}

type CreatePatientLinkRequest struct {
	Phone              string  `json:"phone"` // Used to find registered user or as external contact phone
	DisplayName        *string `json:"displayName,omitempty"`
	Relationship       string  `json:"relationship"`
	Kind               string  `json:"kind"` // 'family', 'clinician', 'other'
	CanMonitor         bool    `json:"canMonitor"`
	IsEmergencyContact bool    `json:"isEmergencyContact"`
	IsPrimary          bool    `json:"isPrimary"`
}

type UpdatePatientLinkRequest struct {
	DisplayName        *string `json:"displayName,omitempty"`
	Relationship       *string `json:"relationship,omitempty"`
	CanMonitor         *bool   `json:"canMonitor,omitempty"`
	IsEmergencyContact *bool   `json:"isEmergencyContact,omitempty"`
	IsPrimary          *bool   `json:"isPrimary,omitempty"`
}

// GetPatientLinksHandler returns all links for the authenticated patient.
func (a *API) GetPatientLinksHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || patientID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	rows, err := a.db.QueryContext(r.Context(), `
		SELECT id, patient_id, linked_user_id, display_name, phone, relationship, kind, can_monitor, is_emergency_contact, is_primary
		FROM patient_links
		WHERE patient_id = $1
	`, patientID)
	if err != nil {
		http.Error(w, "Failed to query patient links", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var links []PatientLink
	for rows.Next() {
		var l PatientLink
		if err := rows.Scan(&l.ID, &l.PatientID, &l.LinkedUserID, &l.DisplayName, &l.Phone, &l.Relationship, &l.Kind, &l.CanMonitor, &l.IsEmergencyContact, &l.IsPrimary); err != nil {
			http.Error(w, "Failed to scan patient links", http.StatusInternalServerError)
			return
		}
		links = append(links, l)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(links)
}

// PostPatientLinkHandler creates a new patient link.
func (a *API) PostPatientLinkHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || patientID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req CreatePatientLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	req.Phone = strings.TrimSpace(req.Phone)
	if req.Phone == "" {
		http.Error(w, "Phone number is required", http.StatusBadRequest)
		return
	}

	if req.Kind != "family" && req.Kind != "clinician" && req.Kind != "other" {
		http.Error(w, "Invalid kind. Must be family, clinician, or other", http.StatusBadRequest)
		return
	}

	// Check if the phone belongs to a registered user
	var linkedUserID string
	err := a.db.QueryRowContext(r.Context(), "SELECT id FROM users WHERE phone_number = $1", req.Phone).Scan(&linkedUserID)
	
	isRegistered := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Failed to check user existence", http.StatusInternalServerError)
		return
	}

	if linkedUserID == patientID {
		http.Error(w, "Cannot link to yourself", http.StatusBadRequest)
		return
	}

	if !isRegistered && req.CanMonitor {
		http.Error(w, "External contacts cannot have monitoring permissions", http.StatusBadRequest)
		return
	}

	if !isRegistered && req.DisplayName == nil {
		http.Error(w, "Display name is required for external contacts", http.StatusBadRequest)
		return
	}

	// Duplicate link check
	var exists bool
	if isRegistered {
		err = a.db.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM patient_links WHERE patient_id = $1 AND linked_user_id = $2)", patientID, linkedUserID).Scan(&exists)
	} else {
		err = a.db.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM patient_links WHERE patient_id = $1 AND phone = $2)", patientID, req.Phone).Scan(&exists)
	}
	
	if err == nil && exists {
		http.Error(w, "Link already exists for this contact", http.StatusConflict)
		return
	}

	newID := uuid.New().String()
	
	var insertErr error
	if isRegistered {
		// Registered user link
		_, insertErr = a.db.ExecContext(r.Context(), `
			INSERT INTO patient_links (id, patient_id, linked_user_id, relationship, kind, can_monitor, is_emergency_contact, is_primary)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, newID, patientID, linkedUserID, req.Relationship, req.Kind, req.CanMonitor, req.IsEmergencyContact, req.IsPrimary)
	} else {
		// External contact link
		_, insertErr = a.db.ExecContext(r.Context(), `
			INSERT INTO patient_links (id, patient_id, display_name, phone, relationship, kind, can_monitor, is_emergency_contact, is_primary)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`, newID, patientID, req.DisplayName, req.Phone, req.Relationship, req.Kind, req.CanMonitor, req.IsEmergencyContact, req.IsPrimary)
	}

	if insertErr != nil {
		http.Error(w, "Failed to create link", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// PatchPatientLinkHandler updates an existing patient link.
func (a *API) PatchPatientLinkHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || patientID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	linkID := chi.URLParam(r, "linkId")
	if linkID == "" {
		http.Error(w, "Missing linkId", http.StatusBadRequest)
		return
	}

	// Verify ownership
	var existingLinkedUserID *string
	err := a.db.QueryRowContext(r.Context(), "SELECT linked_user_id FROM patient_links WHERE id = $1 AND patient_id = $2", linkID, patientID).Scan(&existingLinkedUserID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Link not found or unauthorized", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "Failed to query link", http.StatusInternalServerError)
		return
	}

	var req UpdatePatientLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	if req.CanMonitor != nil && *req.CanMonitor && existingLinkedUserID == nil {
		http.Error(w, "External contacts cannot have monitoring permissions", http.StatusBadRequest)
		return
	}

	// Basic dynamic update query construction
	query := "UPDATE patient_links SET "
	var args []interface{}
	argId := 1

	if req.DisplayName != nil {
		query += fmt.Sprintf("display_name = $%d, ", argId)
		args = append(args, *req.DisplayName)
		argId++
	}
	if req.Relationship != nil {
		query += fmt.Sprintf("relationship = $%d, ", argId)
		args = append(args, *req.Relationship)
		argId++
	}
	if req.CanMonitor != nil {
		query += fmt.Sprintf("can_monitor = $%d, ", argId)
		args = append(args, *req.CanMonitor)
		argId++
	}
	if req.IsEmergencyContact != nil {
		query += fmt.Sprintf("is_emergency_contact = $%d, ", argId)
		args = append(args, *req.IsEmergencyContact)
		argId++
	}
	if req.IsPrimary != nil {
		query += fmt.Sprintf("is_primary = $%d, ", argId)
		args = append(args, *req.IsPrimary)
		argId++
	}

	if len(args) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Remove trailing comma and space
	query = query[:len(query)-2]
	query += fmt.Sprintf(" WHERE id = $%d AND patient_id = $%d", argId, argId+1)
	args = append(args, linkID, patientID)

	_, err = a.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "Failed to update link", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// DeletePatientLinkHandler deletes a patient link.
func (a *API) DeletePatientLinkHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || patientID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	linkID := chi.URLParam(r, "linkId")
	if linkID == "" {
		http.Error(w, "Missing linkId", http.StatusBadRequest)
		return
	}

	result, err := a.db.ExecContext(r.Context(), "DELETE FROM patient_links WHERE id = $1 AND patient_id = $2", linkID, patientID)
	if err != nil {
		http.Error(w, "Failed to delete link", http.StatusInternalServerError)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		http.Error(w, "Link not found or unauthorized", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
