package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// SOSIncidentRequest represents the payload for creating a new SOS incident.
type SOSIncidentRequest struct {
	Latitude       *float64 `json:"latitude"`
	Longitude      *float64 `json:"longitude"`
	AccuracyMeters *float64 `json:"accuracyMeters"`
	Address        *string  `json:"address"`
}

// SOSIncidentResponse represents the response when an SOS incident is successfully created.
type SOSIncidentResponse struct {
	ID             string    `json:"id"`
	PatientID      string    `json:"patientId"`
	Status         string    `json:"status"`
	Latitude       *float64  `json:"latitude"`
	Longitude      *float64  `json:"longitude"`
	AccuracyMeters *float64  `json:"accuracyMeters"`
	Address        *string   `json:"address"`
	StartedAt      time.Time `json:"startedAt"`
}

// createOutboxEntries creates outbox records for a given patient's emergency contacts inside a transaction.
func createOutboxEntries(ctx context.Context, tx *sql.Tx, patientID string, incidentID string, eventType string, message string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT display_name, phone, NULL::uuid AS linked_user_id
		FROM emergency_contacts
		WHERE patient_id = $1
		UNION ALL
		SELECT COALESCE(u.display_name, ''), u.phone_number, pr.member_user_id
		FROM patient_relationships pr
		JOIN users u ON u.id = pr.member_user_id
		WHERE pr.patient_id = $1
		  AND pr.is_emergency_contact = TRUE
		  AND pr.active = TRUE
		  AND pr.revoked_at IS NULL`, patientID)
	if err != nil {
		return fmt.Errorf("query emergency contacts: %w", err)
	}
	defer rows.Close()

	type contact struct {
		phone        string
		linkedUserID sql.NullString
	}
	var contacts []contact
	for rows.Next() {
		var c contact
		var displayName string
		if err := rows.Scan(&displayName, &c.phone, &c.linkedUserID); err == nil {
			contacts = append(contacts, c)
		}
	}

	payload := map[string]interface{}{
		"incidentId": incidentID,
		"message":    message,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	for _, c := range contacts {
		var endpoints []struct {
			channel string
			token   string
		}

		if c.linkedUserID.Valid && c.linkedUserID.String != "" {
			erows, err := tx.QueryContext(ctx, `
				SELECT channel, token FROM notification_endpoints
				WHERE user_id = $1 AND active = true`,
				c.linkedUserID.String)
			if err == nil {
				for erows.Next() {
					var ep struct {
						channel string
						token   string
					}
					if err := erows.Scan(&ep.channel, &ep.token); err == nil {
						endpoints = append(endpoints, ep)
					}
				}
				erows.Close()
			}
		}

		if len(endpoints) > 0 {
			// Write outbox entry for each registered endpoint
			for _, ep := range endpoints {
				idempotencyKey := fmt.Sprintf("%s:%s:%s", incidentID, eventType, ep.token)

				var exists bool
				err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM notification_outbox WHERE idempotency_key = $1)", idempotencyKey).Scan(&exists)
				if err != nil {
					return fmt.Errorf("check outbox idempotency: %w", err)
				}
				if exists {
					continue
				}

				outboxID := uuid.New().String()
				_, err = tx.ExecContext(ctx, `
					INSERT INTO notification_outbox (id, event_type, incident_id, recipient_id, recipient_address, channel, payload, status, idempotency_key)
					VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8)`,
					outboxID, eventType, incidentID, c.linkedUserID, ep.token, ep.channel, payloadBytes, idempotencyKey)
				if err != nil {
					return fmt.Errorf("insert push outbox: %w", err)
				}
			}
		} else if c.phone != "" {
			// Fallback: Write SMS outbox entry
			idempotencyKey := fmt.Sprintf("%s:%s:%s", incidentID, eventType, c.phone)

			var exists bool
			err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM notification_outbox WHERE idempotency_key = $1)", idempotencyKey).Scan(&exists)
			if err != nil {
				return fmt.Errorf("check outbox idempotency fallback: %w", err)
			}
			if exists {
				continue
			}

			outboxID := uuid.New().String()
			var recipientID interface{} = nil
			if c.linkedUserID.Valid && c.linkedUserID.String != "" {
				recipientID = c.linkedUserID.String
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO notification_outbox (id, event_type, incident_id, recipient_id, recipient_address, channel, payload, status, idempotency_key)
				VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8)`,
				outboxID, eventType, incidentID, recipientID, c.phone, "sms", payloadBytes, idempotencyKey)
			if err != nil {
				return fmt.Errorf("insert sms outbox: %w", err)
			}
		}
	}

	return nil
}

