package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Storage handles S3/MinIO operations.
type Storage struct {
	client     *minio.Client
	bucketName string
}

// NewStorage initializes a new S3/MinIO client.
func NewStorage() (*Storage, error) {
	endpoint := getEnv("S3_ENDPOINT", "localhost:9000")
	accessKey := getEnv("S3_ACCESS_KEY", "minioadmin")
	secretKey := getEnv("S3_SECRET_KEY", "minioadmin")
	bucketName := getEnv("S3_BUCKET", "guardianband-files")
	useSSL := getEnv("S3_USE_SSL", "false") == "true"

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize S3 client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Ensure the bucket exists
	exists, err := client.BucketExists(ctx, bucketName)
	if err != nil {
		log.Printf("Warning: failed to check bucket existence: %v", err)
	} else if !exists {
		err = client.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{})
		if err != nil {
			log.Printf("Warning: failed to create bucket: %v", err)
		} else {
			log.Printf("Successfully created bucket: %s", bucketName)
		}
	}

	return &Storage{
		client:     client,
		bucketName: bucketName,
	}, nil
}

type UploadResult struct {
	ObjectKey   string
	VersionID   string
	ETag        string
	ContentType string
	Size        int64
}

// UploadFile uploads an object to MinIO/S3 and returns its upload info.
func (s *Storage) UploadFile(ctx context.Context, fileHeaderReader io.Reader, fileName string, fileSize int64, contentType string) (UploadResult, error) {
	// Generate unique filename to prevent overwrites
	ext := filepath.Ext(fileName)
	uniqueFileName := fmt.Sprintf("%s%s", uuid.New().String(), ext)

	info, err := s.client.PutObject(ctx, s.bucketName, uniqueFileName, fileHeaderReader, fileSize, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return UploadResult{}, fmt.Errorf("failed to upload object: %w", err)
	}

	return UploadResult{
		ObjectKey:   uniqueFileName,
		VersionID:   info.VersionID,
		ETag:        info.ETag,
		ContentType: contentType,
		Size:        info.Size,
	}, nil
}

// UploadFileWithKey uploads an object to MinIO/S3 using a predefined key.
func (s *Storage) UploadFileWithKey(ctx context.Context, fileHeaderReader io.Reader, objectKey string, fileSize int64, contentType string) (UploadResult, error) {
	info, err := s.client.PutObject(ctx, s.bucketName, objectKey, fileHeaderReader, fileSize, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return UploadResult{}, fmt.Errorf("failed to upload object: %w", err)
	}

	return UploadResult{
		ObjectKey:   objectKey,
		VersionID:   info.VersionID,
		ETag:        info.ETag,
		ContentType: contentType,
		Size:        info.Size,
	}, nil
}

// DeleteFile deletes an object from MinIO/S3 (useful for cleanup).
func (s *Storage) DeleteFile(ctx context.Context, objectKey string) error {
	err := s.client.RemoveObject(ctx, s.bucketName, objectKey, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	return nil
}

// UploadHandler handles file upload requests (POST /api/upload).
func (a *API) UploadHandler(w http.ResponseWriter, r *http.Request) {
	if a.storage == nil {
		http.Error(w, "storage service unavailable", http.StatusInternalServerError)
		return
	}

	// Limit upload size to 10MB
	r.ParseMultipartForm(10 << 20)

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "invalid file parameter 'file'", http.StatusBadRequest)
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	res, err := a.storage.UploadFile(r.Context(), file, header.Filename, header.Size, contentType)
	if err != nil {
		http.Error(w, "failed to store file", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"fileKey": res.ObjectKey,
		"message": "file uploaded successfully",
	})
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}