// Package proto defines the wire format shared by the goHTTPS server and client.
//
// A chat message is a small JSON object. Over the raw TLS socket transport each
// message is sent as one JSON object per line ("JSON lines"); over HTTPS the same
// struct is carried in request/response bodies.
package proto

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Admin is the reserved sender/recipient id for the server operator.
const Admin = "admin"

// Message is a single chat message exchanged between the admin and a client.
//
// From/To are either Admin or a client id. Time is an RFC3339 string filled in
// by the server so both ends agree on ordering without sharing a clock.
type Message struct {
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
	Time string `json:"time"`
}

// Register is the first frame a socket client sends so the server learns its
// display name before any chat traffic.
type Register struct {
	Name string `json:"name"`
}

// WriteJSONLine encodes v as a single JSON line (terminated by '\n'). It is safe
// for the line-framed socket transport because json.Marshal never emits a raw
// newline inside the object.
func WriteJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// NewLineReader wraps r in a scanner sized for chat lines. Each successful Scan
// yields one JSON object; decode it with DecodeLine.
func NewLineReader(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 4096), 1<<20) // allow up to 1 MiB messages
	return s
}

// DecodeLine unmarshals one scanned line into v.
func DecodeLine(line []byte, v any) error {
	return json.Unmarshal(line, v)
}
