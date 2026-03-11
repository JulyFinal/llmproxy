package router

import (
	"sort"
	"sync"

	"proxyllm/internal/domain"
)

// Router holds the live node table in memory, protected by sync.RWMutex.
type Router struct {
	mu       sync.RWMutex
	// nodes indexed by ID
	nodes    map[string]*domain.ModelNode
	// alias → sorted node list (sorted by Priority asc)
	aliasIdx map[string][]*domain.ModelNode
}

// New creates and returns an empty Router.
func New() *Router {
	return &Router{
		nodes:    make(map[string]*domain.ModelNode),
		aliasIdx: make(map[string][]*domain.ModelNode),
	}
}

// Sync replaces the entire node table atomically (called on startup + admin API changes).
func (r *Router) Sync(nodes []*domain.ModelNode) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nodes = make(map[string]*domain.ModelNode, len(nodes))
	for _, n := range nodes {
		r.nodes[n.ID] = n
	}
	r.rebuildAliasIdx()
}

// AddOrUpdate adds or updates a single node and refreshes the alias index.
func (r *Router) AddOrUpdate(node *domain.ModelNode) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nodes[node.ID] = node
	r.rebuildAliasIdx()
}

// Remove removes a node by ID.
func (r *Router) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.nodes, id)
	r.rebuildAliasIdx()
}

// Resolve returns candidate nodes for the given alias and endpoint type.
// Filters: node.Enabled == true, node.EndpointType matches (EndpointAll matches anything).
// Returns nodes sorted by Priority (ascending). Empty slice if none found.
func (r *Router) Resolve(alias string, et domain.EndpointType) []*domain.ModelNode {
	// Hold the read lock for the entire filter pass so we never read stale
	// node fields after a concurrent AddOrUpdate has replaced the pointer.
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := r.aliasIdx[alias]
	candidates := make([]*domain.ModelNode, 0, len(all))
	for _, n := range all {
		if !n.Enabled {
			continue
		}
		if !endpointMatches(n.EndpointType, et) {
			continue
		}
		candidates = append(candidates, n)
	}
	return candidates
}

// endpointMatches reports whether a node's endpoint type satisfies the requested type.
// EndpointAll on either side is a wildcard.
func endpointMatches(nodeType, requested domain.EndpointType) bool {
	if nodeType == domain.EndpointAll || requested == domain.EndpointAll {
		return true
	}
	return nodeType == requested
}

// ListEnabled returns all enabled nodes (used by /v1/models).
func (r *Router) ListEnabled() []*domain.ModelNode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*domain.ModelNode, 0, len(r.nodes))
	for _, n := range r.nodes {
		if n.Enabled {
			out = append(out, n)
		}
	}
	return out
}

// rebuildAliasIdx rebuilds aliasIdx from r.nodes.
// Must be called with r.mu held for writing.
func (r *Router) rebuildAliasIdx() {
	idx := make(map[string][]*domain.ModelNode)
	for _, n := range r.nodes {
		for _, alias := range n.Aliases {
			idx[alias] = append(idx[alias], n)
		}
	}
	for alias := range idx {
		sort.Slice(idx[alias], func(i, j int) bool {
			return idx[alias][i].Priority < idx[alias][j].Priority
		})
	}
	r.aliasIdx = idx
}
