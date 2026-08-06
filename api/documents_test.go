package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
)

func TestGetDocumentsSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	req := httptest.NewRequest(http.MethodGet, "/api/documents", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	now := time.Now()
	mock.ExpectQuery("^SELECT id, kind, title, content_type, current_version_id, created_at, updated_at FROM documents WHERE patient_id = \\$1").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "title", "content_type", "current_version_id", "created_at", "updated_at"}).
			AddRow("doc-1", "report", "Blood Test", "application/pdf", "v1", now, now).
			AddRow("doc-2", "prescription", "Med List", "image/png", "v1", now, now))

	a.GetDocumentsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp []DocumentCatalog
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp) != 2 {
		t.Errorf("expected 2 documents, got %d", len(resp))
	}

	if resp[0].Title != "Blood Test" || resp[1].Kind != "prescription" {
		t.Errorf("unexpected documents returned: %+v", resp)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestGetDocumentsEmpty(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	req := httptest.NewRequest(http.MethodGet, "/api/documents", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT id, kind, title, content_type, current_version_id, created_at, updated_at FROM documents WHERE patient_id = \\$1").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "title", "content_type", "current_version_id", "created_at", "updated_at"}))

	a.GetDocumentsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp []DocumentCatalog
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp) != 0 {
		t.Errorf("expected empty documents array, got %d items", len(resp))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestGetDocumentDetailSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	docID := "doc-uuid-abc"

	req := httptest.NewRequest(http.MethodGet, "/api/documents/"+docID, nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	// Setup Chi context URL parameters
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("documentId", docID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	now := time.Now()
	mock.ExpectQuery("^SELECT id, patient_id, kind, title, content_type, current_version_id, created_at, updated_at FROM documents WHERE id = \\$1").
		WithArgs(docID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "patient_id", "kind", "title", "content_type", "current_version_id", "created_at", "updated_at"}).
			AddRow(docID, userID, "report", "Blood Test", "application/pdf", "v1", now, now))

	a.GetDocumentDetailHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp DocumentCatalog
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID != docID || resp.Title != "Blood Test" {
		t.Errorf("unexpected document detail returned: %+v", resp)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestGetDocumentDetailNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	docID := "doc-uuid-abc"

	req := httptest.NewRequest(http.MethodGet, "/api/documents/"+docID, nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("documentId", docID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT id, patient_id, kind, title, content_type, current_version_id, created_at, updated_at FROM documents WHERE id = \\$1").
		WithArgs(docID).
		WillReturnError(sql.ErrNoRows)

	a.GetDocumentDetailHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestGetDocumentDetailForbidden(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	otherUserID := "user-uuid-999"
	docID := "doc-uuid-abc"

	req := httptest.NewRequest(http.MethodGet, "/api/documents/"+docID, nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID)) // authenticated user is doc owner mismatch

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("documentId", docID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	now := time.Now()
	// Return otherUserID as patient_id
	mock.ExpectQuery("^SELECT id, patient_id, kind, title, content_type, current_version_id, created_at, updated_at FROM documents WHERE id = \\$1").
		WithArgs(docID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "patient_id", "kind", "title", "content_type", "current_version_id", "created_at", "updated_at"}).
			AddRow(docID, otherUserID, "report", "Blood Test", "application/pdf", "v1", now, now))

	a.GetDocumentDetailHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for other user's document, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestGetDocumentsUnauthorized(t *testing.T) {
	a := &API{}

	req := httptest.NewRequest(http.MethodGet, "/api/documents", nil)
	rec := httptest.NewRecorder()

	a.GetDocumentsHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestGetDocumentDetailUnauthorized(t *testing.T) {
	a := &API{}
	docID := "doc-uuid-abc"

	req := httptest.NewRequest(http.MethodGet, "/api/documents/"+docID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("documentId", docID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	a.GetDocumentDetailHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}
