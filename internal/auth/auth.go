package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"proxyllm/internal/domain"
	"proxyllm/internal/storage"
)

// ErrUnauthorized is returned when a token is not found or the key is disabled.
var ErrUnauthorized = errors.New("unauthorized")

// ErrExpired is returned when a token's ExpiresAt is in the past.
var ErrExpired = errors.New("api key expired")

// Authenticator validates incoming API keys against the backing store.
type Authenticator struct {
	store storage.Storage
}

// NewAuthenticator creates a new Authenticator backed by the given Storage.
func NewAuthenticator(store storage.Storage) *Authenticator {
	return &Authenticator{store: store}
}

// ExtractToken extracts the Bearer token from the Authorization header.
// Returns ("", false) if the header is missing or not a valid Bearer scheme.
func ExtractToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(header, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}

// Authenticate looks up the token in storage and returns the associated APIKey
// if it exists and is enabled. Returns ErrUnauthorized if the key is not found
// or disabled, and ErrExpired if the key's ExpiresAt is set and in the past.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (*domain.APIKey, error) {
	key, err := a.store.GetAPIKeyByValue(ctx, token)
	if err != nil {
		return nil, ErrUnauthorized
	}
	if key == nil || !key.Enabled {
		return nil, ErrUnauthorized
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		return nil, ErrExpired
	}
	return key, nil
}

// CheckModelAllowed reports whether key permits access to the given model alias.
// An empty AllowModels list means all models are allowed.
func CheckModelAllowed(key *domain.APIKey, modelAlias string) bool {
	if len(key.AllowModels) == 0 {
		return true
	}
	for _, allowed := range key.AllowModels {
		if allowed == modelAlias {
			return true
		}
	}
	return false
}
