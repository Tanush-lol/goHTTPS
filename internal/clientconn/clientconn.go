// Package clientconn provides the client side of the goHTTPS chat. It hides the
// two transports (raw TLS socket vs. HTTPS long-poll) behind a single Conn
// interface so the client GUI doesn't care which protocol is in use.
//
// Both dialers use InsecureSkipVerify because the server mints a fresh
// self-signed certificate on each start. That is fine for this local dev/demo
// tool; do not do this against real servers.
package clientconn

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"goHTTPS/internal/proto"
)

// Conn is a live connection to the server from the client's point of view.
type Conn interface {
	// Send delivers a chat message to the admin.
	Send(text string) error
	// Incoming streams messages the admin sent to this client. Closed on disconnect.
	Incoming() <-chan proto.Message
	// Done is closed when the connection drops (server hung up, network error).
	Done() <-chan struct{}
	// Close tells the server we're leaving and releases resources.
	Close() error
}

func devTLS() *tls.Config { return &tls.Config{InsecureSkipVerify: true} }

// ---- socket transport -------------------------------------------------------

type socketConn struct {
	conn     *tls.Conn
	incoming chan proto.Message
	done     chan struct{}
}

// DialSocket opens a persistent TLS connection and registers name as the first
// JSON line. A background reader feeds admin messages into Incoming().
func DialSocket(addr, name string) (Conn, error) {
	conn, err := tls.Dial("tcp", addr, devTLS())
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	if err := proto.WriteJSONLine(conn, proto.Register{Name: name}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("register: %w", err)
	}

	s := &socketConn{
		conn:     conn,
		incoming: make(chan proto.Message, 32),
		done:     make(chan struct{}),
	}
	go s.readLoop()
	return s, nil
}

func (s *socketConn) readLoop() {
	defer close(s.done)
	defer close(s.incoming)
	scanner := proto.NewLineReader(s.conn)
	for scanner.Scan() {
		var m proto.Message
		if err := proto.DecodeLine(scanner.Bytes(), &m); err != nil {
			continue // skip malformed line
		}
		s.incoming <- m
	}
}

func (s *socketConn) Send(text string) error {
	return proto.WriteJSONLine(s.conn, proto.Message{From: "me", To: proto.Admin, Text: text})
}

func (s *socketConn) Incoming() <-chan proto.Message { return s.incoming }
func (s *socketConn) Done() <-chan struct{}          { return s.done }
func (s *socketConn) Close() error                   { return s.conn.Close() }

// ---- https long-poll transport ---------------------------------------------

type httpConn struct {
	base     string // e.g. https://host:port
	id       string
	client   *http.Client
	incoming chan proto.Message
	done     chan struct{}
	closeOne chan struct{} // closed once by Close to stop the poll loop
}

// DialHTTPS registers over HTTPS and starts a long-poll loop that feeds admin
// messages into Incoming().
func DialHTTPS(addr, name string) (Conn, error) {
	base := "https://" + addr
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: devTLS()},
		Timeout:   35 * time.Second, // a touch above the server's poll wait
	}

	body, _ := json.Marshal(map[string]string{"name": name})
	resp, err := client.Post(base+"/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	defer resp.Body.Close()
	var reg struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil || reg.ID == "" {
		return nil, fmt.Errorf("register: bad response")
	}

	h := &httpConn{
		base:     base,
		id:       reg.ID,
		client:   client,
		incoming: make(chan proto.Message, 32),
		done:     make(chan struct{}),
		closeOne: make(chan struct{}),
	}
	go h.pollLoop()
	return h, nil
}

func (h *httpConn) pollLoop() {
	defer close(h.done)
	defer close(h.incoming)
	q := url.Values{"id": {h.id}}.Encode()
	for {
		select {
		case <-h.closeOne:
			return
		default:
		}

		resp, err := h.client.Get(h.base + "/poll?" + q)
		if err != nil {
			// Network blip: pause briefly and retry unless we're closing.
			select {
			case <-h.closeOne:
				return
			case <-time.After(time.Second):
				continue
			}
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return // server forgot us
		}
		var out struct {
			Messages []proto.Message `json:"messages"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			continue
		}
		for _, m := range out.Messages {
			h.incoming <- m
		}
	}
}

func (h *httpConn) Send(text string) error {
	body, _ := json.Marshal(map[string]string{"text": text})
	resp, err := h.client.Post(h.base+"/send?id="+url.QueryEscape(h.id), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("send: status %d", resp.StatusCode)
	}
	return nil
}

func (h *httpConn) Incoming() <-chan proto.Message { return h.incoming }
func (h *httpConn) Done() <-chan struct{}          { return h.done }

func (h *httpConn) Close() error {
	select {
	case <-h.closeOne:
	default:
		close(h.closeOne)
	}
	// Best-effort goodbye so the server marks us offline immediately.
	req, _ := http.NewRequest(http.MethodPost, h.base+"/bye?id="+url.QueryEscape(h.id), nil)
	if req != nil {
		if resp, err := h.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	return nil
}
