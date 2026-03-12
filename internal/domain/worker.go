package domain

import (
	"net/http"
)

type RateLimitStatus struct {
	RPMCurrent  int
	RPMLimit    int
	TPMCurrent  int
	TPMLimit    int
	BlockReason string
}

type AttemptLog struct {
	Attempt          int    `json:"attempt"`
	NodeID           string `json:"node_id"`
	NodeName         string `json:"node_name"`
	DurationMs       int64  `json:"duration_ms"`
	StatusCode       int    `json:"status_code"`
	ErrorType        string `json:"error_type"`
	Error            error  `json:"-"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
}

type ExecutionResult struct {
	StatusCode       int
	Body             []byte
	Headers          http.Header
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	FinalNodeID      string
	Attempts         []*AttemptLog
	ExecutionMs      int64
	Error            string
	ErrorType        string
}
