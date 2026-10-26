// Command framedclient fetches several URLs through the framed proxy over one
// connection and prints when each stream finishes. If the
// proxy interleaves frames, small objects finish long before big ones.
package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"proxyplus/frame"
)

func main() {
	urls := os.Args[1:]
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "usage: framedclient <url> [url ...]")
		os.Exit(1)
	}
	conn, err := net.Dial("tcp4", frame.Addr)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	t0 := time.Now()
	if err := sendRequests(conn.(*net.TCPConn), urls); err != nil {
		log.Fatal(err)
	}
	if err := receiveResponses(conn, t0); err != nil {
		log.Fatal(err)
	}
}

// sendRequests sends each URL as one frame (stream ids 1, 3, 5, ...), then
// half-closes the connection so the proxy knows no more requests are coming.
func sendRequests(conn *net.TCPConn, urls []string) error {
	for i, url := range urls {
		if err := frame.Write(conn, uint32(i*2+1), false, []byte(url)); err != nil {
			return err
		}
	}
	return conn.CloseWrite()
}

// receiveResponses reads frames until the proxy closes the connection and
// prints each stream's total size when its end frame arrives.
func receiveResponses(r io.Reader, t0 time.Time) error {
	received := map[uint32]int{}
	br := bufio.NewReader(r)
	for {
		id, end, payload, err := frame.Read(br)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		received[id] += len(payload)
		if end {
			fmt.Printf("stream %d  %9d B  done at %.3fs\n", id, received[id], time.Since(t0).Seconds())
		}
	}
}
