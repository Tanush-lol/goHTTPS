package main

import (
	"image"
	"image/color"
	"log"
	"os"
	"strconv"
	"sync"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"goHTTPS/internal/clientconn"
	"goHTTPS/internal/gui"
)

func main() {
	go func() {
		w := new(app.Window)
		w.Option(app.Title("goHTTPS client"), app.Size(unit.Dp(640), unit.Dp(560)))
		if err := newUI(w).loop(w); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

type ui struct {
	th     *material.Theme
	redraw func()

	// setup screen
	mode      widget.Enum
	hostEd    widget.Editor
	portEd    widget.Editor
	nameEd    widget.Editor
	connectBt widget.Clickable
	setupErr  string

	// chat screen
	conn    clientconn.Conn
	name    string
	msgList widget.List
	input   widget.Editor
	sendBtn widget.Clickable

	
	mu        sync.Mutex
	connected bool
	lines     []gui.Line
}


func (u *ui) addLine(l gui.Line) {
	u.mu.Lock()
	u.lines = append(u.lines, l)
	u.mu.Unlock()
}


func (u *ui) snapshotLines() []gui.Line {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]gui.Line, len(u.lines))
	copy(out, u.lines)
	return out
}

func (u *ui) isConnected() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.connected
}

func newUI(w *app.Window) *ui {
	u := &ui{th: material.NewTheme(), redraw: w.Invalidate}
	u.mode.Value = "https"
	for ed, val := range map[*widget.Editor]string{
		&u.hostEd: "localhost",
		&u.portEd: "5000",
		&u.nameEd: "",
	} {
		ed.SingleLine = true
		ed.SetText(val)
	}
	u.input.SingleLine = true
	u.input.Submit = true
	u.msgList.Axis = layout.Vertical
	return u
}

func (u *ui) loop(w *app.Window) error {
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			if u.conn != nil {
				u.conn.Close()
			}
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			if u.isConnected() {
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
	if u.connectBt.Clicked(gtx) {
		u.connect()
	}
	th := u.th
	row := func(label string, ed *widget.Editor, hint string) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(material.Body2(th, label).Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(280))
					return material.Editor(th, ed, hint).Layout(gtx)
				}),
				layout.Rigid(spacer(10)),
			)
		})
	}
	return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(material.H5(th, "goHTTPS client").Layout),
			layout.Rigid(spacer(16)),
			layout.Rigid(material.Body1(th, "Communication mode:").Layout),
			layout.Rigid(material.RadioButton(th, &u.mode, "https", "HTTPS (long-poll chat)").Layout),
			layout.Rigid(material.RadioButton(th, &u.mode, "socket", "Raw TLS socket").Layout),
			layout.Rigid(spacer(12)),
			row("Server host", &u.hostEd, "localhost"),
			row("Server port", &u.portEd, "5000"),
			row("Display name", &u.nameEd, "your name"),
			layout.Rigid(material.Button(th, &u.connectBt, "Connect").Layout),
			layout.Rigid(spacer(8)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if u.setupErr == "" {
					return layout.Dimensions{}
				}
				l := material.Body2(th, u.setupErr)
				l.Color = color.NRGBA{R: 0xcc, G: 0x33, B: 0x33, A: 0xff}
				return l.Layout(gtx)
			}),
		)
	})
}

func (u *ui) connect() {
	port, err := strconv.Atoi(u.portEd.Text())
	if err != nil || port < 1 || port > 65535 {
		u.setupErr = u.portEd.Text() + " is not a valid port (1-65535)"
		return
	}
	name := u.nameEd.Text()
	if name == "" {
		name = "anonymous"
	}
	addr := u.hostEd.Text() + ":" + strconv.Itoa(port)

	var conn clientconn.Conn
	switch u.mode.Value {
	case "https":
		conn, err = clientconn.DialHTTPS(addr, name)
	case "socket":
		conn, err = clientconn.DialSocket(addr, name)
	}
	if err != nil {
		u.setupErr = "connect failed: " + err.Error()
		return
	}

	u.conn = conn
	u.name = name
	u.mu.Lock()
	u.connected = true
	u.mu.Unlock()
	go u.recvLoop()
}

// recvLoop feeds incoming admin messages into the view and notices disconnects.
func (u *ui) recvLoop() {
	in := u.conn.Incoming()
	done := u.conn.Done()
	for {
		select {
		case m, ok := <-in:
			if !ok {
				in = nil
				continue
			}
			u.addLine(gui.Line{Who: "admin", Text: m.Text})
			u.redraw()
		case <-done:
			u.mu.Lock()
			u.connected = false 
			u.mu.Unlock()
			u.redraw()
			return
		}
	}
}

// ---- chat screen ------------------------------------------------------------

func (u *ui) layoutChat(gtx layout.Context) layout.Dimensions {
	th := u.th

	if u.sendBtn.Clicked(gtx) {
		u.send()
	}
	for {
		ev, ok := u.input.Update(gtx)
		if !ok {
			break
		}
		if _, isSubmit := ev.(widget.SubmitEvent); isSubmit {
			u.send()
		}
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(gui.Dot(true)),
					layout.Rigid(spacerX(8)),
					layout.Rigid(material.Body2(th, "connected as "+u.name+" — chatting with admin").Layout),
				)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return gui.MessageList(gtx, th, &u.msgList, u.snapshotLines())
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return gui.InputRow(gtx, th, &u.input, &u.sendBtn, "message admin")
			})
		}),
	)
}

func (u *ui) send() {
	text := u.input.Text()
	if text == "" || u.conn == nil {
		return
	}
	if err := u.conn.Send(text); err != nil {
		u.addLine(gui.Line{Who: "system", Text: "send failed: " + err.Error()})
		return
	}
	u.addLine(gui.Line{Who: "you", Text: text, Mine: true})
	u.input.SetText("")
}


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
