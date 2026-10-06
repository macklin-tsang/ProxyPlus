package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"time"

	"proxyplus/frame"
	"proxyplus/metrics"
)

const rate = 1 << 20 // pacing in bytes/sec per connection (RATE in proxy.py)

type request struct {
	id  uint32
	url string
}

type stream struct {
	id   uint32
	data []byte // response bytes not yet sent
}

// serveFramed answers one framed connection (frame + owner in proxy.py). It is
// the scheduler and the only goroutine that writes to conn: it starts a fetch
// per request, queues each stream once its object is fully fetched, and sends
// one 4096-byte frame per turn in round-robin order, paced at rate. A big
// object therefore never blocks a small one queued behind it.
//
// ponytail: relative pacing drifts if OS timers are coarse; switch to an
// absolute schedule (nextAt += d) if the demo runs slow.
func serveFramed(conn net.Conn) {
	metrics.ActiveConns.Add(1)
	defer metrics.ActiveConns.Add(-1)
	defer conn.Close()
	quit := make(chan struct{})
	defer close(quit) // runs first: releases the reader and any waiting fetchers

	reqs := make(chan request)
	ready := make(chan stream)
	go readRequests(conn, reqs, quit)

	var queue []stream // round-robin line; only this goroutine touches it
	pending := 0       // fetches started but not yet queued
	next := time.After(0)
	for reqs != nil || pending > 0 || len(queue) > 0 {
		send := next
		if len(queue) == 0 {
			send = nil // a nil channel never fires: nothing to send yet
		}
		select {
		case r, ok := <-reqs:
			if !ok {
				reqs = nil // client half-closed: no more requests
				continue
			}
			metrics.Requests.Add(1)
			pending++
			go fetchStream(r, ready, quit)
		case s := <-ready:
			pending--
			queue = append(queue, s)
		case <-send:
			s := queue[0]
			queue = queue[1:]
			n := min(len(s.data), frame.MaxPayload)
			end := n == len(s.data)
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if frame.Write(conn, s.id, end, s.data[:n]) != nil {
				return // client gone or stalled
			}
			metrics.BytesSent.Add(int64(frame.HeaderSize + n))
			if !end {
				s.data = s.data[n:]
				queue = append(queue, s) // back of the line: round robin
			}
			next = time.After(time.Duration(n) * time.Second / rate)
		}
	}
}

// readRequests is the only reader of the connection. It turns request frames
// into requests until the client half-closes, idles for 30s, or the scheduler
// quits.
func readRequests(conn net.Conn, reqs chan<- request, quit <-chan struct{}) {
	defer close(reqs)
	br := bufio.NewReader(conn)
	for {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		id, _, url, err := frame.Read(br)
		if err != nil {
			return // EOF from half-close, idle timeout, or a bad frame
		}
		select {
		case reqs <- request{id, string(url)}:
		case <-quit:
			return
		}
	}
}

// fetchStream fetches one URL (framing_thread). Any failure, even a panic,
// becomes a 502 response so the stream still ends and the scheduler can finish.
func fetchStream(r request, ready chan<- stream, quit <-chan struct{}) {
	s := stream{id: r.id}
	defer func() {
		if e := recover(); e != nil {
			s.data = fmt.Appendf(nil, "HTTP/1.1 502 Bad Gateway\r\n\r\n%v", e)
		}
		select {
		case ready <- s:
		case <-quit: // connection already gone
		}
	}()
	obj, state, key, err := retrieve(r.url, nil)
	if err != nil {
		s.data = []byte("HTTP/1.1 502 Bad Gateway\r\n\r\n" + err.Error())
		return
	}
	log.Printf("stream %d %s %s", r.id, state, key)
	s.data = obj
}
