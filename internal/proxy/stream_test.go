package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForwardStream_LargeChunk(t *testing.T) {
	// Create a large SSE chunk (> 64KB)
	largeContent := strings.Repeat("a", 100*1024)
	largeChunk := fmt.Sprintf(`data: {"choices":[{"delta":{"content":"%s"}}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`+"\n\n", largeContent)

	bodyStr := largeChunk + "data: [DONE]\n\n"
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(bodyStr)),
	}

	rec := httptest.NewRecorder()
	result, err := ForwardStream(context.Background(), rec, resp)
	if err != nil {
		t.Fatalf("ForwardStream failed for large chunk: %v", err)
	}

	if result.PromptTokens != 10 || result.CompletionTokens != 20 {
		t.Errorf("expected 10/20 tokens, got %d/%d", result.PromptTokens, result.CompletionTokens)
	}

	if len(result.FullContent.String()) != 100*1024 {
		t.Errorf("expected 100KB content, got %d bytes", len(result.FullContent.String()))
	}
}
