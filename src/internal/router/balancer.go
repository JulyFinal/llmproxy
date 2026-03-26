package router

import (
	"math/rand"
	"sync/atomic"

	"proxyllm/internal/domain"
)

// globalCounter is used by PickRoundRobin for deterministic rotation.
var globalCounter atomic.Uint64

// Pick selects one node from candidates using uniform random selection.
//
// Returns nil if candidates is empty.
func Pick(candidates []*domain.ModelNode) *domain.ModelNode {
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return candidates[rand.Intn(len(candidates))]
}

// PickRoundRobin does strict round-robin within the candidates.
// Uses an atomic counter; suitable when you want deterministic rotation.
func PickRoundRobin(candidates []*domain.ModelNode) *domain.ModelNode {
	if len(candidates) == 0 {
		return nil
	}
	idx := globalCounter.Add(1) % uint64(len(candidates))
	return candidates[idx]
}
