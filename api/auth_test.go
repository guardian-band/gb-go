package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRegisterSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	reqBody := RegisterRequest{
		PhoneNumber: "+905551234567",
		Password:    "secure-password",
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/register", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	// 1. SELECT query expectation: No user found
	mock.ExpectQuery("^SELECT id FROM users WHERE phone_number = \\$1$").
		WithArgs(reqBody.PhoneNumber).
		WillReturnError(sql.ErrNoRows)

	// 2. INSERT query expectation: Success
	mock.ExpectExec("^INSERT INTO users").
		WithArgs(sqlmock.AnyArg(), reqBody.PhoneNumber, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.Register(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}

	var resp RegisterResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.PhoneNumber != reqBody.PhoneNumber {
		t.Errorf("expected phone number %s, got %s", reqBody.PhoneNumber, resp.PhoneNumber)
	}

	// UUID should match basic UUID regex
	uuidRegex := regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
	if !uuidRegex.MatchString(resp.ID) {
		t.Errorf("invalid response ID format, expected UUID: %s", resp.ID)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestRegisterInvalidPhoneNumber(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	invalidPhones := []string{"1234567", "05551234567", "+90555123456789012", "+0123", ""}
	for _, phone := range invalidPhones {
		reqBody := RegisterRequest{
			PhoneNumber: phone,
			Password:    "secure-password",
		}
		bodyBytes, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/api/register", bytes.NewReader(bodyBytes))
		rec := httptest.NewRecorder()

		a.Register(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("for phone %q: expected status 400, got %d", phone, rec.Code)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestRegisterShortPassword(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	reqBody := RegisterRequest{
		PhoneNumber: "+905551234567",
		Password:    "short",
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/register", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	a.Register(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for short password, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestRegisterDuplicateUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	reqBody := RegisterRequest{
		PhoneNumber: "+905551234567",
		Password:    "secure-password",
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/register", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	// Mock existing user ID returned from query
	mock.ExpectQuery("^SELECT id FROM users WHERE phone_number = \\$1$").
		WithArgs(reqBody.PhoneNumber).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("existing-uuid"))

	a.Register(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestRegisterDatabaseError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	reqBody := RegisterRequest{
		PhoneNumber: "+905551234567",
		Password:    "secure-password",
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/register", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT id FROM users WHERE phone_number = \\$1$").
		WithArgs(reqBody.PhoneNumber).
		WillReturnError(errors.New("connection failed"))

	a.Register(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestLoginSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	rawPassword := "secure-password"
	hashedPassword, _ := HashPassword(rawPassword)
	userUUID := "user-uuid-1234-5678"

	reqBody := AuthRequest{
		PhoneNumber: "+905551234567",
		Password:    rawPassword,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	// Mock DB returning matching credentials
	mock.ExpectQuery("^SELECT id, password_hash FROM users WHERE phone_number = \\$1$").
		WithArgs(reqBody.PhoneNumber).
		WillReturnRows(sqlmock.NewRows([]string{"id", "password_hash"}).AddRow(userUUID, hashedPassword))

	a.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp AuthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Token == "" {
		t.Error("expected JWT token in response, got empty string")
	}

	if resp.UserID != userUUID {
		t.Errorf("expected UserID %s in response, got %s", userUUID, resp.UserID)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	rawPassword := "correct-password"
	hashedPassword, _ := HashPassword(rawPassword)
	userUUID := "user-uuid-1234-5678"

	reqBody := AuthRequest{
		PhoneNumber: "+905551234567",
		Password:    "wrong-password",
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT id, password_hash FROM users WHERE phone_number = \\$1$").
		WithArgs(reqBody.PhoneNumber).
		WillReturnRows(sqlmock.NewRows([]string{"id", "password_hash"}).AddRow(userUUID, hashedPassword))

	a.Login(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 for wrong password, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestLoginUnknownUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	reqBody := AuthRequest{
		PhoneNumber: "+905551234567",
		Password:    "some-password",
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT id, password_hash FROM users WHERE phone_number = \\$1$").
		WithArgs(reqBody.PhoneNumber).
		WillReturnError(sql.ErrNoRows)

	a.Login(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 for unknown user, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestLoginInvalidRequest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	invalidRequests := []AuthRequest{
		{PhoneNumber: "", Password: "password123"},
		{PhoneNumber: "+905551234567", Password: ""},
	}

	for _, reqBody := range invalidRequests {
		bodyBytes, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(bodyBytes))
		rec := httptest.NewRecorder()

		a.Login(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("for request %+v: expected status 400, got %d", reqBody, rec.Code)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}
