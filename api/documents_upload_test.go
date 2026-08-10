package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type mockDocS3Server struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func (m *mockDocS3Server) put(key string, val []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = val
}

func (m *mockDocS3Server) get(key string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, exists := m.objects[key]
	return val, exists
}

func (m *mockDocS3Server) delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
}

func setupDocMockStorage(t *testing.T, mockS3 *mockDocS3Server) (*Storage, *httptest.Server) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody bytes.Buffer
		if r.Body != nil {
			_, _ = io.Copy(&reqBody, r.Body)
			_ = r.Body.Close()
		}

		bucketPrefix := "/guardianband-files/"
		objectKey := strings.TrimPrefix(r.URL.Path, bucketPrefix)

		if r.URL.Path == "/guardianband-files/" || r.URL.Path == "/guardianband-files" {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method == http.MethodGet {
				xmlResponse := `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
    <Name>guardianband-files</Name>
    <Prefix></Prefix>
    <IsTruncated>false</IsTruncated>
</ListBucketResult>`
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(xmlResponse))
				return
			}
		}

		if r.Method == http.MethodPut {
			mockS3.put(objectKey, reqBody.Bytes())
			w.Header().Set("x-amz-version-id", "mock-version-v1")
			w.Header().Set("Last-Modified", "Tue, 04 Aug 2026 09:00:00 GMT")
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method == http.MethodGet {
			data, exists := mockS3.get(objectKey)
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.Header().Set("Last-Modified", "Tue, 04 Aug 2026 09:00:00 GMT")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}

		if r.Method == http.MethodHead {
			data, exists := mockS3.get(objectKey)
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("x-amz-version-id", "mock-version-v1")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.Header().Set("Last-Modified", "Tue, 04 Aug 2026 09:00:00 GMT")
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method == http.MethodDelete {
			mockS3.delete(objectKey)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))

	endpoint := strings.TrimPrefix(server.URL, "http://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4("mockkey", "mocksecret", "mocktoken"),
		Secure:       false,
		BucketLookup: minio.BucketLookupPath,
		Transport:    &stripAuthTransport{underlying: http.DefaultTransport},
	})
	if err != nil {
		t.Fatalf("failed to create minio client: %v", err)
	}

	return &Storage{
		client:     client,
		bucketName: "guardianband-files",
	}, server
}

func TestPostDocumentSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mockS3 := &mockDocS3Server{objects: make(map[string][]byte)}
	storage, s3Server := setupDocMockStorage(t, mockS3)
	defer s3Server.Close()

	api := &API{db: db, storage: storage}
	userID := "user-123"

	// Create multipart request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	
	_ = writer.WriteField("kind", "report")
	_ = writer.WriteField("title", "Blood Report")
	
	// Create a dummy PDF file part
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="test.pdf"`)
	h.Set("Content-Type", "application/pdf")
	part, _ := writer.CreatePart(h)
	
	// Dummy PDF signature: %PDF-1.4
	pdfData := []byte("%PDF-1.4\n" + strings.Repeat("A", 100))
	_, _ = part.Write(pdfData)
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/documents", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	// SQL Expectation
	mock.ExpectExec("^INSERT INTO documents").
		WithArgs(sqlmock.AnyArg(), userID, "report", "Blood Report", sqlmock.AnyArg(), "mock-version-v1", sqlmock.AnyArg(), "application/pdf", userID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	api.PostDocumentHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d, body: %s", rec.Code, rec.Body.String())
	}

	if len(mockS3.objects) != 1 {
		t.Errorf("expected 1 file uploaded to S3, got %d", len(mockS3.objects))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestPostDocumentSQLFailureCleanup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mockS3 := &mockDocS3Server{objects: make(map[string][]byte)}
	storage, s3Server := setupDocMockStorage(t, mockS3)
	defer s3Server.Close()

	api := &API{db: db, storage: storage}
	userID := "user-123"

	// Create multipart request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("kind", "report")
	_ = writer.WriteField("title", "Blood Report")
	
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="test.pdf"`)
	h.Set("Content-Type", "application/pdf")
	part, _ := writer.CreatePart(h)
	_, _ = part.Write([]byte("%PDF-1.4\nDummy PDF content"))
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/documents", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	// SQL failure
	mock.ExpectExec("^INSERT INTO documents").
		WillReturnError(fmt.Errorf("DB Error"))

	api.PostDocumentHandler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}

	// S3 Object MUST be cleaned up (deleted)
	if len(mockS3.objects) != 0 {
		t.Errorf("expected 0 files in S3 after SQL rollback, got %d", len(mockS3.objects))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestGetDocumentContentSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mockS3 := &mockDocS3Server{objects: make(map[string][]byte)}
	storage, s3Server := setupDocMockStorage(t, mockS3)
	defer s3Server.Close()

	api := &API{db: db, storage: storage}
	userID := "user-123"
	docID := "doc-uuid-abc"
	objectKey := "reports/doc-uuid-abc"
	pdfContent := []byte("%PDF-1.4\nHello pdf stream")

	mockS3.put(objectKey, pdfContent)

	req := httptest.NewRequest(http.MethodGet, "/api/documents/"+docID+"/content", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("documentId", docID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	// SQL queries
	mock.ExpectQuery("^SELECT patient_id, object_key, content_type FROM documents").
		WithArgs(docID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "object_key", "content_type"}).AddRow(userID, objectKey, "application/pdf"))

	api.GetDocumentContentHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	if !bytes.Equal(rec.Body.Bytes(), pdfContent) {
		t.Errorf("stream content mismatch, got %s", rec.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}
