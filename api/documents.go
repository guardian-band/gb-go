package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
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

// PostDocumentHandler creates a document metadata entry and uploads content to S3.
func (a *API) PostDocumentHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if a.storage == nil {
		http.Error(w, "storage service unavailable", http.StatusInternalServerError)
		return
	}

	// 1. Limit upload size to 10MB
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)

	// Parse multipart form
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "request payload exceeds maximum limit of 10MB", http.StatusBadRequest)
		return
	}

	kind := r.FormValue("kind")
	title := strings.TrimSpace(r.FormValue("title"))

	if kind != "report" && kind != "prescription" {
		http.Error(w, "invalid document kind. Must be 'report' or 'prescription'", http.StatusBadRequest)
		return
	}
	if title == "" {
		http.Error(w, "missing document title", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file parameter 'file'", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Detect content type safely using the first 512 bytes
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		http.Error(w, "failed to read file content", http.StatusInternalServerError)
		return
	}
	contentType := http.DetectContentType(buffer[:n])

	if contentType != "application/pdf" && contentType != "image/jpeg" && contentType != "image/png" {
		http.Error(w, "unsupported content type. Must be PDF, JPEG, or PNG", http.StatusBadRequest)
		return
	}

	// Reset read pointer
	if _, err := file.Seek(0, 0); err != nil {
		http.Error(w, "failed to process file", http.StatusInternalServerError)
		return
	}

	// Generate opaque server key (eg. reports/uuid or prescriptions/uuid)
	newUUID := uuid.New().String()
	objectKey := fmt.Sprintf("%ss/%s", kind, newUUID)

	// Upload to S3
	res, err := a.storage.UploadFileWithKey(r.Context(), file, objectKey, header.Size, contentType)
	if err != nil {
		http.Error(w, "failed to upload document: "+err.Error(), http.StatusInternalServerError)
		return
	}

	versionID := res.VersionID
	if versionID == "" {
		versionID = "v1" // Fallback when S3 bucket versioning is disabled
	}

	// SQL Insert
	var rollbackNeeded = true
	defer func() {
		if rollbackNeeded {
			_ = a.storage.DeleteFile(context.Background(), objectKey)
		}
	}()

	newDocID := uuid.New().String()
	_, err = a.db.ExecContext(r.Context(), `
		INSERT INTO documents (id, patient_id, kind, title, object_key, current_version_id, etag, content_type, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
	`, newDocID, userID, kind, title, res.ObjectKey, versionID, res.ETag, res.ContentType, userID)

	if err != nil {
		http.Error(w, "failed to register document in catalog: "+err.Error(), http.StatusInternalServerError)
		return
	}

	rollbackNeeded = false

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"id":        newDocID,
		"objectKey": res.ObjectKey,
		"versionId": res.VersionID,
		"message":   "document created successfully",
	})
}

// PutDocumentContentHandler updates the content of an existing document.
func (a *API) PutDocumentContentHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if a.storage == nil {
		http.Error(w, "storage service unavailable", http.StatusInternalServerError)
		return
	}

	documentID := chi.URLParam(r, "documentId")
	if documentID == "" {
		http.Error(w, "missing documentId", http.StatusBadRequest)
		return
	}

	// Verify ownership and get original key
	var objectKey string
	var patientID string
	err := a.db.QueryRowContext(r.Context(), "SELECT patient_id, object_key FROM documents WHERE id = $1", documentID).Scan(&patientID, &objectKey)
	if err == sql.ErrNoRows {
		http.Error(w, "document not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "failed to query document info", http.StatusInternalServerError)
		return
	}

	if patientID != userID {
		http.Error(w, "forbidden: access to this document is denied", http.StatusForbidden)
		return
	}

	// 1. Limit upload size to 10MB
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)

	// Parse multipart form
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "request payload exceeds maximum limit of 10MB", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file parameter 'file'", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Detect content type safely using the first 512 bytes
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		http.Error(w, "failed to read file content", http.StatusInternalServerError)
		return
	}
	contentType := http.DetectContentType(buffer[:n])

	if contentType != "application/pdf" && contentType != "image/jpeg" && contentType != "image/png" {
		http.Error(w, "unsupported content type. Must be PDF, JPEG, or PNG", http.StatusBadRequest)
		return
	}

	// Reset read pointer
	if _, err := file.Seek(0, 0); err != nil {
		http.Error(w, "failed to process file", http.StatusInternalServerError)
		return
	}

	// Upload to S3 with the same objectKey (versioning creates new version)
	res, err := a.storage.UploadFileWithKey(r.Context(), file, objectKey, header.Size, contentType)
	if err != nil {
		http.Error(w, "failed to update document content in storage: "+err.Error(), http.StatusInternalServerError)
		return
	}

	versionID := res.VersionID
	if versionID == "" {
		versionID = "v1" // Fallback when S3 bucket versioning is disabled
	}

	// Update SQL catalog
	_, err = a.db.ExecContext(r.Context(), `
		UPDATE documents
		SET current_version_id = $1, etag = $2, content_type = $3, updated_at = NOW()
		WHERE id = $4 AND patient_id = $5
	`, versionID, res.ETag, res.ContentType, documentID, userID)

	if err != nil {
		http.Error(w, "failed to update document catalog info: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// GetDocumentContentHandler streams the content of the authorized document from S3.
func (a *API) GetDocumentContentHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if a.storage == nil {
		http.Error(w, "storage service unavailable", http.StatusInternalServerError)
		return
	}

	documentID := chi.URLParam(r, "documentId")
	if documentID == "" {
		http.Error(w, "missing documentId", http.StatusBadRequest)
		return
	}

	// Verify ownership and get object key
	var objectKey string
	var patientID string
	var contentType string
	err := a.db.QueryRowContext(r.Context(), "SELECT patient_id, object_key, content_type FROM documents WHERE id = $1", documentID).Scan(&patientID, &objectKey, &contentType)
	if err == sql.ErrNoRows {
		http.Error(w, "document not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "failed to query document info", http.StatusInternalServerError)
		return
	}

	if patientID != userID {
		http.Error(w, "forbidden: access to this document is denied", http.StatusForbidden)
		return
	}

	// Retrieve object reader from MinIO
	obj, err := a.storage.client.GetObject(r.Context(), a.storage.bucketName, objectKey, minio.GetObjectOptions{})
	if err != nil {
		http.Error(w, "failed to download document: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer obj.Close()

	// Get object info for content length
	stat, err := obj.Stat()
	if err != nil {
		http.Error(w, "failed to get document metadata: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, obj); err != nil {
		log.Printf("Warning: failed to stream document: %v", err)
	}
}
