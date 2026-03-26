package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"proxyllm/internal/config"
	"proxyllm/internal/domain"
	"proxyllm/internal/router"
	"proxyllm/internal/storage"
)

type mockStore struct {
	storage.Storage
	nodes map[string]*domain.ModelNode
	keys  map[string]*domain.APIKey
}

func (m *mockStore) GetNode(ctx context.Context, id string) (*domain.ModelNode, error) {
	return m.nodes[id], nil
}
func (m *mockStore) UpsertNode(ctx context.Context, n *domain.ModelNode) error {
	m.nodes[n.ID] = n
	return nil
}
func (m *mockStore) GetAPIKey(ctx context.Context, id string) (*domain.APIKey, error) {
	return m.keys[id], nil
}
func (m *mockStore) UpsertAPIKey(ctx context.Context, k *domain.APIKey) error {
	m.keys[k.ID] = k
	return nil
}
func (m *mockStore) ListNodes(ctx context.Context) ([]*domain.ModelNode, error) { return nil, nil }
func (m *mockStore) ListAPIKeys(ctx context.Context) ([]*domain.APIKey, error) { return nil, nil }
func (m *mockStore) GetAPIKeyByValue(ctx context.Context, v string) (*domain.APIKey, error) { return nil, nil }

func TestAdminHandler_NodeUpdateMerge(t *testing.T) {
	store := &mockStore{nodes: make(map[string]*domain.ModelNode), keys: make(map[string]*domain.APIKey)}
	existing := &domain.ModelNode{
		ID:       "node1",
		Name:     "Original Name",
		APIKey:   "secret-key",
		Enabled:  true,
	}
	store.nodes["node1"] = existing

	rt := router.New()
	tmpFile, _ := os.CreateTemp("", "config.toml")
	defer os.Remove(tmpFile.Name())
	cfgMgr := config.NewConfigManager(tmpFile.Name(), &config.Config{})

	h := NewAdminHandler(store, rt, nil, nil, cfgMgr)
	
	// Partial update: Clear APIKey
	updateReq := map[string]any{
		"name":     "New Name",
		"api_key":  "",
		"aliases":  []string{},
	}
	body, _ := json.Marshal(updateReq)
	req := httptest.NewRequest(http.MethodPut, "/admin/nodes/node1", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.nodeByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	updated := store.nodes["node1"]
	if updated.Name != "New Name" {
		t.Errorf("expected New Name, got %s", updated.Name)
	}
	if updated.APIKey != "" {
		t.Error("APIKey was not cleared!")
	}
	if len(updated.Aliases) != 0 {
		t.Error("Aliases were not cleared!")
	}
	if !updated.Enabled {
		t.Error("Enabled was lost in update!")
	}
}

func TestAdminHandler_KeyUpdateMerge(t *testing.T) {
	store := &mockStore{nodes: make(map[string]*domain.ModelNode), keys: make(map[string]*domain.APIKey)}
	existing := &domain.APIKey{
		ID:      "key1",
		Name:    "Old Key",
		Key:     "token123",
		Enabled: true,
	}
	store.keys["key1"] = existing

	tmpFile, _ := os.CreateTemp("", "config.toml")
	defer os.Remove(tmpFile.Name())
	cfgMgr := config.NewConfigManager(tmpFile.Name(), &config.Config{})

	h := NewAdminHandler(store, nil, nil, nil, cfgMgr)

	// Partial update: only Name
	updateReq := map[string]any{
		"name": "New Key Name",
	}
	body, _ := json.Marshal(updateReq)
	req := httptest.NewRequest(http.MethodPut, "/admin/keys/key1", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.keyByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	updated := store.keys["key1"]
	if updated.Name != "New Key Name" {
		t.Errorf("expected New Key Name, got %s", updated.Name)
	}
	if updated.Key != "token123" {
		t.Error("Key value was lost in update!")
	}
}
