package sqlite

import (
	"context"
	"os"
	"testing"

	"proxyllm/internal/domain"
)

func TestSQLiteStorage_GetAPIKey(t *testing.T) {
	tmpFile := "test_key.db"
	defer os.Remove(tmpFile)

	db, err := Open(tmpFile)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	s := NewSQLiteStorage(db)
	ctx := context.Background()

	key := &domain.APIKey{
		ID:   "key-id-1",
		Key:  "secret-value",
		Name: "Test Key",
	}

	if err := s.UpsertAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}

	// Test GetAPIKey (by ID)
	fetched, err := s.GetAPIKey(ctx, "key-id-1")
	if err != nil {
		t.Fatal(err)
	}
	if fetched == nil || fetched.Key != "secret-value" {
		t.Errorf("GetAPIKey failed")
	}

	// Test GetAPIKeyByValue
	fetched, err = s.GetAPIKeyByValue(ctx, "secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if fetched == nil || fetched.ID != "key-id-1" {
		t.Errorf("GetAPIKeyByValue failed")
	}
}