// PostSOSIncidentHandler triggers a new SOS incident and alerts emergency contacts via transaction outbox.
func (a *API) PostSOSIncidentHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req SOSIncidentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 1. Validations
	if req.Latitude != nil {
		if *req.Latitude < -90 || *req.Latitude > 90 {
			http.Error(w, "invalid latitude, must be between -90 and 90", http.StatusBadRequest)
			return
		}
	}

	if req.Longitude != nil {
		if *req.Longitude < -180 || *req.Longitude > 180 {
			http.Error(w, "invalid longitude, must be between -180 and 180", http.StatusBadRequest)
			return
		}
	}

	if req.AccuracyMeters != nil {
		if *req.AccuracyMeters < 0 {
			http.Error(w, "invalid accuracyMeters, cannot be negative", http.StatusBadRequest)
			return
		}
	}

	incidentID := uuid.New().String()
	startedAt := time.Now()

	// Validate patient profile exists
	var exists int
	err := a.db.QueryRowContext(r.Context(), "SELECT 1 FROM patient_profiles WHERE user_id = $1", userID).Scan(&exists)
	if err == sql.ErrNoRows {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": "patient profile not found"})
		return
	} else if err != nil {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "failed to start transaction: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Insert active SOS incident into database
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO sos_incidents (id, patient_id, status, latitude, longitude, accuracy_meters, address, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		incidentID, userID, "active", req.Latitude, req.Longitude, req.AccuracyMeters, req.Address, startedAt)

	if err != nil {
		http.Error(w, "failed to create SOS incident: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create Outbox entries in transaction
	err = createOutboxEntries(r.Context(), tx, userID, incidentID, "sos", "Emergency SOS alert: Patient needs assistance.")
	if err != nil {
		http.Error(w, "failed to record notifications: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "failed to commit transaction: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SOSIncidentResponse{
		ID:             incidentID,
		PatientID:      userID,
		Status:         "active",
		Latitude:       req.Latitude,
		Longitude:      req.Longitude,
		AccuracyMeters: req.AccuracyMeters,
		Address:        req.Address,
		StartedAt:      startedAt,
	})
}

// SOSAllClearResponse represents the response when an SOS incident all-clear is processed.
type SOSAllClearResponse struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	AllClearAt *time.Time `json:"allClearAt"`
}

// PostSOSAllClearHandler handles "all clear" signal, updates all_clear_at, and writes "all clear" outbox notifications.
func (a *API) PostSOSAllClearHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	incidentID := chi.URLParam(r, "incidentId")
	if incidentID == "" {
		http.Error(w, "missing incidentId parameter", http.StatusBadRequest)
		return
	}

	var patientID string
	var status string
	var allClearAtNull sql.NullTime

	err := a.db.QueryRowContext(r.Context(),
		"SELECT patient_id, status, all_clear_at FROM sos_incidents WHERE id = $1",
		incidentID).Scan(&patientID, &status, &allClearAtNull)

	if err == sql.ErrNoRows {
		http.Error(w, "SOS incident not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 403 Forbidden check: Validate SOS incident ownership
	if patientID != userID {
		http.Error(w, "forbidden: access to this SOS incident is denied", http.StatusForbidden)
		return
	}

	// Idempotency: check if all-clear is already set
	if allClearAtNull.Valid {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SOSAllClearResponse{
			ID:         incidentID,
			Status:     status,
			AllClearAt: &allClearAtNull.Time,
		})
		return
	}

	now := time.Now()

	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "failed to start transaction: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Update SOS incident all_clear_at in database (keeping the status intact)
	_, err = tx.ExecContext(r.Context(),
		"UPDATE sos_incidents SET all_clear_at = $2 WHERE id = $1",
		incidentID, now)
	if err != nil {
		http.Error(w, "failed to update SOS incident: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create Outbox entries in transaction
	err = createOutboxEntries(r.Context(), tx, userID, incidentID, "all_clear", "I'm good, everything is fine.")
	if err != nil {
		http.Error(w, "failed to record all-clear notifications: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "failed to commit transaction: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SOSAllClearResponse{
		ID:         incidentID,
		Status:     status,
		AllClearAt: &now,
	})
}
