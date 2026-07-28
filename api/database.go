package api

import (
	"database/sql"
	"fmt"
	"os"

	_ "gitcode.com/opengauss/openGauss-connector-go-pq"
)

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

func getenv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}

	return fallback
}
