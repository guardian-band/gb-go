package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// DocumentCatalog represents the catalog metadata of a document.
type DocumentCatalog struct {
	ID               string    `json:"id"`
	Kind             string    `json:"kind"`
	Title            string    `json:"title"`
	ContentType      string    `json:"contentType"`
	CurrentVersionID string    `json:"currentVersionId"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// GetDocumentsHandler lists all report and prescription metadata for the authenticated user.
func (a *API) GetDocumentsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rows, err := a.db.QueryContext(r.Context(), `
		SELECT id, kind, title, content_type, current_version_id, created_at, updated_at
		FROM documents
		WHERE patient_id = $1
		ORDER BY created_at DESC`,
		userID)

	if err != nil {
		http.Error(w, "failed to query documents: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	documents := make([]DocumentCatalog, 0)
	for rows.Next() {
		var doc DocumentCatalog
		err := rows.Scan(&doc.ID, &doc.Kind, &doc.Title, &doc.ContentType, &doc.CurrentVersionID, &doc.CreatedAt, &doc.UpdatedAt)
		if err != nil {
			http.Error(w, "failed to scan document row: "+err.Error(), http.StatusInternalServerError)
			return
		}
		documents = append(documents, doc)
	}

	if err := rows.Err(); err != nil {
		http.Error(w, "error reading document rows: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(documents)
}

// GetDocumentDetailHandler returns single document catalog metadata if the user owns it.
func (a *API) GetDocumentDetailHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	documentID := chi.URLParam(r, "documentId")
	if documentID == "" {
		http.Error(w, "missing documentId parameter", http.StatusBadRequest)
		return
	}

	var doc DocumentCatalog
	var patientID string

	err := a.db.QueryRowContext(r.Context(), `
		SELECT id, patient_id, kind, title, content_type, current_version_id, created_at, updated_at
		FROM documents
		WHERE id = $1`,
		documentID).Scan(&doc.ID, &patientID, &doc.Kind, &doc.Title, &doc.ContentType, &doc.CurrentVersionID, &doc.CreatedAt, &doc.UpdatedAt)

	if err == sql.ErrNoRows {
		http.Error(w, "document not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "failed to query document details: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 403 Forbidden check: Validate document belongs to the authenticated user
	if patientID != userID {
		http.Error(w, "forbidden: access to this document is denied", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(doc)
}
