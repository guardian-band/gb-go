package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/redis/go-redis/v9"
)

// TelemetryReading represents a single vital measurement inside archived chunks.
type TelemetryReading struct {
	PatientID   string    `json:"patientId"`
	HeartRate   int       `json:"heartRate,omitempty"`
	BloodOxygen int       `json:"bloodOxygen,omitempty"`
	RecordedAt  time.Time `json:"recordedAt"`
	DeviceSN    string    `json:"deviceSn,omitempty"`
	ReadingID   string    `json:"readingId,omitempty"`
	StreamID    string    `json:"streamId"`
}

// DailyManifest represents the index metadata for a patient's closed local date.
type DailyManifest struct {
	SchemaVersion int                `json:"schemaVersion"`
	LocalDate     string             `json:"localDate"`
	TimeZone      string             `json:"timeZone"`
	PeriodStart   time.Time          `json:"periodStart"`
	PeriodEnd     time.Time          `json:"periodEnd"`
	Hours         []HourlyChunkEntry `json:"hours"`
}

// HourlyChunkEntry represents metadata pointing to an hourly chunk.
type HourlyChunkEntry struct {
	From         time.Time `json:"from"`
	ObjectKey    string    `json:"objectKey"`
	VersionID    string    `json:"versionId"`
	ReadingCount int       `json:"readingCount"`
}

// TelemetryArchiver runs the processing logic for archiving Redis streams to object storage.
type TelemetryArchiver struct {
	db      *sql.DB
	redis   *redis.Client
	storage *Storage
}

// NewTelemetryArchiver creates an archiver helper.
func NewTelemetryArchiver(db *sql.DB, redisClient *redis.Client, storage *Storage) *TelemetryArchiver {
	return &TelemetryArchiver{
		db:      db,
		redis:   redisClient,
		storage: storage,
	}
}

// RunOnce pulls pending messages from guardianband:telemetry Stream, groups them,
// archives hourly chunks, generates/updates manifests, and trims Stream records.
func (ta *TelemetryArchiver) RunOnce(ctx context.Context) error {
	// 1. Ensure consumer group exists
	_ = ta.redis.XGroupCreateMkStream(ctx, "guardianband:telemetry", "archive", "0").Err()

	// 2. Read pending/new messages using group
	streams, err := ta.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    "archive",
		Consumer: "archiver-worker",
		Streams:  []string{"guardianband:telemetry", ">"},
		Count:    1000,
		Block:    10 * time.Millisecond,
	}).Result()

	if err == redis.Nil {
		return nil
	} else if err != nil {
		return fmt.Errorf("read stream: %w", err)
	}

	if len(streams) == 0 || len(streams[0].Messages) == 0 {
		return nil
	}

	messages := streams[0].Messages
	readingsByPatient := make(map[string][]TelemetryReading)
	msgIDsToAck := make([]string, 0, len(messages))

	for _, msg := range messages {
		reading, parseErr := parseStreamMessage(msg)
		if parseErr != nil {
			// Skip malformed records to prevent locking the stream, but still acknowledge them
			msgIDsToAck = append(msgIDsToAck, msg.ID)
			continue
		}
		readingsByPatient[reading.PatientID] = append(readingsByPatient[reading.PatientID], reading)
		msgIDsToAck = append(msgIDsToAck, msg.ID)
	}

	// 3. Process each patient's telemetry chunks
	for patientID, readings := range readingsByPatient {
		timezone := ta.getPatientTimezone(ctx, patientID)
		loc, tzErr := time.LoadLocation(timezone)
		if tzErr != nil {
			loc = time.UTC
		}

		// Group patient readings by hourly chunk paths
		chunksMap := make(map[string][]TelemetryReading)
		for _, r := range readings {
			localTime := r.RecordedAt.In(loc)
			localDate := localTime.Format("2006-01-02")
			hour := localTime.Format("15")

			key := fmt.Sprintf("telemetry/hourly/v1/%s/%s/%s.jsonl", patientID, localDate, hour)
			chunksMap[key] = append(chunksMap[key], r)
		}

		// Process each hourly chunk
		for objectKey, list := range chunksMap {
			// Get existing readings in storage to prevent duplicates
			existingMap := make(map[string]bool)
			existingReadings, fetchErr := ta.fetchHourlyChunkReadings(ctx, objectKey)
			if fetchErr == nil {
				for _, er := range existingReadings {
					if er.ReadingID != "" {
						existingMap[er.ReadingID] = true
					} else if er.StreamID != "" {
						existingMap[er.StreamID] = true
					}
				}
			}

			// Filter out duplicates
			toWrite := make([]TelemetryReading, 0)
			// Start with existing ones to maintain chronological order
			toWrite = append(toWrite, existingReadings...)

			for _, nr := range list {
				dupKey := nr.ReadingID
				if dupKey == "" {
					dupKey = nr.StreamID
				}
				if !existingMap[dupKey] {
					toWrite = append(toWrite, nr)
				}
			}

			// Chronological sort could be added here, but stream read is already chronological.
			// Write JSON Lines to S3
			var buffer bytes.Buffer
			for _, item := range toWrite {
				line, _ := json.Marshal(item)
				buffer.Write(line)
				buffer.WriteString("\n")
			}

			bucketName := ta.storage.bucketName
			_, putErr := ta.storage.client.PutObject(ctx, bucketName, objectKey, &buffer, int64(buffer.Len()), minio.PutObjectOptions{
				ContentType:          "application/x-jsonlines",
				DisableContentSha256: true,
			})
			if putErr != nil {
				return fmt.Errorf("put hourly chunk to storage: %w", putErr)
			}

			// Trigger daily manifest update for the date of this chunk
			parts := strings.Split(objectKey, "/")
			if len(parts) >= 6 {
				localDate := parts[4] // Extract local-date from path
				_ = ta.UpdateDailyManifest(ctx, patientID, localDate, timezone, loc)
			}
		}
	}

	// 4. Acknowledge processed messages in Stream
	if len(msgIDsToAck) > 0 {
		_, ackErr := ta.redis.XAck(ctx, "guardianband:telemetry", "archive", msgIDsToAck...).Result()
		if ackErr != nil {
			return fmt.Errorf("ack stream messages: %w", ackErr)
		}

		// Trim acknowledged entries to keep memory low
		lastMsgID := msgIDsToAck[len(msgIDsToAck)-1]
		_ = ta.redis.XTrimMinID(ctx, "guardianband:telemetry", lastMsgID).Err()
	}

	return nil
}

