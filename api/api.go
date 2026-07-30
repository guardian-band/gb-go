package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"
)

// API contains the HTTP handlers and application dependencies.
type API struct {
	users   map[string]string // username -> bcrypt hashed password
	storage *Storage
	db      *sql.DB
	redis   *redis.Client
	mu      sync.RWMutex
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

	return &API{
		users:   make(map[string]string),
		storage: storage,
		db:      db,
		redis:   redisClient,
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
			r.Post("/upload", a.UploadHandler)
			r.Get("/protected", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("Access granted to protected endpoint!"))
			})
		})
	})

	return r
}
