package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolvePath(t *testing.T) {
	cases := map[string]bool{ // URL path -> allowed
		"/":                true,
		"/test.html?q=1#x": true,
		"//":               true, // the root dir itself: 404, as in Python
		"/a/../test.html":  true,
		"/../x":            false,
		"/../ProxyPlus2/x": false, // Python's startswith check let this through
	}
	if runtime.GOOS == "windows" {
		cases[`/..\..\x`] = false
		cases["/C:/Windows/win.ini"] = false
		cases["/NUL"] = false
	}
	for path, want := range cases {
		if _, ok := resolvePath(path); ok != want {
			t.Errorf("resolvePath(%q) ok = %v, want %v", path, ok, want)
		}
	}
	if name, _ := resolvePath("/?a=b"); name != "test.html" {
		t.Errorf(`resolvePath("/?a=b") = %q, want test.html`, name)
	}
}

func TestBuildResponse(t *testing.T) {
	got := string(buildResponse(200, "D", "Content-Type: text/html\r\n", []byte("hi")))
	want := "HTTP/1.1 200 OK\r\nServer: MiniProjectServer/1.0\r\nDate: D\r\nConnection: close\r\n" +
		"Content-Length: 2\r\nContent-Type: text/html\r\n\r\nhi"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := string(buildResponse(304, "D", "", nil)); strings.Contains(got, "Content-Length") {
		t.Errorf("304 must not have Content-Length: %q", got)
	}
}

func TestWebServer(t *testing.T) {
	root := t.TempDir()
	page := "<p>hello</p>"
	if err := os.WriteFile(filepath.Join(root, "test.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(root, "test.html"))
	lastModified := fi.ModTime().UTC().Format(http.TimeFormat)

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serve(ln, root)

	cases := []struct{ req, status, body string }{
		{"GET / HTTP/1.1\r\n\r\n", "200 OK", page},
		{"GET /test.html?a=1 HTTP/1.0\r\n\r\n", "200 OK", page},
		{"GET / HTTP/1.1\r\nIf-Modified-Since: " + lastModified + "\r\n\r\n", "304 Not Modified", ""},
		{"GET / HTTP/1.1\r\nIf-Modified-Since: Mon, 01 Jan 1990 00:00:00 GMT\r\n\r\n", "200 OK", page},
		{"GET / HTTP/1.1\r\nIf-Modified-Since: yesterday\r\n\r\n", "200 OK", page},
		{"GET /../x HTTP/1.1\r\n\r\n", "403 Forbidden", "Forbidden"},
		{"GET /nope HTTP/1.1\r\n\r\n", "404 Not Found", "<h1>404 Not Found</h1>"},
		{"POST / HTTP/1.1\r\n\r\n", "404 Not Found", "Not Found"},
		{"GET / HTTP/2.0\r\n\r\n", "505 HTTP Version Not Supported", "HTTP Version Not Supported"},
		{"GET /\r\n\r\n", "400 Bad Request", "Bad Request"},
		{"", "", ""}, // connect and close: no response at all
	}
	for _, c := range cases {
		resp := roundTrip(t, ln.Addr().String(), c.req)
		if c.status == "" {
			if resp != "" {
				t.Errorf("%q: got %q, want no response", c.req, resp)
			}
			continue
		}
		if !strings.HasPrefix(resp, "HTTP/1.1 "+c.status+"\r\n") || !strings.HasSuffix(resp, "\r\n\r\n"+c.body) {
			t.Errorf("%q:\ngot %q\nwant status %q and body %q", c.req, resp, c.status, c.body)
		}
	}
}

// roundTrip sends one raw request, half-closes, and reads until the server closes.
func roundTrip(t *testing.T, addr, req string) string {
	t.Helper()
	conn, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(req))
	conn.(*net.TCPConn).CloseWrite()
	b, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
