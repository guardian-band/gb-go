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
	"github.com/go-chi/chi/v5"
)

func TestGetMedicationCatalog(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}

	req := httptest.NewRequest(http.MethodGet, "/api/medication-catalog?search=Parol", nil)
	rec := httptest.NewRecorder()

	mock.ExpectQuery("SELECT c.id, c.name, c.strength").
		WithArgs("%Parol%").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "strength", "ingredient_name", "drugbank_id", "ai_supported"}).
			AddRow("med-1", "Parol", "500mg", "acetaminophen", "DB00316", true))

	a.GetMedicationCatalogHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp []MedicationCatalogItem
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp) != 1 || resp[0].Name != "Parol" {
		t.Errorf("unexpected catalog items: %+v", resp)
	}
	if len(resp[0].Ingredients) != 1 || resp[0].Ingredients[0].DrugBankID != "DB00316" || !resp[0].Ingredients[0].AISupported {
		t.Errorf("unexpected catalog ingredients: %+v", resp[0].Ingredients)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationUsesCatalogIdentityAndAuthoritativeDetails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	catalogID := "111a1234-abcd-ef01-2345-6789abcdef01"
	reqBody := CreateMedicationRequest{
		CatalogItemID:  &catalogID,
		Name:           "untrusted client name",
		Strength:       "untrusted strength",
		FrequencyHours: 12,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery("^SELECT name, strength FROM medication_catalog WHERE id = \\$1$").
		WithArgs(catalogID).
		WillReturnRows(sqlmock.NewRows([]string{"name", "strength"}).AddRow("Parol", "500mg"))
	mock.ExpectExec("INSERT INTO medications").
		WithArgs(sqlmock.AnyArg(), userID, sqlmock.AnyArg(), catalogID, "Parol", "500mg", reqBody.Instructions, sqlmock.AnyArg(), true).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp MedicationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.CatalogItemID == nil || *resp.CatalogItemID != catalogID || resp.Name != "Parol" || resp.Strength != "500mg" {
		t.Errorf("unexpected medication response: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	reqBody := CreateMedicationRequest{
		Name:           "Parol",
		Strength:       "500mg",
		Instructions:   "Günde 2 kez",
		FrequencyHours: 12,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))

	mock.ExpectExec("INSERT INTO medications").
		WithArgs(sqlmock.AnyArg(), userID, sqlmock.AnyArg(), sqlmock.AnyArg(), reqBody.Name, reqBody.Strength, reqBody.Instructions, sqlmock.AnyArg(), true).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationSuccess8Hours(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	reqBody := CreateMedicationRequest{
		Name:           "Parol",
		Strength:       "500mg",
		Instructions:   "Günde 3 kez",
		FrequencyHours: 8,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))

	mock.ExpectExec("INSERT INTO medications").
		WithArgs(sqlmock.AnyArg(), userID, sqlmock.AnyArg(), sqlmock.AnyArg(), reqBody.Name, reqBody.Strength, reqBody.Instructions, sqlmock.AnyArg(), true).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201 for 8 hours frequency, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationInvalidFrequency(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	reqBody := CreateMedicationRequest{
		Name:           "Parol",
		Strength:       "500mg",
		Instructions:   "Günde 2 kez",
		FrequencyHours: 5, // invalid frequency
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationWithPrescriptionSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	prescriptionDocID := "doc-uuid-abc"

	reqBody := CreateMedicationRequest{
		Name:                   "Parol",
		Strength:               "500mg",
		Instructions:           "Günde 2 kez",
		FrequencyHours:         12,
		PrescriptionDocumentID: &prescriptionDocID,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	// Mock DB check for patient profile
	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))

	// Mock DB check for prescription
	mock.ExpectQuery("^SELECT patient_id, kind FROM documents WHERE id = \\$1").
		WithArgs(prescriptionDocID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "kind"}).
			AddRow(userID, "prescription"))

	mock.ExpectExec("INSERT INTO medications").
		WithArgs(sqlmock.AnyArg(), userID, prescriptionDocID, sqlmock.AnyArg(), reqBody.Name, reqBody.Strength, reqBody.Instructions, sqlmock.AnyArg(), true).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationWithPrescriptionForeignOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	otherUserID := "user-uuid-999"
	prescriptionDocID := "doc-uuid-abc"

	reqBody := CreateMedicationRequest{
		Name:                   "Parol",
		Strength:               "500mg",
		Instructions:           "Günde 2 kez",
		FrequencyHours:         12,
		PrescriptionDocumentID: &prescriptionDocID,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	// Mock DB check for patient profile
	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))

	// Mock DB returning other user as owner
	mock.ExpectQuery("^SELECT patient_id, kind FROM documents WHERE id = \\$1").
		WithArgs(prescriptionDocID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "kind"}).
			AddRow(otherUserID, "prescription"))

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for foreign prescription, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestGetMedicationsList(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	req := httptest.NewRequest(http.MethodGet, "/api/medications", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	now := time.Now()
	lastTaken := now.Add(-6 * time.Hour) // 6 hours ago

	mock.ExpectQuery("SELECT m.id, m.catalog_item_id, m.name, m.strength, m.instructions, m.schedule").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "catalog_item_id", "name", "strength", "instructions", "schedule", "last_taken_at"}).
			AddRow("med-1", "111a1234-abcd-ef01-2345-6789abcdef01", "Parol", "500mg", "Günde 2 kez", []byte(`{"frequency_hours": 12}`), lastTaken).
			AddRow("med-2", nil, "Aspirin", "100mg", "Günde 1 kez", []byte(`{"frequency_hours": 24}`), sql.NullTime{}))

	a.GetMedicationsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp []MedicationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp) != 2 {
		t.Errorf("expected 2 plans, got %d", len(resp))
	}
	if resp[0].CatalogItemID == nil || *resp[0].CatalogItemID != "111a1234-abcd-ef01-2345-6789abcdef01" || resp[1].CatalogItemID != nil {
		t.Errorf("unexpected catalog identities: %+v", resp)
	}

	// Dynamic calculation verification
	// First item lastTakenAt = 6h ago, freq = 12h, nextScheduledFor should be lastTaken + 12h = now + 6h
	if resp[0].LastTakenAt == nil || resp[0].NextScheduledFor == nil {
		t.Error("expected LastTakenAt and NextScheduledFor to be populated")
	} else {
		expectedNext := lastTaken.Add(12 * time.Hour)
		if !resp[0].NextScheduledFor.Equal(expectedNext) {
			t.Errorf("unexpected nextScheduledFor calculation: got %v, expected %v", resp[0].NextScheduledFor, expectedNext)
		}
	}

	// Second item has not been taken yet, so NextScheduledFor and LastTakenAt should be nil
	if resp[1].LastTakenAt != nil || resp[1].NextScheduledFor != nil {
		t.Error("expected empty LastTakenAt and NextScheduledFor for untaken medication")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationTakenSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	medID := "med-uuid-abc"

	req := httptest.NewRequest(http.MethodPost, "/api/medications/"+medID+"/taken", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("medicationId", medID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT patient_id, schedule FROM medications WHERE id = \\$1 AND active = true").
		WithArgs(medID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "schedule"}).
			AddRow(userID, []byte(`{"frequency_hours": 12}`)))

	mock.ExpectExec("INSERT INTO medication_events").
		WithArgs(sqlmock.AnyArg(), medID, sqlmock.AnyArg(), "taken", sqlmock.AnyArg(), userID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.PostMedicationTakenHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp TakenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// nextScheduledFor should be roughly NOW() + 12 hours
	expectedNextLower := time.Now().Add(11 * time.Hour)
	expectedNextUpper := time.Now().Add(13 * time.Hour)
	if resp.NextScheduledFor.Before(expectedNextLower) || resp.NextScheduledFor.After(expectedNextUpper) {
		t.Errorf("unexpected nextScheduledFor time calculation: %v", resp.NextScheduledFor)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationTakenForbidden(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	otherUserID := "user-uuid-999"
	medID := "med-uuid-abc"

	req := httptest.NewRequest(http.MethodPost, "/api/medications/"+medID+"/taken", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("medicationId", medID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	// Return otherUserID as patient_id
	mock.ExpectQuery("^SELECT patient_id, schedule FROM medications WHERE id = \\$1 AND active = true").
		WithArgs(medID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id", "schedule"}).
			AddRow(otherUserID, []byte(`{"frequency_hours": 12}`)))

	a.PostMedicationTakenHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestDeleteMedicationHandlerSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"
	medID := "med-uuid-abc"

	req := httptest.NewRequest(http.MethodDelete, "/api/medications/"+medID, nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("medicationId", medID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT patient_id FROM medications WHERE id = \\$1 AND active = true$").
		WithArgs(medID).
		WillReturnRows(sqlmock.NewRows([]string{"patient_id"}).AddRow(userID))

	mock.ExpectExec("^UPDATE medications SET active = false WHERE id = \\$1 AND patient_id = \\$2$").
		WithArgs(medID, userID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a.DeleteMedicationHandler(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rec.Code, rec.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}

func TestPostMedicationProfileNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	a := &API{db: db}
	userID := "user-uuid-123"

	reqBody := CreateMedicationRequest{
		Name:           "Parol",
		Strength:       "500mg",
		Instructions:   "Günde 2 kez",
		FrequencyHours: 12,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/medications", bytes.NewReader(bodyBytes))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1").
		WithArgs(userID).
		WillReturnError(sql.ErrNoRows)

	a.PostMedicationHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["message"] != "patient profile not found" {
		t.Errorf("expected 'patient profile not found', got %q", resp["message"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations were not met: %v", err)
	}
}
