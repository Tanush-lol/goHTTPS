// Package chat implements the server-side state for the goHTTPS chat: a hub that
// tracks connected clients and routes private messages between the admin and each
// client. It is transport-agnostic — the same hub backs both the raw TLS socket
// server and the HTTPS long-poll server.
package chat

import (
	"fmt"
	"net"
	"sync"
	"time"

	"goHTTPS/internal/proto"
)

// httpTimeout is how long an HTTPS client may go without polling/sending before
// the sweeper marks it offline. Socket clients don't use this — their liveness
// follows the live connection.
const httpTimeout = 30 * time.Second

// Client is one connected peer as seen by the server.
type Client struct {
	ID   string
	Name string

	online   bool
	lastSeen time.Time

	// Exactly one delivery mechanism is set depending on the transport:
	conn   net.Conn           // socket transport: write JSON lines directly
	outbox chan proto.Message // https transport: drained by the /poll handler
}

// ClientView is an immutable snapshot of a client for the GUI to render.
type ClientView struct {
	ID     string
	Name   string
	Online bool
}

// Hub owns all client state and message history. All exported methods are safe
// for concurrent use.
type Hub struct {
	mu      sync.Mutex
	clients map[string]*Client
	order   []string                   // client ids in connection order, for a stable roster
	history map[string][]proto.Message // per-client transcript (admin <-> that client)
	seq     int                        // monotonic counter for generating client ids

	// notify is called (without the lock held) whenever state changes, so the
	// GUI can redraw. Wired to window.Invalidate by the server.
	notify func()
}

// NewHub creates a hub. notify is invoked on every state change; pass a no-op if
// you don't need redraw callbacks.
func NewHub(notify func()) *Hub {
	if notify == nil {
		notify = func() {}
	}
	h := &Hub{
		clients: make(map[string]*Client),
		history: make(map[string][]proto.Message),
		notify:  notify,
	}
	go h.sweepLoop()
	return h
}

// nextID returns a unique client id. Caller must hold h.mu.
func (h *Hub) nextID() string {
	h.seq++
	return fmt.Sprintf("c%d", h.seq)
}

// AddSocketClient registers a client backed by a live TLS connection.
func (h *Hub) AddSocketClient(name string, conn net.Conn) *Client {
	h.mu.Lock()
	c := &Client{ID: h.nextID(), Name: name, online: true, lastSeen: time.Now(), conn: conn}
	h.clients[c.ID] = c
	h.order = append(h.order, c.ID)
	h.mu.Unlock()
	h.notify()
	return c
}

// AddHTTPClient registers a client reachable via long-poll. Messages for it are
// queued on its outbox until the next /poll drains them.
func (h *Hub) AddHTTPClient(name string) *Client {
	h.mu.Lock()
	c := &Client{ID: h.nextID(), Name: name, online: true, lastSeen: time.Now(),
		outbox: make(chan proto.Message, 64)}
	h.clients[c.ID] = c
	h.order = append(h.order, c.ID)
	h.mu.Unlock()
	h.notify()
	return c
}

// get returns the client by id (nil if unknown). Caller must hold h.mu.
func (h *Hub) get(id string) *Client { return h.clients[id] }

// SetOnline flips a client's presence and refreshes its last-seen time.
func (h *Hub) SetOnline(id string, online bool) {
	h.mu.Lock()
	if c := h.get(id); c != nil {
		c.online = online
		c.lastSeen = time.Now()
	}
	h.mu.Unlock()
	h.notify()
}

// Touch records that an HTTPS client is still alive (called on each poll/send).
func (h *Hub) Touch(id string) {
	h.mu.Lock()
	if c := h.get(id); c != nil {
		c.lastSeen = time.Now()
		if !c.online {
			c.online = true
		}
	}
	h.mu.Unlock()
	h.notify()
}

