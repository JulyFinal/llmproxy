package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// StreamResult holds statistics collected during streaming.
type StreamResult struct {
	PromptTokens     int
	CompletionTokens int
	FullContent      strings.Builder // accumulated assistant content
	RawLines         []string        // raw SSE lines for detail logging
}

// ForwardStream reads SSE from upstream resp.Body, writes each chunk to w,
// and accumulates token counts from the final chunk's usage field.
//
// It respects ctx cancellation: if ctx is done, it stops reading and returns.
//
// Token counting strategy:
//  1. Parse the last non-[DONE] SSE chunk's `usage` field if present.
//  2. If absent, count CompletionTokens by accumulating delta.content lengths
//     and estimating (len(content)/4 as rough token count).
//
// The caller is responsible for setting appropriate response headers before
// calling ForwardStream.
func ForwardStream(ctx context.Context, w http.ResponseWriter, resp *http.Response) (*StreamResult, error) {
	flusher, canFlush := w.(http.Flusher)

	result := &StreamResult{}
	var accumulatedContent strings.Builder
	var usageFound bool

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		// Honour context cancellation between lines.
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		line := scanner.Text()
		result.RawLines = append(result.RawLines, line)

		// Forward the line verbatim to the client.
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return result, err
		}
		if canFlush {
			flusher.Flush()
		}

		// Only process data lines for token accounting.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if strings.TrimSpace(payload) == "[DONE]" {
			continue
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(bytes.NewReader([]byte(payload))).Decode(&chunk); err != nil {
			continue
		}

		// Accumulate usage from the final chunk that carries it.
		if chunk.Usage != nil {
			result.PromptTokens = chunk.Usage.PromptTokens
			result.CompletionTokens = chunk.Usage.CompletionTokens
			usageFound = true
		}

		// Accumulate delta content regardless; used as fallback token estimate.
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				accumulatedContent.WriteString(choice.Delta.Content)
				result.FullContent.WriteString(choice.Delta.Content)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return result, err
	}

	// Fallback token estimation when the upstream did not send usage data.
	if !usageFound {
		content := accumulatedContent.String()
		result.CompletionTokens = len(content) / 4
	}

	return result, nil
}
