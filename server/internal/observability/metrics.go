package observability

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

// Metrics holds content-free operational counters exposed in Prometheus text format.
type Metrics struct {
	RequestsTotal  atomic.Int64
	RequestsSettled atomic.Int64
	RequestsFailed atomic.Int64
	ActiveNodes    atomic.Int64
	LedgerEntries  atomic.Int64
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintf(w, "# TYPE cordova_requests_total counter\ncordova_requests_total %d\n", m.RequestsTotal.Load())
		fmt.Fprintf(w, "# TYPE cordova_requests_settled_total counter\ncordova_requests_settled_total %d\n", m.RequestsSettled.Load())
		fmt.Fprintf(w, "# TYPE cordova_requests_failed_total counter\ncordova_requests_failed_total %d\n", m.RequestsFailed.Load())
		fmt.Fprintf(w, "# TYPE cordova_active_nodes gauge\ncordova_active_nodes %d\n", m.ActiveNodes.Load())
		fmt.Fprintf(w, "# TYPE cordova_ledger_entries_total gauge\ncordova_ledger_entries_total %d\n", m.LedgerEntries.Load())
	})
}
