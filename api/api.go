package api

import (
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// API contains the HTTP handlers and in-memory store for the application.
type API struct {
	users map[string]string // username -> bcrypt hashed password
	mu    sync.RWMutex
}

// New creates an API server with the application's handlers.
func New() *API {
	return &API{
		users: make(map[string]string),
	}
}

// Router builds the application's HTTP router.
func (a *API) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Route("/api", func(r chi.Router) {
		r.Post("/register", a.Register)
		r.Post("/login", a.Login)
	})

	return r
}