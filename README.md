# goHTTPS

A small Go project demonstrating secure (TLS) chat between a **server** and its
**clients**, with **native GUIs** built in [Gio](https://gioui.org). It supports two
transports for the same chat:

- **HTTPS mode** — chat carried over an HTTPS API using **long-polling** (stdlib only).
- **Socket mode** — chat carried over a raw, persistent **TLS socket** (line-framed JSON).

The **server admin** picks the protocol + port, then sees a live **roster of connected
clients** with **online/offline** status and chats **privately with each selected
client**. Each **client** picks protocol/host/port + a display name, connects, and chats
with the admin. When a client connects it appears online on the server; when it
disconnects it flips to offline.

TLS certificates are **self-signed and generated in memory** at server startup — no
`openssl`, no key files, no setup.

## Layout

```
cmd/server            # Gio GUI: pick https|socket + port, then admin chat console
cmd/client            # Gio GUI: pick https|socket + host/port/name, then chat with admin
internal/proto        # shared wire types + JSON-line framing
internal/chat         # transport-agnostic hub (roster, history, presence) + HTTPS long-poll API
internal/clientconn   # client-side Conn abstraction (DialSocket / DialHTTPS)
internal/gui          # shared Gio widgets (message list, input row, status dot)
internal/certs        # in-memory self-signed certificate generation
```

## Requirements / dev shell

The GUIs render natively on **Wayland** (with X11/XWayland fallback). A `shell.nix`
pins the Go toolchain plus the Wayland/EGL/X11 libraries Gio needs:

```bash
nix-shell          # enter the dev environment (uses ./shell.nix)
go build ./...     # build server + client
```

> All `go` commands below assume you are inside `nix-shell` so the native libraries
> are on the loader path.

## Run

Open **two terminals**, both inside `nix-shell`.

**Terminal 1 — server:**

```bash
go run ./cmd/server
```

A window opens. Pick **HTTPS** or **Raw TLS socket**, set the port (default `5000`), and
click **Start server**. You land on the chat console with an (initially empty) client
roster on the left.

**Terminal 2 — client:**

```bash
go run ./cmd/client
```

Pick the **same mode**, set host `localhost`, the **same port**, type a display name
(e.g. `alice`), and click **Connect**.

Now:

- The server roster shows **alice** with a **green dot (online)**.
- Click **alice** in the roster, type a message, press **Enter** or **Send** — it appears
  in alice's client window.
- alice replies from the client window; it appears in the server's conversation pane.
- Close the client window → alice flips to a **grey dot (offline)** on the server
  (immediately for socket mode; within ~30s for HTTPS, via the heartbeat timeout).

Multiple clients can connect at once; the admin talks to each one privately.

## How it works

- **One hub, two transports.** Both the socket accept-loop and the HTTPS long-poll
  handlers feed the same `chat.Hub`, so the server GUI is identical regardless of
  protocol. Only message *delivery* differs: a JSON line written to the live
  `net.Conn` (socket) vs. a message queued for the next `/poll` (HTTPS).
- **Real-time over HTTPS.** HTTP isn't push-based, so the client long-polls `GET /poll`
  (blocks up to ~25s); the same request doubles as a presence heartbeat.
- **GUI redraws** are driven from background goroutines via Gio's `Window.Invalidate`.

## Notes

- The client uses `InsecureSkipVerify` because the server mints a fresh self-signed
  certificate on every start. That's fine for a local dev/demo tool — **don't** do this
  against real servers.
- TLS 1.2 is the enforced minimum (`tls.VersionTLS12`).

## Tests

End-to-end transport tests (real TLS, both protocols, presence + bidirectional
messaging) live in `internal/chat`:

```bash
go test -race ./...
```
