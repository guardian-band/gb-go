package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata"
)

// PatientProfile represents the medical profile of a patient.
type PatientProfile struct {
	DisplayName   string          `json:"displayName,omitempty"`
	BirthDate     string          `json:"birthDate"`
	BloodType     string          `json:"bloodType"`
	CriticalFacts json.RawMessage `json:"criticalFacts"`
	Timezone      string          `json:"timezone"`
}

var validBloodTypes = map[string]bool{
	"A+":  true,
	"A-":  true,
	"B+":  true,
	"B-":  true,
	"AB+": true,
	"AB-": true,
	"O+":  true,
	"O-":  true,
}

// GetProfileHandler retrieves the profile for the authenticated user.
func (a *API) GetProfileHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var birthDate sql.NullTime
	var bloodType sql.NullString
	var criticalFacts []byte
	var timezone string

	err := a.db.QueryRowContext(r.Context(),
		"SELECT birth_date, blood_type, critical_facts, timezone FROM patient_profiles WHERE user_id = $1",
		userID).Scan(&birthDate, &bloodType, &criticalFacts, &timezone)

	if err == sql.ErrNoRows {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "failed to query profile: "+err.Error(), http.StatusInternalServerError)
		return
	}

	profile := PatientProfile{
		CriticalFacts: json.RawMessage(criticalFacts),
		Timezone:      timezone,
	}
	// Basic identity lives on users; failure to read the optional field must not
	// make an otherwise valid medical profile unavailable.
	var displayName sql.NullString
	if err := a.db.QueryRowContext(r.Context(), "SELECT display_name FROM users WHERE id = $1", userID).Scan(&displayName); err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "failed to query user identity: "+err.Error(), http.StatusInternalServerError)
		return
	} else if displayName.Valid {
		profile.DisplayName = displayName.String
	}

	if birthDate.Valid {
		profile.BirthDate = birthDate.Time.Format("2006-01-02")
	}
	if bloodType.Valid {
		profile.BloodType = bloodType.String
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(profile)
}

// PutProfileHandler creates or updates the profile for the authenticated user.
func (a *API) PutProfileHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req PatientProfile
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 1. BirthDate Validation
	t, err := time.Parse("2006-01-02", req.BirthDate)
	if err != nil {
		http.Error(w, "invalid birthDate format, must be YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	if t.After(time.Now()) {
		http.Error(w, "birthDate cannot be in the future", http.StatusBadRequest)
		return
	}

	// 2. BloodType Validation
	if !validBloodTypes[req.BloodType] {
		http.Error(w, "invalid bloodType", http.StatusBadRequest)
		return
	}

	// 3. Timezone Validation
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		http.Error(w, "invalid timezone, must be IANA format", http.StatusBadRequest)
		return
	}

	// 4. CriticalFacts Validation
	if len(req.CriticalFacts) == 0 {
		req.CriticalFacts = json.RawMessage("{}")
	} else if !json.Valid(req.CriticalFacts) {
		http.Error(w, "invalid criticalFacts JSON", http.StatusBadRequest)
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if len(req.DisplayName) > 200 {
		http.Error(w, "displayName is too long", http.StatusBadRequest)
		return
	}
	if req.DisplayName != "" {
		tx, err := a.db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "failed to start profile update: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		var exists int
		err = tx.QueryRowContext(r.Context(), "SELECT 1 FROM patient_profiles WHERE user_id = $1", userID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(r.Context(), `
				INSERT INTO patient_profiles (user_id, birth_date, blood_type, critical_facts, timezone)
				VALUES ($1, $2, $3, $4, $5)`,
				userID, t, req.BloodType, req.CriticalFacts, req.Timezone)
		} else if err == nil {
			_, err = tx.ExecContext(r.Context(), `
				UPDATE patient_profiles
				SET birth_date = $1, blood_type = $2, critical_facts = $3, timezone = $4
				WHERE user_id = $5`,
				t, req.BloodType, req.CriticalFacts, req.Timezone, userID)
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), "UPDATE users SET display_name = $1 WHERE id = $2", req.DisplayName, userID)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			http.Error(w, "failed to save profile: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(req)
		return
	}

	// Check if profile already exists to handle upsert compatibly in openGauss
	var exists int
	err = a.db.QueryRowContext(r.Context(), "SELECT 1 FROM patient_profiles WHERE user_id = $1", userID).Scan(&exists)

	if err == sql.ErrNoRows {
		// Insert new profile
		_, err = a.db.ExecContext(r.Context(), `
			INSERT INTO patient_profiles (user_id, birth_date, blood_type, critical_facts, timezone)
			VALUES ($1, $2, $3, $4, $5)`,
			userID, t, req.BloodType, req.CriticalFacts, req.Timezone)
	} else if err == nil {
		// Update existing profile
		_, err = a.db.ExecContext(r.Context(), `
			UPDATE patient_profiles
			SET birth_date = $1, blood_type = $2, critical_facts = $3, timezone = $4
			WHERE user_id = $5`,
			t, req.BloodType, req.CriticalFacts, req.Timezone, userID)
	}

	if err != nil {
		http.Error(w, "failed to save profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(req)
}

// UserAccount represents a user's role and role-specific metadata facts.
type UserAccount struct {
	Role      string          `json:"role"`
	RoleFacts json.RawMessage `json:"roleFacts"`
}

// GetAccountHandler retrieves the user account role and roleFacts.
func (a *API) GetAccountHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var role string
	var roleFacts []byte

	err := a.db.QueryRowContext(r.Context(),
		"SELECT role, role_facts FROM user_accounts WHERE user_id = $1",
		userID).Scan(&role, &roleFacts)

	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "user account not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "failed to query user account: "+err.Error(), http.StatusInternalServerError)
		return
	}

	account := UserAccount{
		Role:      role,
		RoleFacts: json.RawMessage(roleFacts),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(account)
}

// PutAccountHandler creates or updates the user account role and roleFacts.
func (a *API) PutAccountHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req UserAccount
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Role != "patient" && req.Role != "family" && req.Role != "doctor" {
		http.Error(w, "invalid role. Must be patient, family, or doctor", http.StatusBadRequest)
		return
	}

	if len(req.RoleFacts) == 0 {
		req.RoleFacts = json.RawMessage("{}")
	}

	var exists int
	err := a.db.QueryRowContext(r.Context(), "SELECT 1 FROM user_accounts WHERE user_id = $1", userID).Scan(&exists)

	if errors.Is(err, sql.ErrNoRows) {
		_, err = a.db.ExecContext(r.Context(), `
			INSERT INTO user_accounts (user_id, role, role_facts, updated_at)
			VALUES ($1, $2, $3, $4)`,
			userID, req.Role, req.RoleFacts, time.Now())
	} else if err == nil {
		_, err = a.db.ExecContext(r.Context(), `
			UPDATE user_accounts
			SET role = $1, role_facts = $2, updated_at = $3
			WHERE user_id = $4`,
			req.Role, req.RoleFacts, time.Now(), userID)
	}

	if err != nil {
		http.Error(w, "failed to save user account: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(req)
}
