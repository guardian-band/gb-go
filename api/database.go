package api

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"

	_ "gitcode.com/opengauss/openGauss-connector-go-pq"
)

//go:embed schema.sql
var databaseSchema string

// createConnection creates a database handle using the application's database
// environment variables. The returned handle is safe for concurrent use.
func createConnection() (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		getenv("DB_HOST", "localhost"),
		getenv("DB_PORT", "5432"),
		getenv("DB_USER", "gaussdb"),
		getenv("DB_PASSWORD", ""),
		getenv("DB_NAME", "postgres"),
		getenv("DB_SSLMODE", "disable"),
	)

	return sql.Open("opengauss", dsn)
}

func initializeDatabase(ctx context.Context, db *sql.DB) error {
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	if _, err := db.ExecContext(ctx, databaseSchema); err != nil {
		return fmt.Errorf("initialize database schema: %w", err)
	}

	return nil
}

func getenv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}

	return fallback
}
