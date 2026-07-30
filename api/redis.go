package api

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

const (
	telemetryStreamKey    = "guardianband:telemetry"
	telemetryArchiveGroup = "guardianband:telemetry-archive"
)

func createRedisClient() (*redis.Client, error) {
	database, err := strconv.Atoi(getenv("REDIS_DB", "0"))
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_DB: %w", err)
	}

	return redis.NewClient(&redis.Options{
		Addr:     getenv("REDIS_ADDR", "localhost:6379"),
		Password: getenv("REDIS_PASSWORD", ""),
		DB:       database,
	}), nil
}

func initializeRedis(ctx context.Context, client *redis.Client) error {
	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}

	err := client.XGroupCreateMkStream(
		ctx,
		telemetryStreamKey,
		telemetryArchiveGroup,
		"0",
	).Err()
	if err != nil && !redis.HasErrorPrefix(err, "BUSYGROUP") {
		return fmt.Errorf("create telemetry consumer group: %w", err)
	}

	return nil
}

func patientHealthKey(patientID string) string {
	return fmt.Sprintf("guardianband:patient:%s:health", patientID)
}

func patientPresenceKey(patientID string) string {
	return fmt.Sprintf("guardianband:patient:%s:presence", patientID)
}
