package proxy

import (
	"bufio"
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
	// Default buffer is 64K; increase to 10MB for large chunks (tool calls, structured output).
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
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
		// Also try SGLang responses style
		var sgChunk struct {
			Response struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}

		rawPayload := []byte(payload)
		if err := json.Unmarshal(rawPayload, &chunk); err == nil {
			// Accumulate usage from the final chunk that carries it (OpenAI style).
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

		if !usageFound {
			if err := json.Unmarshal(rawPayload, &sgChunk); err == nil {
				if sgChunk.Response.Usage.InputTokens > 0 {
					result.PromptTokens = sgChunk.Response.Usage.InputTokens
					result.CompletionTokens = sgChunk.Response.Usage.OutputTokens
					usageFound = true
				}
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
