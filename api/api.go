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
	storage             *Storage
	db                  *sql.DB
	redis               *redis.Client
	notificationService NotificationService
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

	return &API{
		storage:             storage,
		db:                  db,
		redis:               redisClient,
		notificationService: &ConsoleNotificationService{},
	}, nil
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
			r.Get("/documents", a.GetDocumentsHandler)
			r.Get("/documents/{documentId}", a.GetDocumentDetailHandler)
			r.Get("/medication-catalog", a.GetMedicationCatalogHandler)
			r.Get("/medications", a.GetMedicationsHandler)
			r.Post("/medications", a.PostMedicationHandler)
			r.Post("/medications/{medicationId}/taken", a.PostMedicationTakenHandler)
			r.Post("/sos/incidents", a.PostSOSIncidentHandler)
			r.Post("/sos/incidents/{incidentId}/cancel", a.PostSOSCancelHandler)
			r.Post("/vitals", a.VitalsPostHandler)
			
			// Patient Links
			r.Get("/patient-links", a.GetPatientLinksHandler)
			r.Post("/patient-links", a.PostPatientLinkHandler)
			r.Patch("/patient-links/{linkId}", a.PatchPatientLinkHandler)
			r.Delete("/patient-links/{linkId}", a.DeletePatientLinkHandler)
			r.Get("/protected", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("Access granted to protected endpoint!"))
			})
		})
	})

	return r
}
