// Command server runs the goHTTPS chat server with a native (Gio) GUI.
//
// The admin first picks a communication mode (HTTPS or raw TLS socket) and a
// port, then lands on a chat console: a roster of connected clients showing who
// is online/offline, and a per-client message pane for talking to whichever
// client is selected. Both transports are backed by the same chat.Hub.
package main

import (
	"crypto/tls"
	"fmt"
	"image"
	"image/color"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"goHTTPS/internal/certs"
	"goHTTPS/internal/chat"
	"goHTTPS/internal/gui"
	"goHTTPS/internal/proto"
)

func main() {
	go func() {
		w := new(app.Window)
		w.Option(app.Title("goHTTPS server"), app.Size(unit.Dp(900), unit.Dp(600)))
		if err := newUI(w).loop(w); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

// ui holds all GUI state for the server window.
type ui struct {
	th  *material.Theme
	hub *chat.Hub

	// setup screen
	mode      widget.Enum
	portEd    widget.Editor
	startBtn  widget.Clickable
	setupErr  string
	started   bool
	statusMsg string

	// chat screen
	roster    widget.List
	rosterSel []widget.Clickable // one clickable per roster row
	selected  string             // selected client id
	msgList   widget.List
	input     widget.Editor
	sendBtn   widget.Clickable
}

func newUI(w *app.Window) *ui {
	u := &ui{th: material.NewTheme()}
	u.mode.Value = "https"
	u.portEd.SingleLine = true
	u.portEd.SetText("5000")
	u.input.SingleLine = true
	u.input.Submit = true
	u.roster.Axis = layout.Vertical
	u.msgList.Axis = layout.Vertical
	// Redraw whenever the hub changes (a client connects, a message arrives, ...).
	u.hub = chat.NewHub(w.Invalidate)
	return u
}

func (u *ui) loop(w *app.Window) error {
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			if u.started {
				u.layoutChat(gtx)
			} else {
				u.layoutSetup(gtx)
			}
			e.Frame(gtx.Ops)
		}
	}
}

// ---- setup screen -----------------------------------------------------------

func (u *ui) layoutSetup(gtx layout.Context) layout.Dimensions {
	if u.startBtn.Clicked(gtx) {
		u.start()
	}
	th := u.th
	return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceEnd}.Layout(gtx,
			layout.Rigid(material.H5(th, "goHTTPS server").Layout),
			layout.Rigid(spacer(16)),
			layout.Rigid(material.Body1(th, "Communication mode:").Layout),
			layout.Rigid(material.RadioButton(th, &u.mode, "https", "HTTPS (long-poll chat)").Layout),
			layout.Rigid(material.RadioButton(th, &u.mode, "socket", "Raw TLS socket").Layout),
			layout.Rigid(spacer(12)),
			layout.Rigid(material.Body1(th, "Port:").Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = gtx.Dp(unit.Dp(160))
				return material.Editor(th, &u.portEd, "5000").Layout(gtx)
			}),
			layout.Rigid(spacer(16)),
			layout.Rigid(material.Button(th, &u.startBtn, "Start server").Layout),
			layout.Rigid(spacer(8)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if u.setupErr == "" {
					return layout.Dimensions{}
				}
				l := material.Body2(th, u.setupErr)
				l.Color = errColor()
				return l.Layout(gtx)
			}),
		)
	})
}

func (u *ui) start() {
	port, err := strconv.Atoi(u.portEd.Text())
	if err != nil || port < 1 || port > 65535 {
		u.setupErr = fmt.Sprintf("%q is not a valid port (1-65535)", u.portEd.Text())
		return
	}
	addr := ":" + strconv.Itoa(port)

	cert, err := certs.GenerateSelfSigned()
	if err != nil {
		u.setupErr = "certificate error: " + err.Error()
		return
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}

	switch u.mode.Value {
	case "https":
		ln, err := tls.Listen("tcp", addr, tlsCfg)
		if err != nil {
			u.setupErr = "listen: " + err.Error()
			return
		}
		srv := &http.Server{Handler: u.hub.HTTPHandler()}
		go func() {
			if err := srv.Serve(ln); err != nil {
				log.Printf("https server: %v", err)
			}
		}()
		u.statusMsg = fmt.Sprintf("HTTPS chat server on https://localhost%s", addr)
	case "socket":
		ln, err := tls.Listen("tcp", addr, tlsCfg)
		if err != nil {
			u.setupErr = "listen: " + err.Error()
			return
		}
		go u.acceptSockets(ln)
		u.statusMsg = fmt.Sprintf("TLS socket chat server on localhost%s", addr)
	}
	u.started = true
}

