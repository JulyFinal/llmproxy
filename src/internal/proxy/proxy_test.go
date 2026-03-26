package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyllm/internal/domain"
)

func TestProxy_Forward_ErrorAttribution(t *testing.T) {
	p := New()
	ctx := context.Background()
	
	// Create a node pointing to an unreachable port to trigger a connection error
	node := &domain.ModelNode{
		ID:      "test-node",
		BaseURL: "http://127.0.0.1:1", // guaranteed unreachable port
	}
	
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	
	res, err := p.Forward(ctx, node, req, w)
	
	if err == nil {
		t.Error("expected error for unreachable URL, got nil")
	}
	
	// Since client.Do failed, res should be nil and Forward should return error
	if res != nil {
		t.Errorf("expected nil result on hard error, got %v", res)
	}
}

func TestProxy_Forward_Success(t *testing.T) {
	// Mock upstream
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom-Header", "foo")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("created"))
	}))
	defer server.Close()

	p := New()
	node := &domain.ModelNode{
		ID:      "node-1",
		BaseURL: server.URL,
	}
	
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	
	res, err := p.Forward(context.Background(), node, req, w)
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	
	if res.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", res.StatusCode)
	}
	
	if w.Header().Get("X-Custom-Header") != "foo" {
		t.Error("custom header not forwarded to client")
	}
	
	if string(res.Body) != "created" {
		t.Errorf("expected 'created', got %s", string(res.Body))
	}
}
