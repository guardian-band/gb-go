package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
)

func TestRegisterStoresDisplayName(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	a := &API{db: db}
	body, _ := json.Marshal(RegisterRequest{PhoneNumber: "+905551234567", Password: "secure-password", DisplayName: "Ada"})
	req := httptest.NewRequest(http.MethodPost, "/api/register", bytes.NewReader(body))

	mock.ExpectQuery("^SELECT id FROM users WHERE phone_number = \\$1$").
		WithArgs("+905551234567").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("^INSERT INTO users").
		WithArgs(sqlmock.AnyArg(), "+905551234567", sqlmock.AnyArg(), sqlmock.AnyArg(), "Ada").
		WillReturnResult(sqlmock.NewResult(1, 1))

	rec := httptest.NewRecorder()
	a.Register(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var got RegisterResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Ada" {
		t.Fatalf("expected display name Ada, got %q", got.DisplayName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMonitoringRosterRequiresRelationship(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	a := &API{db: db}
	req := httptest.NewRequest(http.MethodGet, "/api/monitoring/patients", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "member-1"))
	mock.ExpectQuery("^SELECT pr.id, pr.patient_id").
		WithArgs("member-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "patient_id", "display_name", "relationship", "blood_type", "birth_date", "phone"}))

	rec := httptest.NewRecorder()
	a.GetMonitoringPatientsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "[]\n" {
		t.Fatalf("expected empty roster, got %s", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMonitoringRosterReturnsSafePopulatedPatient(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	req := httptest.NewRequest(http.MethodGet, "/api/monitoring/patients", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "member-1"))
	birthDate := time.Date(1995, 4, 12, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("^SELECT pr.id, pr.patient_id").WithArgs("member-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "patient_id", "display_name", "relationship", "blood_type", "birth_date", "phone"}).
			AddRow("rel-1", "patient-1", "Ada Patient", "daughter", "A+", birthDate, ""))
	rec := httptest.NewRecorder()
	a.GetMonitoringPatientsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []MonitoringPatient
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PatientID != "patient-1" || got[0].DisplayName != "Ada Patient" || got[0].BloodType != "A+" || got[0].BirthDate != "1995-04-12" {
		t.Fatalf("unexpected roster: %+v", got)
	}
	if strings.Contains(rec.Body.String(), "phone") || strings.Contains(rec.Body.String(), "canMonitor") {
		t.Fatalf("roster leaked private or authorization fields: %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetPatientRelationshipsScopedAndSafe(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	req := httptest.NewRequest(http.MethodGet, "/api/patient-relationships", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "patient-1"))
	created := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("^SELECT pr.id, pr.member_user_id").WithArgs("patient-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "member_user_id", "display_name", "relationship", "kind", "can_monitor", "is_emergency_contact", "active", "created_at", "revoked_at"}).
			AddRow("rel-1", "member-1", "Ada Member", "daughter", "family", true, false, true, created, nil))
	rec := httptest.NewRecorder()
	a.GetPatientRelationshipsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []PatientRelationshipResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "rel-1" || got[0].MemberUserID != "member-1" || !got[0].Active {
		t.Fatalf("unexpected relationships: %+v", got)
	}
	if strings.Contains(rec.Body.String(), "phone") || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("relationship response leaked private fields: %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMonitoringRelationshipPredicateRequiresActiveCanMonitor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	mock.ExpectQuery("^SELECT EXISTS").WithArgs("patient-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	allowed, err := a.hasMonitoringRelationship(context.Background(), "patient-1", "member-1")
	if err != nil || !allowed {
		t.Fatalf("expected authorized relationship, allowed=%v err=%v", allowed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("^SELECT EXISTS").WithArgs("patient-1", "member-2").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	allowed, err = a.hasMonitoringRelationship(context.Background(), "patient-1", "member-2")
	if err != nil || allowed {
		t.Fatalf("expected denied relationship, allowed=%v err=%v", allowed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEmergencyContactCreateUsesAuthenticatedPatientOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	primary := true
	body, _ := json.Marshal(EmergencyContactRequest{DisplayName: "Mom", Phone: "+905551111111", Relationship: "mother", IsPrimary: &primary})
	req := httptest.NewRequest(http.MethodPost, "/api/emergency-contacts", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "patient-1"))
	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1").
		WithArgs("patient-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))

	mock.ExpectExec("^INSERT INTO emergency_contacts").
		WithArgs(sqlmock.AnyArg(), "patient-1", "Mom", "+905551111111", "mother", true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	rec := httptest.NewRecorder()
	a.PostEmergencyContactHandler(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "patient_relationship") || strings.Contains(rec.Body.String(), "canMonitor") {
		t.Fatalf("external contact response leaked relationship authorization: %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEmergencyContactPatchAndDeleteArePatientScoped(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("contactId", "contact-1")
	body, _ := json.Marshal(EmergencyContactRequest{DisplayName: "Updated"})
	req := httptest.NewRequest(http.MethodPatch, "/api/emergency-contacts/contact-1", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "patient-1"))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	mock.ExpectExec("^UPDATE emergency_contacts SET display_name = \\$1 WHERE id = \\$2 AND patient_id = \\$3$").
		WithArgs("Updated", "contact-1", "patient-1").WillReturnResult(sqlmock.NewResult(1, 1))
	rec := httptest.NewRecorder()
	a.PatchEmergencyContactHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/emergency-contacts/contact-1", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "patient-1"))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	mock.ExpectExec("^DELETE FROM emergency_contacts WHERE id = \\$1 AND patient_id = \\$2$").
		WithArgs("contact-1", "patient-1").WillReturnResult(sqlmock.NewResult(1, 1))
	rec = httptest.NewRecorder()
	a.DeleteEmergencyContactHandler(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEmergencyContactPutSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("contactId", "contact-1")

	isPrimary := true
	reqBody := EmergencyContactRequest{
		DisplayName:  "Mother",
		Phone:        "+905551112233",
		Relationship: "Mother",
		IsPrimary:    &isPrimary,
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPut, "/api/emergency-contacts/contact-1", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "patient-1"))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))

	mock.ExpectExec("^UPDATE emergency_contacts").
		WithArgs("Mother", "+905551112233", "Mother", true, "contact-1", "patient-1").
		WillReturnResult(sqlmock.NewResult(1, 1))

	rec := httptest.NewRecorder()
	a.PutEmergencyContactHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp EmergencyContactResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.DisplayName != "Mother" || resp.Phone != "+905551112233" || resp.Relationship != "Mother" || !resp.IsPrimary {
		t.Errorf("unexpected updated response data: %+v", resp)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateMonitoringInvitationReturnsOpaqueToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	reqBody, _ := json.Marshal(CreateMonitoringInvitationRequest{
		Relationship: "daughter", Kind: "family", CanMonitor: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/patient-link-invitations", bytes.NewReader(reqBody))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "patient-1"))
	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1$").
		WithArgs("patient-1").WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))
	mock.ExpectExec("^INSERT INTO patient_link_invitations").
		WithArgs(sqlmock.AnyArg(), "patient-1", sqlmock.AnyArg(), "daughter", "family", true, false, false, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	rec := httptest.NewRecorder()
	a.CreateMonitoringInvitationHandler(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var got MonitoringInvitationResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Token) < 40 || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(got.Token) {
		t.Fatalf("token is not an opaque URL-safe value: %q", got.Token)
	}
	if !got.ExpiresAt.After(time.Now()) {
		t.Fatal("invitation must expire in the future")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateMonitoringInvitationRequiresPatientProfile(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	body, _ := json.Marshal(CreateMonitoringInvitationRequest{Relationship: "daughter", Kind: "family", CanMonitor: true})
	req := httptest.NewRequest(http.MethodPost, "/api/patient-link-invitations", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "not-a-patient"))
	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1$").
		WithArgs("not-a-patient").WillReturnError(sql.ErrNoRows)
	rec := httptest.NewRecorder()
	a.CreateMonitoringInvitationHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRedeemMonitoringInvitationAtomicallyCreatesRelationship(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	token := "opaque-token"
	reqBody, _ := json.Marshal(RedeemMonitoringInvitationRequest{Token: token})
	req := httptest.NewRequest(http.MethodPost, "/api/patient-link-invitations/redeem", bytes.NewReader(reqBody))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "member-1"))
	mock.ExpectBegin()
	mock.ExpectQuery("^SELECT id, patient_id, relationship, kind, can_monitor,").
		WithArgs(hashInvitationToken(token)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "patient_id", "relationship", "kind", "can_monitor", "share_phone_number", "is_emergency_contact", "expires_at"}).
			AddRow("invite-1", "patient-1", "daughter", "family", true, false, false, time.Now().Add(time.Hour)))
	mock.ExpectQuery("^UPDATE patient_relationships").
		WithArgs("patient-1", "member-1", "daughter", "family", true, false, false).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("^INSERT INTO patient_relationships").
		WithArgs(sqlmock.AnyArg(), "patient-1", "member-1", "daughter", "family", true, false, false).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("relationship-1"))
	mock.ExpectExec("^UPDATE patient_link_invitations").
		WithArgs("invite-1").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	rec := httptest.NewRecorder()
	a.RedeemMonitoringInvitationHandler(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var got MonitoringRelationshipResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "relationship-1" || got.PatientID != "patient-1" || got.MemberUserID != "member-1" {
		t.Fatalf("unexpected relationship response: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRedeemMonitoringInvitationRejectsSelfLinkAndRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	token := "self-token"
	body, _ := json.Marshal(RedeemMonitoringInvitationRequest{Token: token})
	req := httptest.NewRequest(http.MethodPost, "/api/patient-link-invitations/redeem", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "patient-1"))
	mock.ExpectBegin()
	mock.ExpectQuery("^SELECT id, patient_id, relationship, kind, can_monitor,").
		WithArgs(hashInvitationToken(token)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "patient_id", "relationship", "kind", "can_monitor", "share_phone_number", "is_emergency_contact", "expires_at"}).
			AddRow("invite-1", "patient-1", "daughter", "family", true, false, false, time.Now().Add(time.Hour)))
	mock.ExpectRollback()
	rec := httptest.NewRecorder()
	a.RedeemMonitoringInvitationHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRedeemMonitoringInvitationRejectsExpiredOrUsedToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	body, _ := json.Marshal(RedeemMonitoringInvitationRequest{Token: "expired-or-used"})
	req := httptest.NewRequest(http.MethodPost, "/api/patient-link-invitations/redeem", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "member-1"))
	mock.ExpectBegin()
	mock.ExpectQuery("^SELECT id, patient_id, relationship, kind, can_monitor,").
		WithArgs(hashInvitationToken("expired-or-used")).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	rec := httptest.NewRecorder()
	a.RedeemMonitoringInvitationHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeMonitoringRelationshipRequiresPatientOwnership(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	req := httptest.NewRequest(http.MethodDelete, "/api/patient-relationships/relationship-1", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "patient-1"))
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("relationshipId", "relationship-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	mock.ExpectExec("^UPDATE patient_relationships").
		WithArgs("relationship-1", "patient-1").WillReturnResult(sqlmock.NewResult(1, 1))
	rec := httptest.NewRecorder()
	a.RevokeMonitoringRelationshipHandler(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostEmergencyContactProfileNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	primary := true
	body, _ := json.Marshal(EmergencyContactRequest{DisplayName: "Mom", Phone: "+905551111111", Relationship: "mother", IsPrimary: &primary})
	req := httptest.NewRequest(http.MethodPost, "/api/emergency-contacts", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "patient-1"))
	mock.ExpectQuery("^SELECT 1 FROM patient_profiles WHERE user_id = \\$1").
		WithArgs("patient-1").
		WillReturnError(sql.ErrNoRows)

	rec := httptest.NewRecorder()
	a.PostEmergencyContactHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["message"] != "patient profile not found" {
		t.Errorf("expected 'patient profile not found', got %q", resp["message"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMonitoringRosterReturnsPhoneIfShared(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &API{db: db}
	req := httptest.NewRequest(http.MethodGet, "/api/monitoring/patients", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, "member-1"))
	birthDate := time.Date(1995, 4, 12, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("^SELECT pr.id, pr.patient_id").WithArgs("member-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "patient_id", "display_name", "relationship", "blood_type", "birth_date", "phone"}).
			AddRow("rel-1", "patient-1", "Ada Patient", "daughter", "A+", birthDate, "+905557654321"))
	rec := httptest.NewRecorder()
	a.GetMonitoringPatientsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []MonitoringPatient
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PatientID != "patient-1" || got[0].Phone != "+905557654321" {
		t.Fatalf("unexpected roster phone: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
