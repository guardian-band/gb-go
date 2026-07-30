package api

import (
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// API contains the HTTP handlers and in-memory store for the application.
type API struct {
	users   map[string]string // username -> bcrypt hashed password
	storage *Storage
	mu      sync.RWMutex
}

// New creates an API server with the application's handlers.
func New() *API {
	storage, err := NewStorage()
	if err != nil {
		// Log warning if MinIO is not available yet at startup, so application still runs
		println("MinIO Storage warning:", err.Error())
	}

	return &API{
		users:   make(map[string]string),
		storage: storage,
	}
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