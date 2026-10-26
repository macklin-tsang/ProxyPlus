// Command proxy is a caching HTTP forward proxy built on raw TCP sockets
// It also serves the framed endpoint in framed.go.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxyplus/frame"
	"proxyplus/metrics"
)

const (
	addr        = "127.0.0.1:8888"
	metricsAddr = "127.0.0.1:9102"
	defaultUA   = "MiniProjectProxy/1.0"
	maxHead     = 8 << 10 // request heads are cut off after 8 KiB
)

// The cache maps "http://host:port/path" to the raw upstream response. Only
// 200 replies are stored, so an error page is never served from the cache.
// one global lock, unbounded, no eviction, concurrent misses both
// fetch; add an in-flight map or LRU if origin load or memory matters.
var (
	cacheMu sync.Mutex
	cache   = map[string][]byte{}
)

func main() {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		log.Fatal(err)
	}
	fln, err := net.Listen("tcp4", frame.Addr)
	if err != nil {
		log.Fatal(err)
	}
	metrics.Serve(metricsAddr, true)
	log.Printf("Framed proxy on %s rate=%d", frame.Addr, rate)
	log.Printf("Proxy listening on http://%s (metrics on http://%s/metrics)", addr, metricsAddr)
	go serve(fln, serveFramed)
	serve(ln, handleProxy)
}

// serve accepts connections forever, one goroutine per connection.
func serve(ln net.Listener, handle func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go func() {
			defer func() {
				if e := recover(); e != nil {
					log.Printf("Error handling %v: %v", conn.RemoteAddr(), e)
				}
			}()
			handle(conn)
		}()
	}
}

// handleProxy answers one proxied GET.
func handleProxy(conn net.Conn) {
	metrics.ActiveConns.Add(1)
	defer metrics.ActiveConns.Add(-1)
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, headers, err := readHead(bufio.NewReader(io.LimitReader(conn, maxHead)))
	if err != nil {
		return // no complete request head: close silently
	}
	metrics.Requests.Add(1)
	method, target, _ := strings.Cut(line, " ")
	target, _, _ = strings.Cut(target, " ")

	var resp []byte
	if method != "GET" || strings.HasPrefix(strings.ToLower(target), "https://") {
		resp = errorResponse(501, "")
	} else if obj, state, key, err := retrieve(target, headers); err != nil {
		resp = errorResponse(502, err.Error())
	} else {
		log.Printf("[%v] %s %s", conn.RemoteAddr(), state, key)
		resp = obj
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	n, _ := conn.Write(resp)
	metrics.BytesSent.Add(int64(n))
}

// readHead reads the request line and headers up to the blank line. Unlike the
// web server, the proxy needs the whole head: EOF before the blank line is an error.
func readHead(br *bufio.Reader) (string, map[string]string, error) {
	first, err := br.ReadString('\n')
	headers := map[string]string{}
	for err == nil {
		var line string
		line, err = br.ReadString('\n')
		if line == "\r\n" || line == "\n" {
			return strings.TrimRight(first, "\r\n"), headers, nil
		}
		if k, v, ok := strings.Cut(strings.TrimRight(line, "\r\n"), ":"); ok {
			headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return "", nil, err
}

// errorResponse builds an error reply with optional detail text as the body.
func errorResponse(status int, detail string) []byte {
	return fmt.Appendf(nil, "HTTP/1.1 %d %s\r\nConnection: close\r\n\r\n%s", status, http.StatusText(status), detail)
}

// retrieve fetches target through the cache and splices an X-Cache header in
// right after the status line The cached bytes never change.
func retrieve(target string, headers map[string]string) (obj []byte, state, key string, err error) {
	host, port, path, err := parseTarget(target, headers["host"])
	if err != nil {
		return nil, "", "", err
	}
	key = fmt.Sprintf("http://%s:%d%s", host, port, path)
	ua := headers["user-agent"]
	if ua == "" {
		ua = defaultUA
	}
	resp, state, err := cachedFetch(key, host, port, path, ua)
	if err != nil {
		return nil, "", key, err
	}
	status, rest, _ := bytes.Cut(resp, []byte("\r\n"))
	return slices.Concat(status, []byte("\r\nX-Cache: "+state+"\r\n"), rest), state, key, nil
}

// parseTarget splits a request target into host, port and path.
// A relative target such as "/x" takes its host from the Host header.
func parseTarget(target, hostHeader string) (host string, port int, path string, err error) {
	if !strings.HasPrefix(strings.ToLower(target), "http://") {
		// Only plain http is supported; "https://x" or "ftp://x" must not become a host name.
		if scheme, _, ok := strings.Cut(target, "://"); ok && !strings.Contains(scheme, "/") {
			return "", 0, "", fmt.Errorf("unsupported scheme %q", scheme)
		}
		target = "http://" + hostHeader + target
	}
	authority, rest, _ := strings.Cut(target[len("http://"):], "/")
	host, p, _ := strings.Cut(authority, ":")
	port = 80
	if p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			return "", 0, "", fmt.Errorf("bad port %q", p)
		}
	}
	if host == "" {
		return "", 0, "", errors.New("missing host")
	}
	return strings.ToLower(host), port, "/" + rest, nil
}

// cachedFetch returns the cached response for key, or fetches and stores it
// The lock is never held during network I/O.
func cachedFetch(key, host string, port int, path, ua string) ([]byte, string, error) {
	cacheMu.Lock()
	resp, ok := cache[key]
	cacheMu.Unlock()
	if ok {
		metrics.CacheHits.Add(1)
		return resp, "HIT", nil
	}
	metrics.CacheMisses.Add(1)
	resp, err := fetch(host, port, path, ua)
	if err != nil {
		return nil, "", err
	}
	if len(resp) == 0 {
		return nil, "", errors.New("Empty Response")
	}
	if isOK(resp) {
		cacheMu.Lock()
		cache[key] = resp
		cacheMu.Unlock()
	}
	return resp, "MISS", nil
}

// isOK reports whether resp starts with a 200 status line.
func isOK(resp []byte) bool {
	line, _, _ := bytes.Cut(resp, []byte("\r\n"))
	f := strings.Fields(string(line))
	return len(f) >= 2 && f[1] == "200"
}

// fetch sends a plain GET to the origin and reads the raw response until the
// origin closes the connection. The whole fetch must finish within 10s.
func fetch(host string, port int, path, ua string) ([]byte, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("%s:%d", host, port), 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s:%d\r\nUser-Agent: %s\r\n"+
		"Accept-Encoding: identity\r\nConnection: close\r\n\r\n", path, host, port, ua)
	if err != nil {
		return nil, err
	}
	resp, err := io.ReadAll(conn)
	metrics.UpstreamBytes.Add(int64(len(resp)))
	if err != nil {
		return nil, err
	}
	metrics.ObserveUpstream(time.Since(start))
	return resp, nil
}
