package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"
)

type contextKey string

const UserContextKey contextKey = "userId"

// EmergencyContact metadata for notifications.
type EmergencyContact struct {
	DisplayName string
	Phone       string
}

// NotificationService defines alert contract.
type NotificationService interface {
	SendSOSAlert(patientID string, contacts []EmergencyContact, incidentID string) error
	SendAllClearAlert(patientID string, contacts []EmergencyContact, incidentID string) error
}

// ConsoleNotificationService prints warnings to std output.
type ConsoleNotificationService struct{}

func (c *ConsoleNotificationService) SendSOSAlert(patientID string, contacts []EmergencyContact, incidentID string) error {
	println("ConsoleNotification: SOS alert sent to emergency contacts for patient:", patientID)
	return nil
}

func (c *ConsoleNotificationService) SendAllClearAlert(patientID string, contacts []EmergencyContact, incidentID string) error {
	println("ConsoleNotification: All clear alert sent to emergency contacts for patient:", patientID)
	return nil
}

// API contains the HTTP handlers and application dependencies.
type API struct {
	storage              *Storage
	db                   *sql.DB
	redis                *redis.Client
	notificationService  NotificationService
	notificationProvider NotificationProvider
}

// New creates an API server with the application's handlers.
func New(ctx context.Context) (*API, error) {
	db, err := createConnection()
	if err != nil {
		return nil, fmt.Errorf("create database connection: %w", err)
	}
	if err := initializeDatabase(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	redisClient, err := createRedisClient()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create Redis client: %w", err)
	}
	if err := initializeRedis(ctx, redisClient); err != nil {
		redisClient.Close()
		db.Close()
		return nil, err
	}

	storage, err := NewStorage()
	if err != nil {
		// Log warning if MinIO is not available yet at startup, so application still runs
		println("MinIO Storage warning:", err.Error())
	}

	apiServer := &API{
		storage:              storage,
		db:                   db,
		redis:                redisClient,
		notificationService:  &ConsoleNotificationService{},
		notificationProvider: NewNotificationProvider(),
	}

	// Start background telemetry archiver worker
	archiver := NewTelemetryArchiver(db, redisClient, storage)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			<-ticker.C
			_ = archiver.RunOnce(context.Background())
		}
	}()

	// Start background outbox notification worker
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			<-ticker.C
			_ = apiServer.RunNotificationWorkerOnce(context.Background())
		}
	}()

	return apiServer, nil
}

// Close releases the API's database and Redis connections.
func (a *API) Close() error {
	var firstErr error
	if a.redis != nil {
		if err := a.redis.Close(); err != nil {
			firstErr = fmt.Errorf("close Redis client: %w", err)
		}
	}
	if a.db != nil {
		if err := a.db.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close database: %w", err)
		}
	}
	return firstErr
}

// Router builds the application's HTTP router.
func (a *API) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Route("/api", func(r chi.Router) {
		r.Post("/register", a.Register)
		r.Post("/login", a.Login)
		// Protected routes requiring a valid JWT token
		r.Group(func(r chi.Router) {
			r.Use(AuthenticateMiddleware)
			r.Get("/vitals/{userId}/latest", a.VitalsGetLatestHandler)

			r.Post("/upload", a.UploadHandler)
			r.Get("/profile", a.GetProfileHandler)
			r.Put("/profile", a.PutProfileHandler)
			r.Get("/account", a.GetAccountHandler)
			r.Put("/account", a.PutAccountHandler)
			r.Get("/documents", a.GetDocumentsHandler)
			r.Post("/documents", a.PostDocumentHandler)
			r.Get("/documents/{documentId}", a.GetDocumentDetailHandler)
			r.Put("/documents/{documentId}/content", a.PutDocumentContentHandler)
			r.Get("/documents/{documentId}/content", a.GetDocumentContentHandler)
			r.Get("/medication-catalog", a.GetMedicationCatalogHandler)
			r.Get("/medications", a.GetMedicationsHandler)
			r.Post("/medications", a.PostMedicationHandler)
			r.Post("/medications/{medicationId}/taken", a.PostMedicationTakenHandler)
			r.Post("/sos/incidents", a.PostSOSIncidentHandler)
			r.Post("/sos/incidents/{incidentId}/all-clear", a.PostSOSAllClearHandler)
			r.Post("/vitals", a.VitalsPostHandler)

			// Notification Endpoints
			r.Put("/notification-endpoints/{endpointId}", a.PutNotificationEndpointHandler)
			r.Delete("/notification-endpoints/{endpointId}", a.DeleteNotificationEndpointHandler)

			// Emergency Access
			r.Post("/emergency-access/tokens", a.PostEmergencyAccessTokenHandler)
			r.Post("/emergency-access/redeem", a.PostEmergencyAccessRedeemHandler)
			r.Get("/emergency-access/sessions/{sessionId}/medical-card", a.GetEmergencyAccessMedicalCardHandler)
			r.Delete("/emergency-access/sessions/{sessionId}", a.DeleteEmergencyAccessSessionHandler)

			// Patient-approved monitoring relationships and invitations.
			r.Get("/monitoring/patients", a.GetMonitoringPatientsHandler)
			r.Get("/patient-relationships", a.GetPatientRelationshipsHandler)
			r.Post("/patient-link-invitations", a.CreateMonitoringInvitationHandler)
			r.Post("/patient-link-invitations/redeem", a.RedeemMonitoringInvitationHandler)
			r.Delete("/patient-relationships/{relationshipId}", a.RevokeMonitoringRelationshipHandler)

			// External emergency contacts are deliberately separate from registered
			// monitoring relationships and never grant patient-data access.
			r.Get("/emergency-contacts", a.GetEmergencyContactsHandler)
			r.Post("/emergency-contacts", a.PostEmergencyContactHandler)
			r.Patch("/emergency-contacts/{contactId}", a.PatchEmergencyContactHandler)
			r.Delete("/emergency-contacts/{contactId}", a.DeleteEmergencyContactHandler)
			r.Get("/protected", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("Access granted to protected endpoint!"))
			})
		})
	})

	return r
}
