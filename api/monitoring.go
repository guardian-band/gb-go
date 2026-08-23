package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	defaultInvitationLifetime = 15 * time.Minute
	maxInvitationLifetime     = time.Hour
)

// MonitoringPatient is the intentionally small reverse roster exposed to a
// monitoring member. It contains no phone, authentication, or authorization
// internals.
type MonitoringPatient struct {
	ID           string `json:"id"`
	PatientID    string `json:"patientId"`
	DisplayName  string `json:"displayName"`
	Name         string `json:"name"`
	Relationship string `json:"relationship"`
	BloodType    string `json:"bloodType"`
	BirthDate    string `json:"birthDate"`
}

type CreateMonitoringInvitationRequest struct {
	Relationship       string `json:"relationship"`
	Kind               string `json:"kind"`
	CanMonitor         bool   `json:"canMonitor"`
	IsEmergencyContact bool   `json:"isEmergencyContact"`
	ExpiresInSeconds   int    `json:"expiresInSeconds,omitempty"`
}

type MonitoringInvitationResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type RedeemMonitoringInvitationRequest struct {
	Token string `json:"token"`
}

type MonitoringRelationshipResponse struct {
	ID                 string `json:"id"`
	PatientID          string `json:"patientId"`
	MemberUserID       string `json:"memberUserId"`
	Relationship       string `json:"relationship"`
	Kind               string `json:"kind"`
	CanMonitor         bool   `json:"canMonitor"`
	IsEmergencyContact bool   `json:"isEmergencyContact"`
}

type PatientRelationshipResponse struct {
	ID                 string     `json:"id"`
	MemberUserID       string     `json:"memberUserId"`
	MemberDisplayName  string     `json:"memberDisplayName"`
	Relationship       string     `json:"relationship"`
	Kind               string     `json:"kind"`
	CanMonitor         bool       `json:"canMonitor"`
	IsEmergencyContact bool       `json:"isEmergencyContact"`
	Active             bool       `json:"active"`
	CreatedAt          time.Time  `json:"createdAt"`
	RevokedAt          *time.Time `json:"revokedAt,omitempty"`
}

type EmergencyContactResponse struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	Phone        string `json:"phone"`
	Relationship string `json:"relationship"`
	IsPrimary    bool   `json:"isPrimary"`
}

type EmergencyContactRequest struct {
	DisplayName  string `json:"displayName"`
	Phone        string `json:"phone"`
	Relationship string `json:"relationship"`
	IsPrimary    *bool  `json:"isPrimary,omitempty"`
}

func userIDFromContext(r *http.Request) (string, bool) {
	id, ok := r.Context().Value(UserContextKey).(string)
	return id, ok && strings.TrimSpace(id) != ""
}

func validRelationshipKind(kind string) bool {
	return kind == "family" || kind == "clinician" || kind == "other"
}

func hashInvitationToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newInvitationToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// GetMonitoringPatientsHandler returns active, explicitly approved monitoring
// relationships where the authenticated user is the member.
func (a *API) GetMonitoringPatientsHandler(w http.ResponseWriter, r *http.Request) {
	memberID, ok := userIDFromContext(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rows, err := a.db.QueryContext(r.Context(), `
		SELECT pr.id, pr.patient_id, COALESCE(u.display_name, ''), pr.relationship,
		       pp.blood_type, pp.birth_date
		FROM patient_relationships pr
		JOIN users u ON u.id = pr.patient_id
		JOIN patient_profiles pp ON pp.user_id = pr.patient_id
		WHERE pr.member_user_id = $1
		  AND pr.can_monitor = TRUE
		  AND pr.active = TRUE
		  AND pr.revoked_at IS NULL
		ORDER BY pr.created_at DESC`, memberID)
	if err != nil {
		http.Error(w, "failed to query monitoring patients", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	patients := make([]MonitoringPatient, 0)
	for rows.Next() {
		var patient MonitoringPatient
		var bloodType sql.NullString
		var birthDate sql.NullTime
		if err := rows.Scan(&patient.ID, &patient.PatientID, &patient.DisplayName, &patient.Relationship, &bloodType, &birthDate); err != nil {
			http.Error(w, "failed to scan monitoring patients", http.StatusInternalServerError)
			return
		}
		patient.Name = patient.DisplayName
		if bloodType.Valid {
			patient.BloodType = bloodType.String
		}
		if birthDate.Valid {
			patient.BirthDate = birthDate.Time.Format("2006-01-02")
		}
		patients = append(patients, patient)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read monitoring patients", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(patients)
}

// GetPatientRelationshipsHandler returns the patient's own registered
// relationship records. The query is always scoped by the JWT patient id.
func (a *API) GetPatientRelationshipsHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := userIDFromContext(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT pr.id, pr.member_user_id, COALESCE(u.display_name, ''),
		       pr.relationship, pr.kind, pr.can_monitor, pr.is_emergency_contact,
		       pr.active, pr.created_at, pr.revoked_at
		FROM patient_relationships pr
		JOIN users u ON u.id = pr.member_user_id
		WHERE pr.patient_id = $1
		ORDER BY pr.created_at DESC`, patientID)
	if err != nil {
		http.Error(w, "failed to query patient relationships", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	relationships := make([]PatientRelationshipResponse, 0)
	for rows.Next() {
		var relationship PatientRelationshipResponse
		var revokedAt sql.NullTime
		if err := rows.Scan(&relationship.ID, &relationship.MemberUserID, &relationship.MemberDisplayName,
			&relationship.Relationship, &relationship.Kind, &relationship.CanMonitor,
			&relationship.IsEmergencyContact, &relationship.Active, &relationship.CreatedAt, &revokedAt); err != nil {
			http.Error(w, "failed to scan patient relationships", http.StatusInternalServerError)
			return
		}
		if revokedAt.Valid {
			relationship.RevokedAt = &revokedAt.Time
		}
		relationships = append(relationships, relationship)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read patient relationships", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(relationships)
}

// CreateMonitoringInvitationHandler creates a single-use, short-lived
// invitation. Only the patient in the JWT may create it.
func (a *API) CreateMonitoringInvitationHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := userIDFromContext(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var profileExists int
	err := a.db.QueryRowContext(r.Context(), "SELECT 1 FROM patient_profiles WHERE user_id = $1", patientID).Scan(&profileExists)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "patient profile not found", http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, "failed to verify patient profile", http.StatusInternalServerError)
		return
	}

	var req CreateMonitoringInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Relationship = strings.TrimSpace(req.Relationship)
	if req.Relationship == "" || len(req.Relationship) > 64 || !validRelationshipKind(req.Kind) || !req.CanMonitor {
		http.Error(w, "invalid relationship or kind", http.StatusBadRequest)
		return
	}
	lifetime := defaultInvitationLifetime
	if req.ExpiresInSeconds != 0 {
		if req.ExpiresInSeconds < 1 || time.Duration(req.ExpiresInSeconds)*time.Second > maxInvitationLifetime {
			http.Error(w, "invalid invitation expiry", http.StatusBadRequest)
			return
		}
		lifetime = time.Duration(req.ExpiresInSeconds) * time.Second
	}
	token, err := newInvitationToken()
	if err != nil {
		http.Error(w, "failed to create invitation", http.StatusInternalServerError)
		return
	}
	expiresAt := time.Now().UTC().Add(lifetime)
	_, err = a.db.ExecContext(r.Context(), `
		INSERT INTO patient_link_invitations
			(id, patient_id, token_hash, relationship, kind, can_monitor,
			 is_emergency_contact, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		uuid.New().String(), patientID, hashInvitationToken(token), req.Relationship,
		req.Kind, req.CanMonitor, req.IsEmergencyContact, expiresAt)
	if err != nil {
		http.Error(w, "failed to create invitation", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(MonitoringInvitationResponse{Token: token, ExpiresAt: expiresAt})
}

// RedeemMonitoringInvitationHandler atomically consumes an invitation and
// upserts the approved relationship for the redeemer.
func (a *API) RedeemMonitoringInvitationHandler(w http.ResponseWriter, r *http.Request) {
	memberID, ok := userIDFromContext(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req RedeemMonitoringInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		http.Error(w, "invalid invitation token", http.StatusBadRequest)
		return
	}

	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "failed to start redemption", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var invitation struct {
		ID                 string
		PatientID          string
		Relationship       string
		Kind               string
		CanMonitor         bool
		IsEmergencyContact bool
		ExpiresAt          time.Time
	}
	err = tx.QueryRowContext(r.Context(), `
		SELECT id, patient_id, relationship, kind, can_monitor,
		       is_emergency_contact, expires_at
		FROM patient_link_invitations
		WHERE token_hash = $1 AND redeemed_at IS NULL AND expires_at > CURRENT_TIMESTAMP
		FOR UPDATE`, hashInvitationToken(req.Token)).Scan(
		&invitation.ID, &invitation.PatientID, &invitation.Relationship,
		&invitation.Kind, &invitation.CanMonitor, &invitation.IsEmergencyContact,
		&invitation.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "invitation is invalid, expired, or already used", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "failed to read invitation", http.StatusInternalServerError)
		return
	}
	if !invitation.ExpiresAt.After(time.Now()) {
		http.Error(w, "invitation is invalid, expired, or already used", http.StatusBadRequest)
		return
	}
	if invitation.PatientID == memberID {
		http.Error(w, "cannot link to yourself", http.StatusBadRequest)
		return
	}

	relationshipID := uuid.New().String()
	err = tx.QueryRowContext(r.Context(), `
		UPDATE patient_relationships
		SET relationship = $3,
		    kind = $4,
		    can_monitor = $5,
		    is_emergency_contact = $6,
		    active = TRUE,
		    revoked_at = NULL
		WHERE patient_id = $1 AND member_user_id = $2
		RETURNING id::text`,
		invitation.PatientID, memberID, invitation.Relationship,
		invitation.Kind, invitation.CanMonitor, invitation.IsEmergencyContact).Scan(&relationshipID)
	if err == sql.ErrNoRows {
		relationshipID = uuid.New().String()
		err = tx.QueryRowContext(r.Context(), `
			INSERT INTO patient_relationships
				(id, patient_id, member_user_id, relationship, kind, can_monitor,
				 is_emergency_contact, active, revoked_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, TRUE, NULL)
			RETURNING id`,
			relationshipID, invitation.PatientID, memberID, invitation.Relationship,
			invitation.Kind, invitation.CanMonitor, invitation.IsEmergencyContact).Scan(&relationshipID)
	}
	if err != nil {
		log.Printf("Redeem error: %v", err)
		http.Error(w, "failed to create relationship: "+err.Error(), http.StatusInternalServerError)
		return
	}

	result, err := tx.ExecContext(r.Context(), `
		UPDATE patient_link_invitations
		SET redeemed_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND redeemed_at IS NULL`, invitation.ID)
	if err != nil {
		http.Error(w, "failed to consume invitation", http.StatusInternalServerError)
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		http.Error(w, "invitation is invalid, expired, or already used", http.StatusBadRequest)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "failed to commit relationship", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(MonitoringRelationshipResponse{
		ID: relationshipID, PatientID: invitation.PatientID, MemberUserID: memberID,
		Relationship: invitation.Relationship, Kind: invitation.Kind,
		CanMonitor: invitation.CanMonitor, IsEmergencyContact: invitation.IsEmergencyContact,
	})
}

// RevokeMonitoringRelationshipHandler is owned by the patient. Revocation is
// soft so the unique patient/member constraint can safely be reused by a later
// consent invitation.
func (a *API) RevokeMonitoringRelationshipHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := userIDFromContext(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	relationshipID := chi.URLParam(r, "relationshipId")
	if relationshipID == "" {
		http.Error(w, "missing relationshipId", http.StatusBadRequest)
		return
	}
	result, err := a.db.ExecContext(r.Context(), `
		UPDATE patient_relationships
		SET active = FALSE, revoked_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND patient_id = $2 AND active = TRUE`, relationshipID, patientID)
	if err != nil {
		http.Error(w, "failed to revoke relationship", http.StatusInternalServerError)
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		http.Error(w, "relationship not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) GetEmergencyContactsHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := userIDFromContext(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT id, display_name, phone, relationship, is_primary
		FROM emergency_contacts WHERE patient_id = $1 ORDER BY created_at`, patientID)
	if err != nil {
		http.Error(w, "failed to query emergency contacts", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	contacts := make([]EmergencyContactResponse, 0)
	for rows.Next() {
		var c EmergencyContactResponse
		if err := rows.Scan(&c.ID, &c.DisplayName, &c.Phone, &c.Relationship, &c.IsPrimary); err != nil {
			http.Error(w, "failed to scan emergency contacts", http.StatusInternalServerError)
			return
		}
		contacts = append(contacts, c)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read emergency contacts", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(contacts)
}

func (a *API) PostEmergencyContactHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := userIDFromContext(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req EmergencyContactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Phone = strings.TrimSpace(req.Phone)
	req.Relationship = strings.TrimSpace(req.Relationship)
	if req.DisplayName == "" || req.Phone == "" || req.Relationship == "" || len(req.DisplayName) > 200 || len(req.Phone) > 32 || len(req.Relationship) > 64 {
		http.Error(w, "displayName, phone, and relationship are required", http.StatusBadRequest)
		return
	}
	if !e164Regex.MatchString(req.Phone) {
		http.Error(w, "invalid phone number format, must be E.164", http.StatusBadRequest)
		return
	}
	isPrimary := false
	if req.IsPrimary != nil {
		isPrimary = *req.IsPrimary
	}
	id := uuid.New().String()
	_, err := a.db.ExecContext(r.Context(), `
		INSERT INTO emergency_contacts (id, patient_id, display_name, phone, relationship, is_primary)
		VALUES ($1, $2, $3, $4, $5, $6)`, id, patientID, req.DisplayName, req.Phone, req.Relationship, isPrimary)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "unique") {
			http.Error(w, "emergency contact already exists", http.StatusConflict)
			return
		}
		http.Error(w, "failed to create emergency contact", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(EmergencyContactResponse{ID: id, DisplayName: req.DisplayName, Phone: req.Phone, Relationship: req.Relationship, IsPrimary: isPrimary})
}

func (a *API) PatchEmergencyContactHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := userIDFromContext(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	contactID := chi.URLParam(r, "contactId")
	if contactID == "" {
		http.Error(w, "missing contactId", http.StatusBadRequest)
		return
	}
	var req EmergencyContactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	sets := make([]string, 0, 4)
	args := make([]interface{}, 0, 6)
	arg := 1
	if strings.TrimSpace(req.DisplayName) != "" {
		if len(strings.TrimSpace(req.DisplayName)) > 200 {
			http.Error(w, "displayName is too long", http.StatusBadRequest)
			return
		}
		sets = append(sets, fmt.Sprintf("display_name = $%d", arg))
		args = append(args, strings.TrimSpace(req.DisplayName))
		arg++
	}
	if strings.TrimSpace(req.Phone) != "" {
		phone := strings.TrimSpace(req.Phone)
		if len(phone) > 32 || !e164Regex.MatchString(phone) {
			http.Error(w, "invalid phone number format, must be E.164", http.StatusBadRequest)
			return
		}
		sets = append(sets, fmt.Sprintf("phone = $%d", arg))
		args = append(args, phone)
		arg++
	}
	if strings.TrimSpace(req.Relationship) != "" {
		if len(strings.TrimSpace(req.Relationship)) > 64 {
			http.Error(w, "relationship is too long", http.StatusBadRequest)
			return
		}
		sets = append(sets, fmt.Sprintf("relationship = $%d", arg))
		args = append(args, strings.TrimSpace(req.Relationship))
		arg++
	}
	if req.IsPrimary != nil {
		sets = append(sets, fmt.Sprintf("is_primary = $%d", arg))
		args = append(args, *req.IsPrimary)
		arg++
	}
	if len(sets) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	args = append(args, contactID, patientID)
	query := "UPDATE emergency_contacts SET " + strings.Join(sets, ", ") + fmt.Sprintf(" WHERE id = $%d AND patient_id = $%d", arg, arg+1)
	result, err := a.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "unique") {
			http.Error(w, "emergency contact already exists", http.StatusConflict)
			return
		}
		http.Error(w, "failed to update emergency contact", http.StatusInternalServerError)
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		http.Error(w, "emergency contact not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *API) DeleteEmergencyContactHandler(w http.ResponseWriter, r *http.Request) {
	patientID, ok := userIDFromContext(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	contactID := chi.URLParam(r, "contactId")
	if contactID == "" {
		http.Error(w, "missing contactId", http.StatusBadRequest)
		return
	}
	result, err := a.db.ExecContext(r.Context(), "DELETE FROM emergency_contacts WHERE id = $1 AND patient_id = $2", contactID, patientID)
	if err != nil {
		http.Error(w, "failed to delete emergency contact", http.StatusInternalServerError)
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		http.Error(w, "emergency contact not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hasMonitoringRelationship is the single authorization predicate for
// patient-object monitoring reads.
func (a *API) hasMonitoringRelationship(ctx context.Context, patientID, memberID string) (bool, error) {
	var allowed bool
	err := a.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM patient_relationships
			WHERE patient_id = $1 AND member_user_id = $2
			  AND can_monitor = TRUE AND active = TRUE AND revoked_at IS NULL
		)`, patientID, memberID).Scan(&allowed)
	return allowed, err
}
