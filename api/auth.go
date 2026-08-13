package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// getJWTSecret returns the JWT signing key from environment variables or a fallback secure key.
func getJWTSecret() []byte {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return []byte("super-secret-key-guardianband")
	}
	return []byte(secret)
}

const tokenDuration = 24 * time.Hour

var e164Regex = regexp.MustCompile(`^\+[1-9]\d{1,14}$`)

type AuthRequest struct {
	PhoneNumber string `json:"phoneNumber"`
	Password    string `json:"password"`
}

type AuthResponse struct {
	Token string `json:"token"`
}

type RegisterRequest struct {
	PhoneNumber string `json:"phoneNumber"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName,omitempty"`
}

type RegisterResponse struct {
	ID          string `json:"id"`
	PhoneNumber string `json:"phoneNumber"`
	DisplayName string `json:"displayName,omitempty"`
}

// GenerateToken creates a signed JWT containing the user's UUID in the sub claim.
func GenerateToken(userUUID string) (string, error) {
	claims := jwt.MapClaims{
		"sub": userUUID,
		"exp": time.Now().Add(tokenDuration).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(getJWTSecret())
}

// AuthenticateMiddleware protects routes by validating the Bearer JWT token and injecting user ID into context.
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
			return getJWTSecret(), nil
		})

		if err != nil || !token.Valid {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Error(w, "invalid token claims", http.StatusUnauthorized)
			return
		}

		userID, ok := claims["sub"].(string)
		if !ok || userID == "" {
			http.Error(w, "invalid token subject", http.StatusUnauthorized)
			return
		}

		// Inject user UUID into request context
		ctx := context.WithValue(r.Context(), UserContextKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
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
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if len(req.DisplayName) > 200 {
		http.Error(w, "displayName is too long", http.StatusBadRequest)
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
	var insertQuery string
	var insertArgs []interface{}
	if req.DisplayName == "" {
		// Keep the compact form for clients that do not provide basic identity.
		insertQuery = "INSERT INTO users (id, phone_number, password_hash, created_at) VALUES ($1, $2, $3, $4)"
		insertArgs = []interface{}{userID, req.PhoneNumber, hash, time.Now()}
	} else {
		insertQuery = "INSERT INTO users (id, phone_number, password_hash, created_at, display_name) VALUES ($1, $2, $3, $4, $5)"
		insertArgs = []interface{}{userID, req.PhoneNumber, hash, time.Now(), req.DisplayName}
	}
	_, err = a.db.ExecContext(r.Context(), insertQuery, insertArgs...)
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
		DisplayName: req.DisplayName,
	})
}

// Login handles user login and returns JWT.
func (a *API) Login(w http.ResponseWriter, r *http.Request) {
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.PhoneNumber == "" || req.Password == "" {
		http.Error(w, "missing required fields", http.StatusBadRequest)
		return
	}

	var userID string
	var passwordHash string

	// Query database for user credentials
	err := a.db.QueryRowContext(r.Context(),
		"SELECT id, password_hash FROM users WHERE phone_number = $1",
		req.PhoneNumber).Scan(&userID, &passwordHash)
	if err == sql.ErrNoRows {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	} else if err != nil {
		http.Error(w, "database query failure: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Compare password hash
	if !CheckPasswordHash(req.Password, passwordHash) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	// Generate Token containing user's UUID in "sub"
	token, err := GenerateToken(userID)
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
