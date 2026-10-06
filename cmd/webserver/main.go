// Command webserver is a static file HTTP server built on raw TCP sockets
// (webserver.py). It answers one GET per connection with 200, 304, 400, 403,
// 404 or 505. Run it from the repo root: it serves the current directory.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxyplus/metrics"
)

const (
	addr        = "0.0.0.0:8080"
	metricsAddr = "127.0.0.1:9101"
	serverName  = "MiniProjectServer/1.0"
	maxHead     = 8 << 10 // request heads are cut off after 8 KiB
)

func main() {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		log.Fatal(err)
	}
	metrics.Serve(metricsAddr)
	root, _ := filepath.Abs(".")
	log.Printf("Serving %s on http://%s (metrics on http://%s/metrics)", root, addr, metricsAddr)
	serve(ln, ".")
}

// serve accepts connections forever, one goroutine per connection.
func serve(ln net.Listener, root string) {
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
			handleConnection(conn, root)
		}()
	}
}

func handleConnection(conn net.Conn, root string) {
	metrics.ActiveConns.Add(1)
	defer metrics.ActiveConns.Add(-1)
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, headers, err := readHead(bufio.NewReader(io.LimitReader(conn, maxHead)))
	if err != nil {
		return // client sent nothing or timed out: close silently, like Python
	}
	log.Printf("[%v] %s", conn.RemoteAddr(), line)
	metrics.Requests.Add(1)

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	n, _ := conn.Write(respond(root, line, headers))
	metrics.BytesSent.Add(int64(n))
}

// readHead reads the request line and headers up to the blank line
// (parse_request). Header names are lowercased. A head cut short by EOF is
// still used, as in Python; any other read error is returned.
func readHead(br *bufio.Reader) (string, map[string]string, error) {
	first, err := br.ReadString('\n')
	if err != nil && (first == "" || !errors.Is(err, io.EOF)) {
		return "", nil, err
	}
	headers := map[string]string{}
	for err == nil {
		var line string
		line, err = br.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return strings.TrimRight(first, "\r\n"), headers, nil
}

// respond builds the full response for one request (the body of
// handle_connection). The checks run in the same order as in Python.
func respond(root, line string, headers map[string]string) []byte {
	date := time.Now().UTC().Format(http.TimeFormat)
	parts := strings.Split(line, " ")
	if len(parts) != 3 {
		return buildResponse(400, date, "", []byte("Bad Request"))
	}
	method, target, version := parts[0], parts[1], parts[2]
	if version != "HTTP/1.0" && version != "HTTP/1.1" {
		return buildResponse(505, date, "", []byte("HTTP Version Not Supported"))
	}
	if method != "GET" {
		return buildResponse(404, date, "", []byte("Not Found"))
	}

	name, ok := resolvePath(target)
	if !ok {
		return buildResponse(403, date, "", []byte("Forbidden"))
	}
	f, err := os.Open(filepath.Join(root, name))
	if errors.Is(err, fs.ErrPermission) {
		return buildResponse(403, date, "", []byte("Forbidden"))
	}
	if err != nil {
		return buildResponse(404, date, "", []byte("<h1>404 Not Found</h1>"))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		return buildResponse(404, date, "", []byte("<h1>404 Not Found</h1>"))
	}

	lastModified := "Last-Modified: " + fi.ModTime().UTC().Format(http.TimeFormat) + "\r\n"
	// HTTP dates have whole seconds, so compare against the truncated mtime.
	if ims, err := http.ParseTime(headers["if-modified-since"]); err == nil && !fi.ModTime().Truncate(time.Second).After(ims) {
		return buildResponse(304, date, lastModified, nil)
	}

	body, err := io.ReadAll(f)
	if err != nil {
		return buildResponse(403, date, "", []byte("Forbidden"))
	}
	contentType := "application/octet-stream"
	if strings.HasSuffix(name, ".html") {
		contentType = "text/html"
	}
	return buildResponse(200, date, "Content-Type: "+contentType+"\r\n"+lastModified, body)
}

// resolvePath turns a URL path into a file name relative to the server root
// (resolve_path). ok is false if the name would leave the root.
func resolvePath(target string) (name string, ok bool) {
	p, _, _ := strings.Cut(target, "?")
	p, _, _ = strings.Cut(p, "#")
	if p == "/" {
		p = "/test.html"
	}
	// Clean first so "/a/../test.html" works; IsLocal then rejects "..",
	// absolute paths, drive letters and Windows device names such as NUL.
	name = filepath.Clean(strings.TrimLeft(p, "/"))
	return name, filepath.IsLocal(name)
}

// buildResponse formats a response like build_response. extra holds
// preformatted "Name: value\r\n" lines so the header order stays fixed.
func buildResponse(status int, date, extra string, body []byte) []byte {
	head := fmt.Sprintf("HTTP/1.1 %d %s\r\nServer: %s\r\nDate: %s\r\nConnection: close\r\n",
		status, http.StatusText(status), serverName, date)
	if len(body) > 0 {
		head += fmt.Sprintf("Content-Length: %d\r\n", len(body))
	}
	return append([]byte(head+extra+"\r\n"), body...)
}
