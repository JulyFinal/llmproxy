package router

import (
	"sync"
	"testing"

	"proxyllm/internal/domain"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func node(id string, aliases []string, priority int, et domain.EndpointType, enabled bool) *domain.ModelNode {
	return &domain.ModelNode{
		ID:           id,
		Aliases:      aliases,
		Priority:     priority,
		EndpointType: et,
		Enabled:      enabled,
	}
}

// ── Router.Resolve ────────────────────────────────────────────────────────────

func TestResolve_PriorityOrder(t *testing.T) {
	r := New()
	r.Sync([]*domain.ModelNode{
		node("b", []string{"llm"}, 2, domain.EndpointChat, true),
		node("a", []string{"llm"}, 1, domain.EndpointChat, true),
		node("c", []string{"llm"}, 3, domain.EndpointChat, true),
	})

	got := r.Resolve("llm", domain.EndpointChat)
	if len(got) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(got))
	}
	// Must be sorted by priority ascending
	for i := 1; i < len(got); i++ {
		if got[i].Priority < got[i-1].Priority {
			t.Errorf("not sorted: [%d].Priority=%d > [%d].Priority=%d",
				i-1, got[i-1].Priority, i, got[i].Priority)
		}
	}
}

func TestResolve_DisabledNodeFiltered(t *testing.T) {
	r := New()
	r.Sync([]*domain.ModelNode{
		node("a", []string{"llm"}, 1, domain.EndpointChat, true),
		node("b", []string{"llm"}, 2, domain.EndpointChat, false), // disabled
	})

	got := r.Resolve("llm", domain.EndpointChat)
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("want only node a, got %v", nodeIDs(got))
	}
}

func TestResolve_EndpointTypeFiltering(t *testing.T) {
	r := New()
	r.Sync([]*domain.ModelNode{
		node("chat",  []string{"m"}, 1, domain.EndpointChat,      true),
		node("embed", []string{"m"}, 2, domain.EndpointEmbedding, true),
		node("all",   []string{"m"}, 3, domain.EndpointAll,       true),
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
		node("a", []string{"llm"}, 1, domain.EndpointChat, true),
	})
	got := r.Resolve("unknown-model", domain.EndpointChat)
	if len(got) != 0 {
		t.Errorf("want empty, got %v", nodeIDs(got))
	}
}

func TestResolve_AllDisabled(t *testing.T) {
	r := New()
	r.Sync([]*domain.ModelNode{
		node("a", []string{"llm"}, 1, domain.EndpointChat, false),
		node("b", []string{"llm"}, 2, domain.EndpointChat, false),
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
		node("a", []string{"llm"}, 1, domain.EndpointChat, true),
		node("b", []string{"llm"}, 2, domain.EndpointChat, true),
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
				r.AddOrUpdate(node("a", []string{"llm"}, 1, domain.EndpointChat, j%2 == 0))
			}
		}()
	}
	wg.Wait()
}

// ── balancer.Pick ─────────────────────────────────────────────────────────────

func TestPick_AlwaysTopPriority(t *testing.T) {
	candidates := []*domain.ModelNode{
		node("pri1", nil, 1, domain.EndpointChat, true),
		node("pri2", nil, 2, domain.EndpointChat, true),
		node("pri3", nil, 3, domain.EndpointChat, true),
	}
	for i := 0; i < 200; i++ {
		picked := Pick(candidates)
		if picked == nil {
			t.Fatal("Pick returned nil")
		}
		if picked.ID != "pri1" {
			t.Errorf("iteration %d: want pri1 (highest priority), got %s", i, picked.ID)
		}
	}
}

func TestPick_SamePriority_EqualDistribution(t *testing.T) {
	// equal distribution: both nodes should get roughly 40-60% of samples
	candidates := []*domain.ModelNode{
		node("a", nil, 1, domain.EndpointChat, true),
		node("b", nil, 1, domain.EndpointChat, true),
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
	t.Logf("equal distribution: a=%d (%.1f%%)  b=%d (%.1f%%)",
		counts["a"], 100*pctA,
		counts["b"], 100*pctB)
}

func TestPick_SamePriority_EqualWeight_BothUsed(t *testing.T) {
	candidates := []*domain.ModelNode{
		node("a", nil, 1, domain.EndpointChat, true),
		node("b", nil, 1, domain.EndpointChat, true),
	}
	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		counts[Pick(candidates).ID]++
	}
	if counts["a"] == 0 || counts["b"] == 0 {
		t.Errorf("both nodes should be selected, got a=%d b=%d", counts["a"], counts["b"])
	}
	t.Logf("equal weight distribution: a=%d  b=%d", counts["a"], counts["b"])
}

func TestPick_SingleCandidate(t *testing.T) {
	candidates := []*domain.ModelNode{
		node("only", nil, 1, domain.EndpointChat, true),
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
	// Reset the global counter to a known state for this sub-test.
	// We can't reset atomic.Uint64 directly, so we just verify the pattern
	// across a large sample: each node should be picked roughly N/2 times.
	candidates := []*domain.ModelNode{
		node("a", nil, 1, domain.EndpointChat, true),
		node("b", nil, 1, domain.EndpointChat, true),
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

// ── priority fallback (integration of Resolve + Pick) ─────────────────────────

func TestPriorityFallback_WhenTopNodeDisabled(t *testing.T) {
	r := New()
	r.Sync([]*domain.ModelNode{
		node("primary",   []string{"llm"}, 1, domain.EndpointChat, true),
		node("secondary", []string{"llm"}, 2, domain.EndpointChat, true),
	})

	// Normal: always primary
	for i := 0; i < 50; i++ {
		candidates := r.Resolve("llm", domain.EndpointChat)
		if got := Pick(candidates); got.ID != "primary" {
			t.Errorf("before disable: want primary, got %s", got.ID)
		}
	}

	// Disable primary → all traffic falls to secondary
	r.AddOrUpdate(node("primary", []string{"llm"}, 1, domain.EndpointChat, false))

	for i := 0; i < 50; i++ {
		candidates := r.Resolve("llm", domain.EndpointChat)
		if len(candidates) == 0 {
			t.Fatal("no candidates after disabling primary")
		}
		if got := Pick(candidates); got.ID != "secondary" {
			t.Errorf("after disable: want secondary, got %s", got.ID)
		}
	}

	// Re-enable primary → back to primary
	r.AddOrUpdate(node("primary", []string{"llm"}, 1, domain.EndpointChat, true))

	for i := 0; i < 50; i++ {
		candidates := r.Resolve("llm", domain.EndpointChat)
		if got := Pick(candidates); got.ID != "primary" {
			t.Errorf("after re-enable: want primary, got %s", got.ID)
		}
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
