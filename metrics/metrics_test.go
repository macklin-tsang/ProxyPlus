package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestWrite(t *testing.T) {
	Requests.Add(2)
	ObserveUpstream(3 * time.Millisecond)
	ObserveUpstream(20 * time.Second)

	var b strings.Builder
	write(&b, true)
	out := b.String()
	for _, want := range []string{
		"# TYPE proxyplus_requests_total counter\nproxyplus_requests_total 2\n",
		"# TYPE proxyplus_active_connections gauge\n",
		`proxyplus_upstream_latency_seconds_bucket{le="0.0025"} 0` + "\n",
		`proxyplus_upstream_latency_seconds_bucket{le="0.005"} 1` + "\n",
		`proxyplus_upstream_latency_seconds_bucket{le="10"} 1` + "\n",
		`proxyplus_upstream_latency_seconds_bucket{le="+Inf"} 2` + "\n",
		"proxyplus_upstream_latency_seconds_sum 20.003\n",
		"proxyplus_upstream_latency_seconds_count 2\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	b.Reset()
	write(&b, false)
	if out := b.String(); !strings.Contains(out, "proxyplus_requests_total") || strings.Contains(out, "cache") || strings.Contains(out, "upstream") {
		t.Errorf("web server output should have only the shared metrics:\n%s", out)
	}
}
