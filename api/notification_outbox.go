package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// NotificationProvider defines the contract for sending single alert messages.
type NotificationProvider interface {
	Send(ctx context.Context, channel string, address string, payload map[string]interface{}) error
}

// ConsoleNotificationProvider outputs masked notification requests to stdout.
type ConsoleNotificationProvider struct{}

func (c *ConsoleNotificationProvider) Send(ctx context.Context, channel string, address string, payload map[string]interface{}) error {
	masked := maskAddress(address)
	log.Printf(`{"event":"notification_sent","channel":"%s","address":"%s","payload":%+v}`+"\n", channel, masked, payload)
	return nil
}

func maskAddress(address string) string {
	if len(address) <= 8 {
		return "****"
	}
	return address[:4] + "****" + address[len(address)-4:]
}

// TerminalError represents a delivery error that cannot be resolved with retries.
type TerminalError struct {
	Err error
}

func (e *TerminalError) Error() string {
	return e.Err.Error()
}

func IsTerminalError(err error) bool {
	_, ok := err.(*TerminalError)
	return ok
}

// PutNotificationEndpointHandler registers or updates a device token under the current user.
func (a *API) PutNotificationEndpointHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	endpointID := chi.URLParam(r, "endpointId")
	if _, err := uuid.Parse(endpointID); err != nil {
		http.Error(w, "invalid endpointId parameter", http.StatusBadRequest)
		return
	}

	var req struct {
		Channel string `json:"channel"`
		Token   string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Channel == "" || req.Token == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate channel type
	if req.Channel != "push" && req.Channel != "sms" {
		http.Error(w, "invalid channel, must be push or sms", http.StatusBadRequest)
		return
	}

	// Verify ownership if endpoint already exists
	var ownerID string
	err := a.db.QueryRowContext(r.Context(), "SELECT user_id FROM notification_endpoints WHERE id = $1", endpointID).Scan(&ownerID)
	if err == nil {
		if ownerID != userID {
			http.Error(w, "forbidden: ownership mismatch", http.StatusForbidden)
			return
		}
	} else if err != sql.ErrNoRows {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var count int
	err = a.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM notification_endpoints WHERE id = $1", endpointID).Scan(&count)
	if err != nil {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if count > 0 {
		_, err = a.db.ExecContext(r.Context(), `
			UPDATE notification_endpoints
			SET channel = $1, token = $2, active = true, updated_at = NOW()
			WHERE id = $3`,
			req.Channel, req.Token, endpointID)
	} else {
		_, err = a.db.ExecContext(r.Context(), `
			INSERT INTO notification_endpoints (id, user_id, channel, token, active, updated_at)
			VALUES ($1, $2, $3, $4, true, NOW())`,
			endpointID, userID, req.Channel, req.Token)
	}
	if err != nil {
		http.Error(w, "failed to save notification endpoint: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        endpointID,
		"channel":   req.Channel,
		"token":     maskAddress(req.Token),
		"active":    true,
		"updatedAt": time.Now(),
	})
}

// DeleteNotificationEndpointHandler removes a notification endpoint registration.
func (a *API) DeleteNotificationEndpointHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	endpointID := chi.URLParam(r, "endpointId")
	if _, err := uuid.Parse(endpointID); err != nil {
		http.Error(w, "invalid endpointId parameter", http.StatusBadRequest)
		return
	}

	var ownerID string
	err := a.db.QueryRowContext(r.Context(), "SELECT user_id FROM notification_endpoints WHERE id = $1", endpointID).Scan(&ownerID)
	if err == sql.ErrNoRows {
		http.Error(w, "notification endpoint not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "database query error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if ownerID != userID {
		http.Error(w, "forbidden: ownership mismatch", http.StatusForbidden)
		return
	}

	_, err = a.db.ExecContext(r.Context(), "DELETE FROM notification_endpoints WHERE id = $1", endpointID)
	if err != nil {
		http.Error(w, "failed to delete notification endpoint: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RunNotificationWorkerOnce queries and attempts to process up to 10 pending notification outbox records.
func (a *API) RunNotificationWorkerOnce(ctx context.Context) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, event_type, incident_id, recipient_id, recipient_address, channel, payload, attempt_count
		FROM notification_outbox
		WHERE status IN ('pending', 'failed') AND (next_attempt IS NULL OR next_attempt <= NOW())
		ORDER BY created_at ASC
		LIMIT 10
		FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type outboxEntry struct {
		id               string
		eventType        string
		incidentID       string
		recipientID      sql.NullString
		recipientAddress string
		channel          string
		payload          []byte
		attemptCount     int
	}
	var entries []outboxEntry
	for rows.Next() {
		var e outboxEntry
		if err := rows.Scan(&e.id, &e.eventType, &e.incidentID, &e.recipientID, &e.recipientAddress, &e.channel, &e.payload, &e.attemptCount); err == nil {
			entries = append(entries, e)
		}
	}
	rows.Close() // close rows so tx can execute update queries

	for _, e := range entries {
		var payloadMap map[string]interface{}
		_ = json.Unmarshal(e.payload, &payloadMap)

		sendErr := a.notificationProvider.Send(ctx, e.channel, e.recipientAddress, payloadMap)
		if sendErr == nil {
			_, _ = tx.ExecContext(ctx, `
				UPDATE notification_outbox
				SET status = 'sent', sent_at = NOW(), attempt_count = attempt_count + 1
				WHERE id = $1`, e.id)
		} else if IsTerminalError(sendErr) {
			_, _ = tx.ExecContext(ctx, `
				UPDATE notification_outbox
				SET status = 'terminal_failed', attempt_count = attempt_count + 1
				WHERE id = $1`, e.id)
			// Deactivate the failing endpoint registration
			if e.channel == "push" {
				_, _ = tx.ExecContext(ctx, `
					UPDATE notification_endpoints
					SET active = false, updated_at = NOW()
					WHERE token = $1`, e.recipientAddress)
			}
		} else {
			attempts := e.attemptCount + 1
			if attempts >= 5 {
				_, _ = tx.ExecContext(ctx, `
					UPDATE notification_outbox
					SET status = 'terminal_failed', attempt_count = $2
					WHERE id = $1`, e.id, attempts)
			} else {
				backoffSeconds := 15 * (1 << attempts)
				nextAttempt := time.Now().Add(time.Duration(backoffSeconds) * time.Second)
				_, _ = tx.ExecContext(ctx, `
					UPDATE notification_outbox
					SET status = 'failed', attempt_count = $2, next_attempt = $3
					WHERE id = $1`, e.id, attempts, nextAttempt)
			}
		}
	}

	return tx.Commit()
}
