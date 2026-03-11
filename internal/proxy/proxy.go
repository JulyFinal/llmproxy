package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"proxyllm/internal/domain"
)

const defaultTimeoutSec = 120

// Proxy forwards a single request to an upstream node.
type Proxy struct {
	client *http.Client
}

// New constructs a Proxy with a default HTTP client.
// Per-request timeouts are enforced via context, so the client itself has no
// global timeout.
func New() *Proxy {
	return &Proxy{
		client: &http.Client{},
	}
}

// ForwardResult is returned by Forward for both streaming and non-streaming
// requests.
type ForwardResult struct {
	StatusCode int
	Body       []byte        // non-streaming response body
	Stream     *StreamResult // non-nil for streaming
	DurationMs int64
}

// Forward sends the request to the given node.
//   - Clones the incoming request
//   - Rewrites Host/URL to node.BaseURL + original path
//   - Replaces model name in body with node.ModelName
//   - Applies node.Override via DeepMerge
//   - Attaches node.APIKey as Authorization header (if non-empty)
//   - Propagates the request context (for client disconnect cancellation)
//   - For stream=true: calls ForwardStream and returns a StreamResult
//   - For stream=false: reads full response body and returns it
func (p *Proxy) Forward(ctx context.Context, node *domain.ModelNode, r *http.Request, w http.ResponseWriter) (*ForwardResult, error) {
	// Determine effective timeout.
	timeoutSec := node.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultTimeoutSec
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// Read and parse the incoming request body.
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("proxy: read request body: %w", err)
	}

	var bodyMap map[string]any
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &bodyMap); err != nil {
			return nil, fmt.Errorf("proxy: parse request body: %w", err)
		}
	} else {
		bodyMap = make(map[string]any)
	}

	// Replace model name.
	if node.ModelName != "" {
		bodyMap["model"] = node.ModelName
	}

	// Apply node overrides.
	ApplyOverride(bodyMap, node.Override)

	// Determine whether this is a streaming request.
	isStream, _ := bodyMap["stream"].(bool)

	// Serialise the modified body.
	modifiedBody, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("proxy: marshal modified body: %w", err)
	}

	// Build the upstream URL: node base URL + original request path + query.
	upstreamURL := node.BaseURL + r.URL.RequestURI()

	upstreamReq, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL, bytes.NewReader(modifiedBody))
	if err != nil {
		return nil, fmt.Errorf("proxy: build upstream request: %w", err)
	}

	// Copy original headers, then override as needed.
	for key, vals := range r.Header {
		for _, v := range vals {
			upstreamReq.Header.Add(key, v)
		}
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Host = upstreamReq.URL.Host

	if node.APIKey != "" {
		upstreamReq.Header.Set("Authorization", "Bearer "+node.APIKey)
	}

	start := time.Now()
	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		return nil, fmt.Errorf("proxy: upstream request: %w", err)
	}
	defer resp.Body.Close()

	result := &ForwardResult{
		StatusCode: resp.StatusCode,
	}

	if isStream {
		// Copy upstream response headers to the client.
		for key, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(key, v)
			}
		}
		w.Header().Set("Transfer-Encoding", "chunked")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)

		streamResult, err := ForwardStream(ctx, w, resp)
		if err != nil {
			result.DurationMs = time.Since(start).Milliseconds()
			result.Stream = streamResult
			return result, fmt.Errorf("proxy: stream forward: %w", err)
		}
		result.Stream = streamResult
	} else {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("proxy: read upstream response: %w", err)
		}
		result.Body = body

		// Write response to client.
		for key, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(key, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		if _, err := w.Write(body); err != nil {
			result.DurationMs = time.Since(start).Milliseconds()
			return result, fmt.Errorf("proxy: write response to client: %w", err)
		}
	}

	result.DurationMs = time.Since(start).Milliseconds()
	return result, nil
}
