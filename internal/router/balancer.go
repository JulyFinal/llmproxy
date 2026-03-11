package router

import (
	"math/rand"
	"sync/atomic"

	"proxyllm/internal/domain"
)

// globalCounter is used by PickRoundRobin for deterministic rotation.
var globalCounter atomic.Uint64

// Pick selects one node from candidates using priority + equal random selection:
//  1. Group candidates by Priority (lowest value = highest priority group).
//  2. Take the highest priority group.
//  3. Within that group, pick uniformly at random.
//
// Returns nil if candidates is empty.
func Pick(candidates []*domain.ModelNode) *domain.ModelNode {
	group := topPriorityGroup(candidates)
	if len(group) == 0 {
		return nil
	}
	return group[rand.Intn(len(group))]
}

// PickRoundRobin does strict round-robin within the top-priority group.
// Uses an atomic counter; suitable when you want deterministic rotation.
func PickRoundRobin(candidates []*domain.ModelNode) *domain.ModelNode {
	group := topPriorityGroup(candidates)
	if len(group) == 0 {
		return nil
	}
	idx := globalCounter.Add(1) - 1
	return group[int(idx%uint64(len(group)))]
}

// topPriorityGroup extracts nodes belonging to the lowest Priority value in candidates.
func topPriorityGroup(candidates []*domain.ModelNode) []*domain.ModelNode {
	if len(candidates) == 0 {
		return nil
	}
	minPri := candidates[0].Priority
	for _, n := range candidates[1:] {
		if n.Priority < minPri {
			minPri = n.Priority
		}
	}
	group := make([]*domain.ModelNode, 0, len(candidates))
	for _, n := range candidates {
		if n.Priority == minPri {
			group = append(group, n)
		}
	}
	return group
}
