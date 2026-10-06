// Package metrics exposes Prometheus metrics in the plain-text exposition
// format using only the standard library. Every binary exposes the same set;
// metrics a binary never touches (cache, upstream) simply stay at zero.
package metrics

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var (
	Requests      atomic.Int64 // HTTP requests and framed streams handled
	CacheHits     atomic.Int64
	CacheMisses   atomic.Int64
	ActiveConns   atomic.Int64 // client connections currently open
	BytesSent     atomic.Int64 // bytes written to clients
	UpstreamBytes atomic.Int64 // bytes read from origin servers
)

// Upstream latency histogram: per-bucket counts, bucket upper bounds in seconds.
var latencyBounds = [...]float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

var latency struct {
	sync.Mutex
	counts [len(latencyBounds) + 1]int64 // last slot is +Inf
	sum    float64
}

// ObserveUpstream records how long one origin fetch took.
func ObserveUpstream(d time.Duration) {
	s := d.Seconds()
	latency.Lock()
	latency.counts[sort.SearchFloat64s(latencyBounds[:], s)]++ // first bound >= s
	latency.sum += s
	latency.Unlock()
}

// Serve starts the /metrics endpoint on addr in the background.
func Serve(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		write(w)
	})
	go func() { log.Fatalf("metrics: %v", http.ListenAndServe(addr, mux)) }()
}

func write(w io.Writer) {
	metric := func(name, typ, help string, v int64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", name, help, name, typ, name, v)
	}
	metric("proxyplus_requests_total", "counter", "HTTP requests and framed streams handled.", Requests.Load())
	metric("proxyplus_cache_hits_total", "counter", "Proxy cache hits.", CacheHits.Load())
	metric("proxyplus_cache_misses_total", "counter", "Proxy cache misses.", CacheMisses.Load())
	metric("proxyplus_active_connections", "gauge", "Client connections currently open.", ActiveConns.Load())
	metric("proxyplus_bytes_sent_total", "counter", "Bytes written to clients.", BytesSent.Load())
	metric("proxyplus_upstream_bytes_total", "counter", "Bytes read from origin servers.", UpstreamBytes.Load())

	latency.Lock()
	counts, sum := latency.counts, latency.sum // copy, then write without the lock
	latency.Unlock()
	const h = "proxyplus_upstream_latency_seconds"
	fmt.Fprintf(w, "# HELP %s Time to fetch one object from an origin server.\n# TYPE %s histogram\n", h, h)
	var cum int64
	for i, c := range counts {
		cum += c
		le := "+Inf"
		if i < len(latencyBounds) {
			le = strconv.FormatFloat(latencyBounds[i], 'g', -1, 64)
		}
		fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", h, le, cum)
	}
	fmt.Fprintf(w, "%s_sum %g\n%s_count %d\n", h, sum, h, cum)
}
