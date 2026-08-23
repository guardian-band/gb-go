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

	// Seed test user, profile, and document for manual verification
	seedSQL := `
		INSERT INTO users (id, phone_number, password_hash, created_at)
		SELECT '607d83ca-be13-4258-88a4-c56adcec91d8', '+905551234567', '$2a$10$vI8K54.1Yk7Qf7DqL78LduC2jE2k1hCjO9dJ8H2JpX0Yk2Z2W2W2W', NOW()
		WHERE NOT EXISTS (SELECT 1 FROM users WHERE id = '607d83ca-be13-4258-88a4-c56adcec91d8');

		INSERT INTO patient_profiles (user_id, birth_date, blood_type, critical_facts, timezone)
		SELECT '607d83ca-be13-4258-88a4-c56adcec91d8', '1995-04-12', 'A+', '{"allergies":["penicillin"]}', 'Europe/Istanbul'
		WHERE NOT EXISTS (SELECT 1 FROM patient_profiles WHERE user_id = '607d83ca-be13-4258-88a4-c56adcec91d8');

		INSERT INTO documents (id, patient_id, kind, title, object_key, current_version_id, etag, content_type, created_by, created_at, updated_at) 
		SELECT '811a1234-abcd-ef01-2345-6789abcdef01', '607d83ca-be13-4258-88a4-c56adcec91d8', 'report', 'Genel Kan Analiz Raporu', 'reports/blood_test_2026.pdf', 'v1', '"etag-value-123"', 'application/pdf', '607d83ca-be13-4258-88a4-c56adcec91d8', NOW(), NOW()
		WHERE NOT EXISTS (SELECT 1 FROM documents WHERE id = '811a1234-abcd-ef01-2345-6789abcdef01');

		-- Medication Catalog Seed Data
		INSERT INTO medication_catalog (id, name, strength)
		SELECT '111a1234-abcd-ef01-2345-6789abcdef01', 'Parol', '500mg'
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog WHERE id = '111a1234-abcd-ef01-2345-6789abcdef01');

		INSERT INTO medication_catalog (id, name, strength)
		SELECT '222a1234-abcd-ef01-2345-6789abcdef02', 'Aspirin', '100mg'
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog WHERE id = '222a1234-abcd-ef01-2345-6789abcdef02');

		INSERT INTO medication_catalog (id, name, strength)
		SELECT '333a1234-abcd-ef01-2345-6789abcdef03', 'Augmentin', '1000mg'
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog WHERE id = '333a1234-abcd-ef01-2345-6789abcdef03');

		INSERT INTO medication_catalog (id, name, strength)
		SELECT '444a1234-abcd-ef01-2345-6789abcdef04', 'Lipitor', '20mg'
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog WHERE id = '444a1234-abcd-ef01-2345-6789abcdef04');

		INSERT INTO medication_catalog (id, name, strength)
		SELECT '555a1234-abcd-ef01-2345-6789abcdef05', 'Coraspin', '300mg'
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog WHERE id = '555a1234-abcd-ef01-2345-6789abcdef05');

		INSERT INTO medication_catalog_ingredients (id, catalog_item_id, ingredient_name, drugbank_id, ai_supported)
		SELECT '611a1234-abcd-ef01-2345-6789abcdef01', '111a1234-abcd-ef01-2345-6789abcdef01', 'acetaminophen', 'DB00316', TRUE
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog_ingredients WHERE catalog_item_id = '111a1234-abcd-ef01-2345-6789abcdef01' AND drugbank_id = 'DB00316');

		INSERT INTO medication_catalog_ingredients (id, catalog_item_id, ingredient_name, drugbank_id, ai_supported)
		SELECT '622a1234-abcd-ef01-2345-6789abcdef02', '222a1234-abcd-ef01-2345-6789abcdef02', 'acetylsalicylic acid', 'DB00945', TRUE
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog_ingredients WHERE catalog_item_id = '222a1234-abcd-ef01-2345-6789abcdef02' AND drugbank_id = 'DB00945');

		INSERT INTO medication_catalog_ingredients (id, catalog_item_id, ingredient_name, drugbank_id, ai_supported)
		SELECT '633a1234-abcd-ef01-2345-6789abcdef03', '333a1234-abcd-ef01-2345-6789abcdef03', 'amoxicillin', 'DB01060', FALSE
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog_ingredients WHERE catalog_item_id = '333a1234-abcd-ef01-2345-6789abcdef03' AND drugbank_id = 'DB01060');

		INSERT INTO medication_catalog_ingredients (id, catalog_item_id, ingredient_name, drugbank_id, ai_supported)
		SELECT '634a1234-abcd-ef01-2345-6789abcdef04', '333a1234-abcd-ef01-2345-6789abcdef03', 'clavulanic acid', 'DB00766', FALSE
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog_ingredients WHERE catalog_item_id = '333a1234-abcd-ef01-2345-6789abcdef03' AND drugbank_id = 'DB00766');

		INSERT INTO medication_catalog_ingredients (id, catalog_item_id, ingredient_name, drugbank_id, ai_supported)
		SELECT '644a1234-abcd-ef01-2345-6789abcdef04', '444a1234-abcd-ef01-2345-6789abcdef04', 'atorvastatin', 'DB01076', FALSE
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog_ingredients WHERE catalog_item_id = '444a1234-abcd-ef01-2345-6789abcdef04' AND drugbank_id = 'DB01076');

		INSERT INTO medication_catalog_ingredients (id, catalog_item_id, ingredient_name, drugbank_id, ai_supported)
		SELECT '655a1234-abcd-ef01-2345-6789abcdef05', '555a1234-abcd-ef01-2345-6789abcdef05', 'acetylsalicylic acid', 'DB00945', TRUE
		WHERE NOT EXISTS (SELECT 1 FROM medication_catalog_ingredients WHERE catalog_item_id = '555a1234-abcd-ef01-2345-6789abcdef05' AND drugbank_id = 'DB00945');`
	
	if _, err := db.ExecContext(ctx, seedSQL); err != nil {
		fmt.Printf("Database Seeding Warning: %s\n", err.Error())
	}

	return nil
}

func getenv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}

	return fallback
}