// UpdateDailyManifest constructs or rewrites the manifest file pointing to hourly chunks.
func (ta *TelemetryArchiver) UpdateDailyManifest(ctx context.Context, patientID string, localDate string, timezone string, loc *time.Location) error {
	parsedDate, err := time.ParseInLocation("2006-01-02", localDate, loc)
	if err != nil {
		return err
	}

	periodStart := parsedDate.UTC()
	periodEnd := parsedDate.Add(24 * time.Hour).UTC()

	// List all files in the hourly folder for this date
	prefix := fmt.Sprintf("telemetry/hourly/v1/%s/%s/", patientID, localDate)
	bucketName := ta.storage.bucketName

	objectCh := ta.storage.client.ListObjects(ctx, bucketName, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})

	hoursList := make([]HourlyChunkEntry, 0)
	for obj := range objectCh {
		if obj.Err != nil {
			continue
		}

		// Read count of records in this hourly file
		recordCount, versionID := ta.getHourlyMetadata(ctx, obj.Key)

		// Determine hour start
		hourFile := strings.TrimSuffix(strings.Replace(obj.Key, prefix, "", 1), ".jsonl")
		hourVal, parseErr := strconv.Atoi(hourFile)
		if parseErr != nil {
			continue
		}

		chunkTime := parsedDate.Add(time.Duration(hourVal) * time.Hour)

		hoursList = append(hoursList, HourlyChunkEntry{
			From:         chunkTime,
			ObjectKey:    obj.Key,
			VersionID:    versionID,
			ReadingCount: recordCount,
		})
	}

	manifest := DailyManifest{
		SchemaVersion: 1,
		LocalDate:     localDate,
		TimeZone:      timezone,
		PeriodStart:   periodStart,
		PeriodEnd:     periodEnd,
		Hours:         hoursList,
	}

	manifestBytes, _ := json.Marshal(manifest)
	manifestKey := fmt.Sprintf("telemetry/daily/v1/%s/%s/manifest.json", patientID, localDate)

	_, putErr := ta.storage.client.PutObject(ctx, bucketName, manifestKey, bytes.NewReader(manifestBytes), int64(len(manifestBytes)), minio.PutObjectOptions{
		ContentType:          "application/json",
		DisableContentSha256: true,
	})
	return putErr
}

func (ta *TelemetryArchiver) getPatientTimezone(ctx context.Context, patientID string) string {
	var timezone string
	err := ta.db.QueryRowContext(ctx, "SELECT timezone FROM patient_profiles WHERE user_id = $1", patientID).Scan(&timezone)
	if err != nil || timezone == "" {
		return "UTC"
	}
	return timezone
}

func (ta *TelemetryArchiver) fetchHourlyChunkReadings(ctx context.Context, objectKey string) ([]TelemetryReading, error) {
	bucketName := ta.storage.bucketName
	obj, err := ta.storage.client.GetObject(ctx, bucketName, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, err
	}

	readings := make([]TelemetryReading, 0)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var r TelemetryReading
		if err := json.Unmarshal([]byte(trimmed), &r); err == nil {
			readings = append(readings, r)
		}
	}
	return readings, nil
}

func (ta *TelemetryArchiver) getHourlyMetadata(ctx context.Context, objectKey string) (int, string) {
	bucketName := ta.storage.bucketName
	// Get Version ID
	info, err := ta.storage.client.StatObject(ctx, bucketName, objectKey, minio.StatObjectOptions{})
	versionID := ""
	if err == nil {
		versionID = info.VersionID
	}

	// Calculate count
	obj, err := ta.storage.client.GetObject(ctx, bucketName, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return 0, versionID
	}
	defer obj.Close()

	data, err := io.ReadAll(obj)
	if err != nil {
		return 0, versionID
	}

	count := 0
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, versionID
}

func parseStreamMessage(msg redis.XMessage) (TelemetryReading, error) {
	var r TelemetryReading
	r.StreamID = msg.ID

	patID, ok := msg.Values["patient_id"].(string)
	if !ok || patID == "" {
		return r, fmt.Errorf("missing patient_id")
	}
	r.PatientID = patID

	recStr, ok := msg.Values["recorded_at"].(string)
	if !ok || recStr == "" {
		return r, fmt.Errorf("missing recorded_at")
	}
	t, err := time.Parse(time.RFC3339, recStr)
	if err != nil {
		return r, fmt.Errorf("invalid recorded_at format")
	}
	r.RecordedAt = t

	if hrStr, ok := msg.Values["heart_rate"].(string); ok {
		hr, _ := strconv.Atoi(hrStr)
		r.HeartRate = hr
	}
	if boStr, ok := msg.Values["blood_oxygen"].(string); ok {
		bo, _ := strconv.Atoi(boStr)
		r.BloodOxygen = bo
	}
	if devSN, ok := msg.Values["device_sn"].(string); ok {
		r.DeviceSN = devSN
	}
	if readID, ok := msg.Values["reading_id"].(string); ok {
		r.ReadingID = readID
	}

	return r, nil
}
