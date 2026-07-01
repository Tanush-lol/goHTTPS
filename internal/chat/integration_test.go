package chat_test

import (
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"goHTTPS/internal/certs"
	"goHTTPS/internal/chat"
	"goHTTPS/internal/clientconn"
	"goHTTPS/internal/proto"
)

// testTLS spins up a self-signed TLS config like the real server uses.
func testTLS(t *testing.T) *tls.Config {
	t.Helper()
	cert, err := certs.GenerateSelfSigned()
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

// waitFor polls cond until true or the deadline; fails the test otherwise.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func onlineCount(h *chat.Hub) int {
	n := 0
	for _, c := range h.Snapshot() {
		if c.Online {
			n++
		}
	}
	return n
}

// TestSocketRoundTrip verifies a raw TLS socket client shows up online and that
// messages flow admin<->client in both directions.
func TestSocketRoundTrip(t *testing.T) {
	hub := chat.NewHub(nil)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", testTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Minimal socket accept loop mirroring cmd/server.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				sc := proto.NewLineReader(conn)
				if !sc.Scan() {
					return
				}
				var reg proto.Register
				_ = proto.DecodeLine(sc.Bytes(), &reg)
				c := hub.AddSocketClient(reg.Name, conn)
				defer hub.Remove(c.ID)
				for sc.Scan() {
					var m proto.Message
					if proto.DecodeLine(sc.Bytes(), &m) == nil {
						hub.FromClient(c.ID, m.Text)
					}
				}
			}(conn)
		}
	}()

	conn, err := clientconn.DialSocket(ln.Addr().String(), "alice")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	waitFor(t, "alice online", func() bool { return onlineCount(hub) == 1 })

	id := hub.Snapshot()[0].ID

	// client -> admin
	if err := conn.Send("hi admin"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "client msg in history", func() bool {
		h := hub.History(id)
		return len(h) == 1 && h[0].Text == "hi admin" && h[0].From == id
	})

	// admin -> client
	if err := hub.ToClient(id, "hello alice"); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-conn.Incoming():
		if m.Text != "hello alice" || m.From != proto.Admin {
			t.Fatalf("bad admin msg: %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not receive admin message")
	}

	// disconnect -> offline
	conn.Close()
	waitFor(t, "alice offline", func() bool { return onlineCount(hub) == 0 })
}

// TestHTTPSRoundTrip verifies the long-poll transport end to end.
func TestHTTPSRoundTrip(t *testing.T) {
	hub := chat.NewHub(nil)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", testTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: hub.HTTPHandler()}
	go srv.Serve(ln)
	defer srv.Close()

	conn, err := clientconn.DialHTTPS(ln.Addr().String(), "bob")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	waitFor(t, "bob online", func() bool { return onlineCount(hub) == 1 })
	id := hub.Snapshot()[0].ID

	// client -> admin
	if err := conn.Send("yo"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "client msg in history", func() bool {
		h := hub.History(id)
		return len(h) == 1 && h[0].Text == "yo"
	})

	// admin -> client (delivered via the poll loop)
	if err := hub.ToClient(id, "hey bob"); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-conn.Incoming():
		if m.Text != "hey bob" || m.From != proto.Admin {
			t.Fatalf("bad admin msg: %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not receive admin message")
	}

	// graceful bye -> offline
	conn.Close()
	waitFor(t, "bob offline", func() bool { return onlineCount(hub) == 0 })
}