// acceptSockets handles raw TLS socket clients: read the register frame, add to
// the hub, then pump inbound JSON lines into the hub until the client hangs up.
func (u *ui) acceptSockets(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			return
		}
		go u.handleSocket(conn)
	}
}

func (u *ui) handleSocket(conn net.Conn) {
	scanner := proto.NewLineReader(conn)
	if !scanner.Scan() {
		conn.Close()
		return
	}
	var reg proto.Register
	if err := proto.DecodeLine(scanner.Bytes(), &reg); err != nil || reg.Name == "" {
		reg.Name = "anonymous"
	}
	c := u.hub.AddSocketClient(reg.Name, conn)
	defer u.hub.Remove(c.ID)

	for scanner.Scan() {
		var m proto.Message
		if err := proto.DecodeLine(scanner.Bytes(), &m); err != nil {
			continue
		}
		u.hub.FromClient(c.ID, m.Text)
	}
}

// ---- chat screen ------------------------------------------------------------

func (u *ui) layoutChat(gtx layout.Context) layout.Dimensions {
	th := u.th
	roster := u.hub.Snapshot()

	// Keep one clickable per roster row and resolve selection clicks.
	for len(u.rosterSel) < len(roster) {
		u.rosterSel = append(u.rosterSel, widget.Clickable{})
	}
	for i := range roster {
		if u.rosterSel[i].Clicked(gtx) {
			u.selected = roster[i].ID
		}
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, material.Body2(th, u.statusMsg).Layout)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(240))
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(240))
					return u.layoutRoster(gtx, roster)
				}),
				layout.Flexed(1, u.layoutConversation),
			)
		}),
	)
}

func (u *ui) layoutRoster(gtx layout.Context, roster []chat.ClientView) layout.Dimensions {
	th := u.th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			h := material.Body1(th, fmt.Sprintf("Clients (%d)", len(roster)))
			h.Font.Weight = 700
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, h.Layout)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			lst := material.List(th, &u.roster)
			return lst.Layout(gtx, len(roster), func(gtx layout.Context, i int) layout.Dimensions {
				cv := roster[i]
				return material.Clickable(gtx, &u.rosterSel[i], func(gtx layout.Context) layout.Dimensions {
					label := material.Body1(th, cv.Name)
					if cv.ID == u.selected {
						label.Font.Weight = 700
						label.Color = th.Palette.ContrastBg
					}
					return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx,
						func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(gui.Dot(cv.Online)),
								layout.Rigid(spacerX(8)),
								layout.Flexed(1, label.Layout),
								layout.Rigid(material.Caption(th, statusWord(cv.Online)).Layout),
							)
						})
				})
			})
		}),
	)
}

func (u *ui) layoutConversation(gtx layout.Context) layout.Dimensions {
	th := u.th
	if u.selected == "" {
		return layout.Center.Layout(gtx, material.Body1(th, "Select a client to start chatting").Layout)
	}

	// Send on button click or Enter.
	if u.sendBtn.Clicked(gtx) {
		u.sendToSelected()
	}
	for {
		ev, ok := u.input.Update(gtx)
		if !ok {
			break
		}
		if _, isSubmit := ev.(widget.SubmitEvent); isSubmit {
			u.sendToSelected()
		}
	}

	lines := gui.LinesFromHistory(u.hub.History(u.selected), proto.Admin, "you", u.selectedName())
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return gui.MessageList(gtx, th, &u.msgList, lines)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return gui.InputRow(gtx, th, &u.input, &u.sendBtn, "message "+u.selectedName())
			})
		}),
	)
}

func (u *ui) sendToSelected() {
	text := u.input.Text()
	if text == "" || u.selected == "" {
		return
	}
	if err := u.hub.ToClient(u.selected, text); err != nil {
		u.statusMsg = "send failed: " + err.Error()
		return
	}
	u.input.SetText("")
}

func (u *ui) selectedName() string {
	for _, c := range u.hub.Snapshot() {
		if c.ID == u.selected {
			return c.Name
		}
	}
	return u.selected
}

// ---- small helpers ----------------------------------------------------------

func spacer(dp int) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(0, gtx.Dp(unit.Dp(dp)))}
	}
}

func spacerX(dp int) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(gtx.Dp(unit.Dp(dp)), 0)}
	}
}

func statusWord(online bool) string {
	if online {
		return "online"
	}
	return "offline"
}

func errColor() color.NRGBA { return color.NRGBA{R: 0xcc, G: 0x33, B: 0x33, A: 0xff} }
