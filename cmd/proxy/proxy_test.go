package main

import (
	"bufio"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"proxyplus/frame"
	"proxyplus/metrics"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		target, host string
		wantHost     string
		wantPort     int
		wantPath     string
		wantErr      bool
	}{
		{"http://Example.com/a?b=1", "", "example.com", 80, "/a?b=1", false},
		{"http://h:8080", "", "h", 8080, "/", false},
		{"HTTP://h/x", "", "h", 80, "/x", false},
		{"/x", "h:81", "h", 81, "/x", false}, // relative target: host comes from the Host header
		{"http://h:abc/", "", "", 0, "", true},
		{"https://h/x", "", "", 0, "", true}, // unsupported schemes must not become a host
		{"ftp://h/x", "h", "", 0, "", true},
		{"/x", "", "", 0, "", true}, // no host anywhere
	}
	for _, c := range cases {
		host, port, path, err := parseTarget(c.target, c.host)
		if (err != nil) != c.wantErr || host != c.wantHost || port != c.wantPort || path != c.wantPath {
			t.Errorf("parseTarget(%q, %q) = %q %d %q %v", c.target, c.host, host, port, path, err)
		}
	}
}

// origin is a stub origin server: it records each raw request it receives and
// replies with fixed bytes, so tests see exactly what the proxy sends upstream.
type origin struct {
	ln    net.Listener
	pages map[string]string // request path -> raw response; unknown paths get an empty reply
	mu    sync.Mutex
	reqs  []string
}

func newOrigin(t *testing.T, pages map[string]string) *origin {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	o := &origin{ln: ln, pages: pages}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				var req strings.Builder
				for {
					line, err := br.ReadString('\n')
					req.WriteString(line)
					if err != nil || line == "\r\n" {
						break
					}
				}
				o.mu.Lock()
				o.reqs = append(o.reqs, req.String())
				o.mu.Unlock()
				if f := strings.Fields(req.String()); len(f) >= 2 {
					io.WriteString(conn, o.pages[f[1]])
				}
			}()
		}
	}()
	return o
}

func (o *origin) host() string           { return o.ln.Addr().String() }
func (o *origin) url(path string) string { return "http://" + o.host() + path }

func (o *origin) requests() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.reqs...)
}

// startProxy runs a proxy listener on a random port and returns its address.
// It also empties the package-level cache so tests do not see each other's entries.
func startProxy(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	cacheMu.Lock()
	clear(cache)
	cacheMu.Unlock()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go serve(ln, handle)
	return ln.Addr().String()
}

// ask sends one raw request and reads the reply until the proxy closes.
func ask(t *testing.T, addr, req string) string {
	t.Helper()
	conn, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second)) // hang guard
	io.WriteString(conn, req)
	b, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const okHead = "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n"

func TestProxy(t *testing.T) {
	o := newOrigin(t, map[string]string{
		"/a": okHead + "hi", "/b": okHead + "concurrent",
		"/gone": "HTTP/1.1 404 Not Found\r\nConnection: close\r\n\r\nno",
	})
	proxy := startProxy(t, handleProxy)
	hits, misses := metrics.CacheHits.Load(), metrics.CacheMisses.Load()

	// MISS, then HIT: identical except X-Cache, which sits right after the status line.
	miss := ask(t, proxy, "GET "+o.url("/a")+" HTTP/1.1\r\nUser-Agent: tester\r\n\r\n")
	if want := "HTTP/1.1 200 OK\r\nX-Cache: MISS\r\nConnection: close\r\n\r\nhi"; miss != want {
		t.Errorf("first reply = %q, want %q", miss, want)
	}
	hit := ask(t, proxy, "GET "+o.url("/a")+" HTTP/1.1\r\n\r\n")
	if want := "HTTP/1.1 200 OK\r\nX-Cache: HIT\r\nConnection: close\r\n\r\nhi"; hit != want {
		t.Errorf("second reply = %q, want %q", hit, want)
	}
	// A relative target with a Host header maps to the same cache entry.
	if rel := ask(t, proxy, "GET /a HTTP/1.1\r\nHost: "+o.host()+"\r\n\r\n"); rel != hit {
		t.Errorf("relative reply = %q, want %q", rel, hit)
	}
	if h, m := metrics.CacheHits.Load()-hits, metrics.CacheMisses.Load()-misses; h != 2 || m != 1 {
		t.Errorf("cache metrics: %d hits, %d misses, want 2 and 1", h, m)
	}
	// Exactly one upstream request, with exactly the bytes the proxy should send.
	want := "GET /a HTTP/1.1\r\nHost: " + o.host() + "\r\nUser-Agent: tester\r\nAccept-Encoding: identity\r\nConnection: close\r\n\r\n"
	if got := o.requests(); len(got) != 1 || got[0] != want {
		t.Errorf("upstream requests = %q, want one %q", got, want)
	}

	// Many clients at once: the race detector checks the cache mutex.
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if r := ask(t, proxy, "GET "+o.url("/b")+" HTTP/1.1\r\n\r\n"); !strings.HasSuffix(r, "concurrent") {
				t.Errorf("concurrent reply = %q", r)
			}
		})
	}
	wg.Wait()

	// 501 for anything but plain-HTTP GET.
	const want501 = "HTTP/1.1 501 Not Implemented\r\nConnection: close\r\n\r\n"
	for _, req := range []string{
		"POST " + o.url("/a") + " HTTP/1.1\r\n\r\n",
		"CONNECT example.com:443 HTTP/1.1\r\n\r\n",
		"GET https://example.com/ HTTP/1.1\r\n\r\n",
	} {
		if got := ask(t, proxy, req); got != want501 {
			t.Errorf("%q: got %q, want %q", req, got, want501)
		}
	}

	// Only 200 replies are cached: a 404 goes back to the origin every time.
	for range 2 {
		if got := ask(t, proxy, "GET "+o.url("/gone")+" HTTP/1.1\r\n\r\n"); !strings.HasPrefix(got, "HTTP/1.1 404 Not Found\r\nX-Cache: MISS\r\n") {
			t.Errorf("404 reply = %q", got)
		}
	}
	fetched := 0
	for _, r := range o.requests() {
		if strings.HasPrefix(r, "GET /gone ") {
			fetched++
		}
	}
	if fetched != 2 {
		t.Errorf("origin saw %d requests for /gone, want 2 (404s are not cached)", fetched)
	}

	// 502 for a bad port, and for an empty upstream reply, which is not cached.
	const head502 = "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n"
	if got := ask(t, proxy, "GET http://127.0.0.1:abc/ HTTP/1.1\r\n\r\n"); !strings.HasPrefix(got, head502) {
		t.Errorf("bad port: got %q", got)
	}
	if got := ask(t, proxy, "GET "+o.url("/none")+" HTTP/1.1\r\n\r\n"); got != head502+"Empty Response" {
		t.Errorf("empty upstream: got %q", got)
	}
	cacheMu.Lock()
	_, cached := cache["http://"+o.host()+"/none"]
	cacheMu.Unlock()
	if cached {
		t.Error("an empty upstream reply was cached")
	}
}

