// Package frame is the 9-byte frame header shared by the framed proxy and the
// framed client.
package frame

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	HeaderSize = 9                // u32 payload length, u32 stream id, u8 end flag, big-endian
	MaxPayload = 4096             // response chunk size
	maxRead    = 8 << 10          // largest payload Read accepts (URLs and response chunks)
	Addr       = "127.0.0.1:8889" // framed endpoint
)

var ErrTooLarge = errors.New("frame: payload too large")

// Write sends one frame, header and payload in a single Write call.
func Write(w io.Writer, id uint32, end bool, payload []byte) error {
	buf := make([]byte, HeaderSize+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(buf[4:8], id)
	if end {
		buf[8] = 1
	}
	copy(buf[HeaderSize:], payload)
	_, err := w.Write(buf)
	return err
}

// Read reads one frame. It returns io.EOF if the stream ends cleanly before a
// header, and io.ErrUnexpectedEOF if a frame is cut short.
func Read(r io.Reader) (id uint32, end bool, payload []byte, err error) {
	var h [HeaderSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, false, nil, err
	}
	n := binary.BigEndian.Uint32(h[0:4])
	if n > maxRead {
		return 0, false, nil, ErrTooLarge
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF // the header promised more bytes
		}
		return 0, false, nil, err
	}
	return binary.BigEndian.Uint32(h[4:8]), h[8] != 0, payload, nil
}
