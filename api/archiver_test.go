package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
)

type stripAuthTransport struct {
	underlying http.RoundTripper
}

func (s *stripAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Del("Authorization")
	return s.underlying.RoundTrip(req)
}

// In-Memory S3 Mock storage helper
type mockS3Server struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func newMockS3Server() *mockS3Server {
	return &mockS3Server{
		objects: make(map[string][]byte),
	}
}

func (m *mockS3Server) put(key string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
}

func (m *mockS3Server) get(key string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, exists := m.objects[key]
	return data, exists
}

func setupMockStorage(t *testing.T, mockS3 *mockS3Server) (*Storage, *httptest.Server) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read and consume request body to prevent TCP connection reset (EOF)
		var reqBody bytes.Buffer
		if r.Body != nil {
			_, _ = io.Copy(&reqBody, r.Body)
			_ = r.Body.Close()
		}

		bucketPrefix := "/guardianband-files/"
		objectKey := strings.TrimPrefix(r.URL.Path, bucketPrefix)

		// 1. Mock Bucket Existence check & Object list
		if r.URL.Path == "/guardianband-files/" || r.URL.Path == "/guardianband-files" {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method == http.MethodGet {
				prefix := r.URL.Query().Get("prefix")
				mockS3.mu.RLock()
				var contentsXml strings.Builder
				for k, v := range mockS3.objects {
					if strings.HasPrefix(k, prefix) {
						contentsXml.WriteString(fmt.Sprintf(`
    <Contents>
        <Key>%s</Key>
        <LastModified>2026-08-04T09:00:00.000Z</LastModified>
        <ETag>&quot;mock-etag&quot;</ETag>
        <Size>%d</Size>
    </Contents>`, k, len(v)))
					}
				}
				mockS3.mu.RUnlock()

				xmlResponse := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
    <Name>guardianband-files</Name>
    <Prefix>%s</Prefix>
    <IsTruncated>false</IsTruncated>%s
</ListBucketResult>`, prefix, contentsXml.String())

				w.Header().Set("Content-Type", "application/xml")
				w.Header().Set("Content-Length", strconv.Itoa(len(xmlResponse)))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(xmlResponse))
				return
			}
		}

		// 2. Mock Object PUT (Upload)
		if r.Method == http.MethodPut {
			mockS3.put(objectKey, reqBody.Bytes())
			w.Header().Set("x-amz-version-id", "mock-version-v1")
			w.Header().Set("Last-Modified", "Tue, 04 Aug 2026 09:00:00 GMT")
			w.WriteHeader(http.StatusOK)
			return
		}

		// 3. Mock Object HEAD (Metadata)
		if r.Method == http.MethodHead {
			data, exists := mockS3.get(objectKey)
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("x-amz-version-id", "mock-version-v1")
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("Last-Modified", "Tue, 04 Aug 2026 09:00:00 GMT")
			w.WriteHeader(http.StatusOK)
			return
		}

		// 4. Mock Object GET (Download)
		if r.Method == http.MethodGet {
			data, exists := mockS3.get(objectKey)
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("Last-Modified", "Tue, 04 Aug 2026 09:00:00 GMT")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
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

func TestArchiverRunOnceSuccess(t *testing.T) {
	db, dbMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer redisClient.Close()

	// Pre-seed Redis Stream with a message (at local hour 11:30 in Istanbul (UTC+3), recorded at 08:30 UTC)
	userID := "user-uuid-123"
	recordedAt := time.Date(2026, 8, 4, 8, 30, 0, 0, time.UTC)

	_, err = redisClient.XAdd(context.Background(), &redis.XAddArgs{
		Stream: "guardianband:telemetry",
		Values: map[string]interface{}{
			"patient_id":   userID,
			"heart_rate":   "72",
			"blood_oxygen": "98",
			"recorded_at":  recordedAt.Format(time.RFC3339),
			"device_sn":    "band-sn-123",
			"reading_id":   "read-111",
		},
	}).Result()
	if err != nil {
		t.Fatalf("failed to seed redis stream: %v", err)
	}

	dbMock.ExpectQuery("^SELECT timezone FROM patient_profiles").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"timezone"}).AddRow("Europe/Istanbul"))

	mockS3 := newMockS3Server()
	// Pre-seed S3 with another hourly chunk file
	existingData := []byte(`{"patientId":"user-uuid-123","heartRate":70,"bloodOxygen":95,"recordedAt":"2026-08-04T12:00:00Z"}` + "\n")
	mockS3.put("telemetry/hourly/v1/user-uuid-123/2026-08-04/12.jsonl", existingData)

	storage, server := setupMockStorage(t, mockS3)
	defer server.Close()

	archiver := NewTelemetryArchiver(db, redisClient, storage)
	err = archiver.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("archiver run once failed: %v", err)
	}

	// Verify that the new chunk file was created (local time: 8:30 UTC + 3 hours = 11:30 local -> 11.jsonl)
	chunkKey := "telemetry/hourly/v1/user-uuid-123/2026-08-04/11.jsonl"
	data, exists := mockS3.get(chunkKey)
	if !exists {
		t.Fatalf("expected hourly chunk file %s to be created in S3", chunkKey)
	}

	if !strings.Contains(string(data), `"readingId":"read-111"`) {
		t.Errorf("expected S3 chunk to contain the new vital record, got: %s", string(data))
	}

	// Verify daily manifest file was updated
	manifestKey := "telemetry/daily/v1/user-uuid-123/2026-08-04/manifest.json"
	manifestData, exists := mockS3.get(manifestKey)
	if !exists {
		t.Fatalf("expected daily manifest file %s to be created", manifestKey)
	}

	var manifest DailyManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("failed to parse generated manifest JSON: %v", err)
	}

	// Check hours catalog size (should list 11.jsonl and 12.jsonl)
	if len(manifest.Hours) != 2 {
		t.Errorf("expected 2 hour chunks in daily manifest, got %d", len(manifest.Hours))
	}

	// Verify Redis stream message has been acknowledged
	groups, err := redisClient.XInfoGroups(context.Background(), "guardianband:telemetry").Result()
	if err != nil {
		t.Fatalf("failed to query stream info: %v", err)
	}
	if len(groups) > 0 && groups[0].Pending != 0 {
		t.Errorf("expected 0 pending messages, got %d", groups[0].Pending)
	}

	if err := dbMock.ExpectationsWereMet(); err != nil {
		t.Errorf("db expectations were not met: %v", err)
	}
}

func TestArchiverRunOnceNoDuplicates(t *testing.T) {
	db, dbMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer redisClient.Close()

	// Seed Redis Stream with duplicate reading ID
	userID := "user-uuid-123"
	recordedAt := time.Date(2026, 8, 4, 8, 30, 0, 0, time.UTC)

	_, err = redisClient.XAdd(context.Background(), &redis.XAddArgs{
		Stream: "guardianband:telemetry",
		Values: map[string]interface{}{
			"patient_id":   userID,
			"heart_rate":   "80",
			"blood_oxygen": "99",
			"recorded_at":  recordedAt.Format(time.RFC3339),
			"reading_id":   "read-duplicate-999",
		},
	}).Result()

	dbMock.ExpectQuery("^SELECT timezone FROM patient_profiles").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"timezone"}).AddRow("UTC"))

	mockS3 := newMockS3Server()
	// Pre-seed hourly chunk file with the duplicate record already in S3
	existingData := []byte(`{"patientId":"user-uuid-123","heartRate":80,"bloodOxygen":99,"recordedAt":"2026-08-04T08:30:00Z","readingId":"read-duplicate-999","streamId":"123-0"}` + "\n")
	chunkKey := "telemetry/hourly/v1/user-uuid-123/2026-08-04/08.jsonl"
	mockS3.put(chunkKey, existingData)

	storage, server := setupMockStorage(t, mockS3)
	defer server.Close()

	archiver := NewTelemetryArchiver(db, redisClient, storage)
	err = archiver.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("archiver run failed: %v", err)
	}

	// Verify duplicate was filtered out
	updatedData, _ := mockS3.get(chunkKey)
	lines := strings.Split(strings.TrimSpace(string(updatedData)), "\n")
	if len(lines) != 1 {
		t.Errorf("expected S3 chunk to contain exactly 1 reading after duplicate prevention, got %d. Content: %s", len(lines), string(updatedData))
	}

	if err := dbMock.ExpectationsWereMet(); err != nil {
		t.Errorf("db expectations were not met: %v", err)
	}
}

func TestArchiverUpdateDailyManifest(t *testing.T) {
	mockS3 := newMockS3Server()
	// Pre-seed 08.jsonl with 2 records
	existingData := []byte(`{"patientId":"user-uuid-123","heartRate":72,"bloodOxygen":98,"recordedAt":"2026-08-04T08:00:00Z"}` + "\n" +
		`{"patientId":"user-uuid-123","heartRate":73,"bloodOxygen":99,"recordedAt":"2026-08-04T08:30:00Z"}` + "\n")
	mockS3.put("telemetry/hourly/v1/user-uuid-123/2026-08-04/08.jsonl", existingData)

	storage, server := setupMockStorage(t, mockS3)
	defer server.Close()

	archiver := NewTelemetryArchiver(nil, nil, storage)
	loc, _ := time.LoadLocation("Europe/Istanbul")
	err := archiver.UpdateDailyManifest(context.Background(), "user-uuid-123", "2026-08-04", "Europe/Istanbul", loc)
	if err != nil {
		t.Fatalf("failed to update daily manifest: %v", err)
	}

	// Verify manifest metadata
	manifestKey := "telemetry/daily/v1/user-uuid-123/2026-08-04/manifest.json"
	manifestData, exists := mockS3.get(manifestKey)
	if !exists {
		t.Fatalf("manifest should exist at %s", manifestKey)
	}

	var manifest DailyManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("failed to parse manifest: %v", err)
	}

	if manifest.LocalDate != "2026-08-04" || len(manifest.Hours) != 1 {
		t.Errorf("unexpected manifest content: %+v", manifest)
	}
	if manifest.Hours[0].ReadingCount != 2 {
		t.Errorf("expected readingCount of 2, got %d", manifest.Hours[0].ReadingCount)
	}
}