// frameRecord is one frame as the client saw it.
type frameRecord struct {
	id  uint32
	end bool
	n   int
}

func TestFramed(t *testing.T) {
	const big = 1 << 20 // about 1s of pacing, so the small stream always finishes first
	o := newOrigin(t, map[string]string{
		"/big":   okHead + strings.Repeat("x", big),
		"/small": okHead + "small",
	})
	proxy := startProxy(t, serveFramed)

	conn, err := net.Dial("tcp4", proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(15 * time.Second)) // hang guard for the whole exchange
	urls := []string{o.url("/big"), o.url("/small"), "http://127.0.0.1:abc/"}
	for i, u := range urls {
		if err := frame.Write(conn, uint32(2*i+1), false, []byte(u)); err != nil {
			t.Fatal(err)
		}
	}
	conn.(*net.TCPConn).CloseWrite()

	var frames []frameRecord
	payload := map[uint32][]byte{}
	br := bufio.NewReader(conn)
	for {
		id, end, p, err := frame.Read(br)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err) // includes a hang: the connection deadline fires
		}
		frames = append(frames, frameRecord{id, end, len(p)})
		payload[id] = append(payload[id], p...)
	}

	// Each stream has exactly one end flag, on its last frame; other frames are full chunks.
	last := map[uint32]int{}
	for i, f := range frames {
		if f.end {
			if _, dup := last[f.id]; dup {
				t.Errorf("stream %d has two end frames", f.id)
			}
			last[f.id] = i
		} else if f.n != frame.MaxPayload {
			t.Errorf("frame %d of stream %d has %d bytes, want %d", i, f.id, f.n, frame.MaxPayload)
		}
	}
	if len(last) != 3 {
		t.Fatalf("got end frames for streams %v, want 1, 3 and 5", last)
	}
	for i, f := range frames {
		if i > last[f.id] {
			t.Errorf("stream %d has a frame (index %d) after its end frame", f.id, i)
		}
	}

	// Payloads: the origin reply with X-Cache spliced in, and a single 502 frame.
	if want := "HTTP/1.1 200 OK\r\nX-Cache: MISS\r\nConnection: close\r\n\r\nsmall"; string(payload[3]) != want {
		t.Errorf("small stream = %q, want %q", payload[3], want)
	}
	if want := len(okHead) + big + len("X-Cache: MISS\r\n"); len(payload[1]) != want {
		t.Errorf("big stream = %d bytes, want %d", len(payload[1]), want)
	}
	if !strings.HasPrefix(string(payload[5]), "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n") {
		t.Errorf("bad-port stream = %q", payload[5])
	}
	if frames[last[5]].n != len(payload[5]) {
		t.Errorf("the 502 stream should be a single frame")
	}

	// The small object finishes while the big one is still being sent.
	if last[3] > last[1] {
		t.Errorf("small stream ended at frame %d, after the big one at %d: no interleaving", last[3], last[1])
	}

	// Round robin. Between two consecutive frames of an unfinished stream, every other
	// stream sends at most one frame, and exactly one if it was already being sent
	// before and still has frames afterwards. This does not depend on arrival times.
	for i, f := range frames {
		if f.end {
			continue
		}
		j := i + 1
		for frames[j].id != f.id {
			j++
		}
		for _, other := range []uint32{1, 3, 5} {
			if other == f.id {
				continue
			}
			between, before, after := 0, false, false
			for k, g := range frames {
				if g.id != other {
					continue
				}
				switch {
				case k < i:
					before = true
				case k > j:
					after = true
				default:
					between++
				}
			}
			if between > 1 || (before && after && between != 1) {
				t.Errorf("frames %d..%d of stream %d: stream %d sent %d frames between them (before=%v after=%v)",
					i, j, f.id, other, between, before, after)
			}
		}
	}
}