// Remove marks a client offline. Its history is kept so the admin can still read
// the transcript after the client leaves.
func (h *Hub) Remove(id string) {
	h.mu.Lock()
	if c := h.get(id); c != nil {
		c.online = false
		if c.outbox != nil {
			close(c.outbox)
			c.outbox = nil
		}
		c.conn = nil
	}
	h.mu.Unlock()
	h.notify()
}

// FromClient records a message a client sent to the admin.
func (h *Hub) FromClient(id, text string) {
	msg := proto.Message{From: id, To: proto.Admin, Text: text, Time: nowStr()}
	h.mu.Lock()
	if c := h.get(id); c != nil {
		c.lastSeen = time.Now()
	}
	h.history[id] = append(h.history[id], msg)
	h.mu.Unlock()
	h.notify()
}

// ToClient records and delivers a message from the admin to one client. For a
// socket client it writes a JSON line on the connection; for an HTTPS client it
// queues the message for the next poll. Returns an error if the client is gone.
func (h *Hub) ToClient(id, text string) error {
	msg := proto.Message{From: proto.Admin, To: id, Text: text, Time: nowStr()}

	h.mu.Lock()
	c := h.get(id)
	if c == nil {
		h.mu.Unlock()
		return fmt.Errorf("unknown client %q", id)
	}
	h.history[id] = append(h.history[id], msg)
	conn, outbox := c.conn, c.outbox
	h.mu.Unlock()

	switch {
	case conn != nil:
		if err := proto.WriteJSONLine(conn, msg); err != nil {
			h.SetOnline(id, false)
			return err
		}
	case outbox != nil:
		select {
		case outbox <- msg:
		default:
			h.notify()
			return fmt.Errorf("client %q outbox full", id)
		}
	default:
		h.notify()
		return fmt.Errorf("client %q is offline", id)
	}
	h.notify()
	return nil
}

// Poll blocks until a message is queued for an HTTPS client or the deadline
// elapses. It returns any messages drained (possibly empty on timeout) and
// whether the client still exists.
func (h *Hub) Poll(id string, wait time.Duration) ([]proto.Message, bool) {
	h.Touch(id)

	h.mu.Lock()
	c := h.get(id)
	if c == nil || c.outbox == nil {
		h.mu.Unlock()
		return nil, c != nil
	}
	outbox := c.outbox
	h.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()

	var out []proto.Message
	select {
	case m, ok := <-outbox:
		if !ok {
			return out, false
		}
		out = append(out, m)
	case <-timer.C:
		return out, true
	}
	// Drain anything else already queued so we don't make the client round-trip.
	for {
		select {
		case m, ok := <-outbox:
			if !ok {
				return out, false
			}
			out = append(out, m)
		default:
			return out, true
		}
	}
}

// Snapshot returns the roster in stable connection order for the GUI.
func (h *Hub) Snapshot() []ClientView {
	h.mu.Lock()
	defer h.mu.Unlock()
	views := make([]ClientView, 0, len(h.order))
	for _, id := range h.order {
		if c := h.clients[id]; c != nil {
			views = append(views, ClientView{ID: c.ID, Name: c.Name, Online: c.online})
		}
	}
	return views
}

// History returns a copy of the transcript with one client.
func (h *Hub) History(id string) []proto.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	src := h.history[id]
	out := make([]proto.Message, len(src))
	copy(out, src)
	return out
}

// sweepLoop marks HTTPS clients offline once they stop polling. Socket clients
// (conn != nil) are exempt; their presence tracks the live connection.
func (h *Hub) sweepLoop() {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for range tick.C {
		changed := false
		h.mu.Lock()
		for _, c := range h.clients {
			if c.online && c.conn == nil && time.Since(c.lastSeen) > httpTimeout {
				c.online = false
				changed = true
			}
		}
		h.mu.Unlock()
		if changed {
			h.notify()
		}
	}
}

func nowStr() string { return time.Now().Format(time.RFC3339) }
