package metrics

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

type Registry struct {
	requestsTotal    sync.Map // string -> *atomic.Uint64
	tokensTotal      sync.Map // string -> *atomic.Uint64
	queueLength      sync.Map // string -> *atomic.Int64
	workerBusyCount  atomic.Int64
}

var Default = &Registry{}

func (r *Registry) IncRequests(model, status string) {
	label := fmt.Sprintf("model=\"%s\",status=\"%s\"", model, status)
	r.getCounter(&r.requestsTotal, label).Add(1)
}

func (r *Registry) AddTokens(model, tokenType string, count int) {
	if count <= 0 {
		return
	}
	label := fmt.Sprintf("model=\"%s\",type=\"%s\"", model, tokenType)
	r.getCounter(&r.tokensTotal, label).Add(uint64(count))
}

func (r *Registry) SetQueueLength(model string, length int) {
	r.getGauge(&r.queueLength, model).Store(int64(length))
}

func (r *Registry) IncWorkerBusy() { r.workerBusyCount.Add(1) }
func (r *Registry) DecWorkerBusy() { r.workerBusyCount.Add(-1) }

func (r *Registry) WritePrometheus(w io.Writer) {
	fmt.Fprintln(w, "# HELP proxyllm_requests_total Total number of requests processed.")
	fmt.Fprintln(w, "# TYPE proxyllm_requests_total counter")
	r.requestsTotal.Range(func(k, v any) bool {
		fmt.Fprintf(w, "proxyllm_requests_total{%s} %d\n", k, v.(*atomic.Uint64).Load())
		return true
	})

	fmt.Fprintln(w, "# HELP proxyllm_tokens_total Total number of tokens processed.")
	fmt.Fprintln(w, "# TYPE proxyllm_tokens_total counter")
	r.tokensTotal.Range(func(k, v any) bool {
		fmt.Fprintf(w, "proxyllm_tokens_total{%s} %d\n", k, v.(*atomic.Uint64).Load())
		return true
	})

	fmt.Fprintln(w, "# HELP proxyllm_queue_length Current number of requests in queue.")
	fmt.Fprintln(w, "# TYPE proxyllm_queue_length gauge")
	r.queueLength.Range(func(k, v any) bool {
		fmt.Fprintf(w, "proxyllm_queue_length{model=\"%s\"} %d\n", k, v.(*atomic.Int64).Load())
		return true
	})

	fmt.Fprintln(w, "# HELP proxyllm_worker_busy_count Current number of busy worker goroutines.")
	fmt.Fprintln(w, "# TYPE proxyllm_worker_busy_count gauge")
	fmt.Fprintf(w, "proxyllm_worker_busy_count %d\n", r.workerBusyCount.Load())
}

func (r *Registry) getCounter(m *sync.Map, label string) *atomic.Uint64 {
	v, _ := m.LoadOrStore(label, &atomic.Uint64{})
	return v.(*atomic.Uint64)
}

func (r *Registry) getGauge(m *sync.Map, label string) *atomic.Int64 {
	v, _ := m.LoadOrStore(label, &atomic.Int64{})
	return v.(*atomic.Int64)
}
