package frame

import (
	"bytes"
	"io"
	"testing"
)

func TestFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, 1, true, make([]byte, MaxPayload)); err != nil {
		t.Fatal(err)
	}
	// Same bytes as Python's HDR.pack(4096, 1, True).
	if got, want := buf.Bytes()[:HeaderSize], []byte{0, 0, 0x10, 0, 0, 0, 0, 1, 1}; !bytes.Equal(got, want) {
		t.Fatalf("header = % x, want % x", got, want)
	}
	id, end, p, err := Read(&buf)
	if err != nil || id != 1 || !end || len(p) != MaxPayload {
		t.Fatalf("Read = %d %v %d %v", id, end, len(p), err)
	}
	if _, _, _, err := Read(&buf); err != io.EOF {
		t.Fatalf("empty stream: err = %v, want io.EOF", err)
	}

	Write(&buf, 3, false, []byte("abc"))
	buf.Truncate(buf.Len() - 1)
	if _, _, _, err := Read(&buf); err != io.ErrUnexpectedEOF {
		t.Fatalf("truncated frame: err = %v, want io.ErrUnexpectedEOF", err)
	}

	buf.Reset()
	Write(&buf, 5, false, make([]byte, maxRead+1))
	if _, _, _, err := Read(&buf); err != ErrTooLarge {
		t.Fatalf("oversized frame: err = %v, want ErrTooLarge", err)
	}
}
