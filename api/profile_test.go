package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetProfileSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	req := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
	// Inject user ID context mimicking middleware
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	expectedBirthDate := time.Date(1995, 4, 12, 0, 0, 0, 0, time.UTC)
	expectedBloodType := "A+"
	expectedCriticalFacts := `{"allergies": ["penicillin"]}`
	expectedTimezone := "Europe/Istanbul"

	mock.ExpectQuery("^SELECT birth_date, blood_type, critical_facts, timezone FROM patient_profiles WHERE user_id = \\$1$").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"birth_date", "blood_type", "critical_facts", "timezone"}).
			AddRow(expectedBirthDate, expectedBloodType, []byte(expectedCriticalFacts), expectedTimezone))
	mock.ExpectQuery("^SELECT display_name FROM users WHERE id = \\$1$").
		WithArgs(userID).WillReturnRows(sqlmock.NewRows([]string{"display_name"}).AddRow("Ada"))

	a.GetProfileHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp PatientProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.BirthDate != "1995-04-12" || resp.BloodType != expectedBloodType || resp.Timezone != expectedTimezone {
		t.Errorf("unexpected response data: %+v", resp)
	}
	if resp.DisplayName != "Ada" {
		t.Errorf("expected display name Ada, got %q", resp.DisplayName)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestGetProfileNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	req := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT birth_date, blood_type, critical_facts, timezone FROM patient_profiles WHERE user_id = \\$1$").
		WithArgs(userID).
		WillReturnError(sql.ErrNoRows)

	a.GetProfileHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestGetProfileUnauthorized(t *testing.T) {
	a := &API{}

	req := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
	rec := httptest.NewRecorder()

	// Call handler without context
	a.GetProfileHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestPutProfileSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	reqBody := PatientProfile{
		BirthDate:     "1995-04-12",
		BloodType:     "A+",
		CriticalFacts: json.RawMessage(`{"allergies":["penicillin"]}`),
		Timezone:      "Europe/Istanbul",
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPut, "/api/profile", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	parsedTime, _ := time.Parse("2006-01-02", reqBody.BirthDate)

	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1$").
		WithArgs(userID).
		WillReturnError(sql.ErrNoRows)

	mock.ExpectExec("INSERT INTO patient_profiles").
		WithArgs(userID, parsedTime, reqBody.BloodType, []byte(reqBody.CriticalFacts), reqBody.Timezone).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PutProfileHandler(rec, req)

	t.Logf("Response body: %s", rec.Body.String())

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp PatientProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.BirthDate != reqBody.BirthDate || resp.BloodType != reqBody.BloodType || resp.Timezone != reqBody.Timezone {
		t.Errorf("unexpected response data: %+v", resp)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPutProfileInvalidData(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	invalidProfiles := []PatientProfile{
		// Future date
		{BirthDate: time.Now().Add(24 * time.Hour).Format("2006-01-02"), BloodType: "A+", CriticalFacts: json.RawMessage("{}"), Timezone: "Europe/Istanbul"},
		// Invalid date format
		{BirthDate: "12-04-1995", BloodType: "A+", CriticalFacts: json.RawMessage("{}"), Timezone: "Europe/Istanbul"},
		// Invalid blood type
		{BirthDate: "1995-04-12", BloodType: "X+", CriticalFacts: json.RawMessage("{}"), Timezone: "Europe/Istanbul"},
		// Invalid timezone
		{BirthDate: "1995-04-12", BloodType: "A+", CriticalFacts: json.RawMessage("{}"), Timezone: "Europe/InvalidTimezone"},
		// Invalid criticalFacts JSON
		{BirthDate: "1995-04-12", BloodType: "A+", CriticalFacts: json.RawMessage("{bad json}"), Timezone: "Europe/Istanbul"},
	}

	for _, profile := range invalidProfiles {
		bodyBytes, _ := json.Marshal(profile)
		req := httptest.NewRequest(http.MethodPut, "/api/profile", bytes.NewReader(bodyBytes))
		req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
		rec := httptest.NewRecorder()

		a.PutProfileHandler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("for profile %+v: expected status 400, got %d", profile, rec.Code)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPutProfileDisplayNameUpdateRollsBackProfile(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	userID := "user-uuid-123"
	reqBody := PatientProfile{
		DisplayName:   "Ada",
		BirthDate:     "1995-04-12",
		BloodType:     "A+",
		CriticalFacts: json.RawMessage(`{}`),
		Timezone:      "Europe/Istanbul",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPut, "/api/profile", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	mock.ExpectBegin()
	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1$").
		WithArgs(userID).WillReturnError(sql.ErrNoRows)
	parsedTime, _ := time.Parse("2006-01-02", reqBody.BirthDate)
	mock.ExpectExec("INSERT INTO patient_profiles").
		WithArgs(userID, parsedTime, reqBody.BloodType, []byte(reqBody.CriticalFacts), reqBody.Timezone).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^UPDATE users SET display_name = \\$1 WHERE id = \\$2$").
		WithArgs("Ada", userID).WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	rec := httptest.NewRecorder()
	a.PutProfileHandler(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
