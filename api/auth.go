package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var jwtSecret = []byte("super-secret-key-guardianband")

const tokenDuration = 24 * time.Hour

var e164Regex = regexp.MustCompile(`^\+[1-9]\d{1,14}$`)

type AuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type AuthResponse struct {
	Token string `json:"token"`
}

type RegisterRequest struct {
	PhoneNumber string `json:"phoneNumber"`
	Password    string `json:"password"`
}

type RegisterResponse struct {
	ID          string `json:"id"`
	PhoneNumber string `json:"phoneNumber"`
}

// GenerateToken creates a signed JWT for the given username.
func GenerateToken(username string) (string, error) {
	claims := jwt.MapClaims{
		"username": username,
		"exp":      time.Now().Add(tokenDuration).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// AuthenticateMiddleware protects routes by validating the Bearer JWT token.
func AuthenticateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenStr == authHeader {
			http.Error(w, "invalid token format", http.StatusUnauthorized)
			return
		}

		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return jwtSecret, nil
		})

		if err != nil || !token.Valid {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Register handles user registration using database.
func (a *API) Register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.PhoneNumber == "" || req.Password == "" {
		http.Error(w, "missing required fields", http.StatusBadRequest)
		return
	}

	// Validate E.164 phone format
	if !e164Regex.MatchString(req.PhoneNumber) {
		http.Error(w, "invalid phone number format, must be E.164", http.StatusBadRequest)
		return
	}

	// Validate password length
	if len(req.Password) < 8 {
		http.Error(w, "password must be at least 8 characters long", http.StatusBadRequest)
		return
	}

	// Check if the user already exists in database
	var existingID string
	err := a.db.QueryRowContext(r.Context(), "SELECT id FROM users WHERE phone_number = $1", req.PhoneNumber).Scan(&existingID)
	if err == nil {
		http.Error(w, "user already exists", http.StatusConflict)
		return
	}

	// Hash password
	hash, err := HashPassword(req.Password)
	if err != nil {
		http.Error(w, "failed to hash password", http.StatusInternalServerError)
		return
	}

	// Generate new UUID
	userID := uuid.New().String()

	// Insert user record into the database
	_, err = a.db.ExecContext(r.Context(), 
		"INSERT INTO users (id, phone_number, password_hash, created_at) VALUES ($1, $2, $3, $4)",
		userID, req.PhoneNumber, hash, time.Now())
	if err != nil {
		// Log or check duplicate key error code just in case of race conditions
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			http.Error(w, "user already exists", http.StatusConflict)
			return
		}
		http.Error(w, "failed to create user in database: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(RegisterResponse{
		ID:          userID,
		PhoneNumber: req.PhoneNumber,
	})
}

// Login handles user login and returns JWT.
func (a *API) Login(w http.ResponseWriter, r *http.Request) {
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	a.mu.RLock()
	hash, exists := a.users[req.Username]
	a.mu.RUnlock()

	if !exists || !CheckPasswordHash(req.Password, hash) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	token, err := GenerateToken(req.Username)
	if err != nil {
		http.Error(w, "error generating token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AuthResponse{Token: token})
}

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}