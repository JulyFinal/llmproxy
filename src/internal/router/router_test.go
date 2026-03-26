package router

import (
	"sync"
	"testing"

	"proxyllm/internal/domain"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func node(id string, aliases []string, et domain.EndpointType, enabled bool) *domain.ModelNode {
	return &domain.ModelNode{
		ID:           id,
		Aliases:      aliases,
		EndpointType: et,
		Enabled:      enabled,
	}
}

// ── Router.Resolve ────────────────────────────────────────────────────────────

func TestResolve_Normal(t *testing.T) {
	r := New()
	r.Sync([]*domain.ModelNode{
		node("b", []string{"llm"}, domain.EndpointChat, true),
		node("a", []string{"llm"}, domain.EndpointChat, true),
		node("c", []string{"llm"}, domain.EndpointChat, true),
	})

	got := r.Resolve("llm", domain.EndpointChat)
	if len(got) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(got))
	}
}

func TestResolve_DisabledNodeFiltered(t *testing.T) {
	r := New()
	r.Sync([]*domain.ModelNode{
		node("a", []string{"llm"}, domain.EndpointChat, true),
		node("b", []string{"llm"}, domain.EndpointChat, false), // disabled
	})

	got := r.Resolve("llm", domain.EndpointChat)
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("want only node a, got %v", nodeIDs(got))
	}
}

func TestResolve_EndpointTypeFiltering(t *testing.T) {
	r := New()
	r.Sync([]*domain.ModelNode{
		node("chat",  []string{"m"}, domain.EndpointChat,      true),
		node("embed", []string{"m"}, domain.EndpointEmbedding, true),
		node("all",   []string{"m"}, domain.EndpointAll,       true),
	})

	chatCandidates := r.Resolve("m", domain.EndpointChat)
	ids := nodeIDs(chatCandidates)
	if !contains(ids, "chat") || !contains(ids, "all") {
		t.Errorf("chat resolve: want [chat all], got %v", ids)
	}
	if contains(ids, "embed") {
		t.Errorf("chat resolve should NOT include embed node, got %v", ids)
	}

	embedCandidates := r.Resolve("m", domain.EndpointEmbedding)
	ids = nodeIDs(embedCandidates)
	if !contains(ids, "embed") || !contains(ids, "all") {
		t.Errorf("embed resolve: want [embed all], got %v", ids)
	}
	if contains(ids, "chat") {
		t.Errorf("embed resolve should NOT include chat node, got %v", ids)
	}
}

func TestResolve_UnknownAlias(t *testing.T) {
	r := New()
	r.Sync([]*domain.ModelNode{
		node("a", []string{"llm"}, domain.EndpointChat, true),
	})
	got := r.Resolve("unknown-model", domain.EndpointChat)
	if len(got) != 0 {
		t.Errorf("want empty, got %v", nodeIDs(got))
	}
}

func TestResolve_AllDisabled(t *testing.T) {
	r := New()
	r.Sync([]*domain.ModelNode{
		node("a", []string{"llm"}, domain.EndpointChat, false),
		node("b", []string{"llm"}, domain.EndpointChat, false),
	})
	got := r.Resolve("llm", domain.EndpointChat)
	if len(got) != 0 {
		t.Errorf("want empty, got %v", nodeIDs(got))
	}
}

func TestResolve_ConcurrentSafeUnderRace(t *testing.T) {
	// Run with -race to verify the RLock fix holds.
	r := New()
	nodes := []*domain.ModelNode{
		node("a", []string{"llm"}, domain.EndpointChat, true),
		node("b", []string{"llm"}, domain.EndpointChat, true),
	}
	r.Sync(nodes)

	var wg sync.WaitGroup
	// 10 readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Resolve("llm", domain.EndpointChat)
			}
		}()
	}
	// 2 writers
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.AddOrUpdate(node("a", []string{"llm"}, domain.EndpointChat, j%2 == 0))
			}
		}()
	}
	wg.Wait()
}

// ── balancer.Pick ─────────────────────────────────────────────────────────────

func TestPick_Distribution(t *testing.T) {
	// equal distribution: both nodes should get roughly 40-60% of samples
	candidates := []*domain.ModelNode{
		node("a", nil, domain.EndpointChat, true),
		node("b", nil, domain.EndpointChat, true),
	}

	counts := map[string]int{}
	const N = 10000
	for i := 0; i < N; i++ {
		picked := Pick(candidates)
		counts[picked.ID]++
	}

	pctA := float64(counts["a"]) / float64(N)
	pctB := float64(counts["b"]) / float64(N)
	// Expect roughly 50/50; allow ±10% tolerance (40-60%)
	if pctA < 0.40 || pctA > 0.60 {
		t.Errorf("equal distribution: a=%.1f%% b=%.1f%%, want both 40-60%%",
			100*pctA, 100*pctB)
	}
}

func TestPick_SingleCandidate(t *testing.T) {
	candidates := []*domain.ModelNode{
		node("only", nil, domain.EndpointChat, true),
	}
	for i := 0; i < 10; i++ {
		if got := Pick(candidates); got == nil || got.ID != "only" {
			t.Fatalf("want only, got %v", got)
		}
	}
}

func TestPick_Empty(t *testing.T) {
	if got := Pick(nil); got != nil {
		t.Errorf("want nil for empty candidates, got %v", got)
	}
	if got := Pick([]*domain.ModelNode{}); got != nil {
		t.Errorf("want nil for empty candidates, got %v", got)
	}
}

// ── balancer.PickRoundRobin ───────────────────────────────────────────────────

func TestPickRoundRobin_StrictAlternation(t *testing.T) {
	candidates := []*domain.ModelNode{
		node("a", nil, domain.EndpointChat, true),
		node("b", nil, domain.EndpointChat, true),
	}
	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		counts[PickRoundRobin(candidates).ID]++
	}
	// Strict round-robin → exactly 500/500
	if counts["a"] != counts["b"] {
		t.Errorf("strict round-robin: want 500/500, got a=%d b=%d", counts["a"], counts["b"])
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func nodeIDs(nodes []*domain.ModelNode) []string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	return ids
}

func contains(ids []string, id string) bool {
	for _, s := range ids {
		if s == id {
			return true
		}
	}
	return false
}
