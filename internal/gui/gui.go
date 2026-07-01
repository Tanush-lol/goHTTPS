// Package gui holds small Gio helpers shared by the server and client windows:
// a vertical message list and a "type a line + Send" input row. Keeping them
// here avoids duplicating layout code between the two commands.
package gui

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"goHTTPS/internal/proto"
)

// Line is one rendered chat line: who said it and the text.
type Line struct {
	Who  string // display label, e.g. "admin", "you", a client name
	Text string
	Mine bool // true if sent by the local side (styled differently)
}

// LinesFromHistory converts a server/client transcript into renderable lines.
// localID is the id whose messages should be labelled as "mine"; meLabel/other
// are the labels to show for the two directions.
func LinesFromHistory(msgs []proto.Message, localID, meLabel, otherLabel string) []Line {
	out := make([]Line, 0, len(msgs))
	for _, m := range msgs {
		if m.From == localID {
			out = append(out, Line{Who: meLabel, Text: m.Text, Mine: true})
		} else {
			out = append(out, Line{Who: otherLabel, Text: m.Text})
		}
	}
	return out
}

// MessageList lays out lines top-to-bottom in a scrollable list.
func MessageList(gtx layout.Context, th *material.Theme, st *widget.List, lines []Line) layout.Dimensions {
	lst := material.List(th, st)
	return lst.Layout(gtx, len(lines), func(gtx layout.Context, i int) layout.Dimensions {
		ln := lines[i]
		who := material.Body2(th, ln.Who+":")
		who.Font.Weight = 700
		if ln.Mine {
			who.Color = th.Palette.ContrastBg
		}
		body := material.Body1(th, ln.Text)
		return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(who.Layout),
					layout.Rigid(body.Layout),
				)
			})
	})
}

// InputRow lays out a single-line editor and a Send button side by side.
func InputRow(gtx layout.Context, th *material.Theme, ed *widget.Editor, btn *widget.Clickable, hint string) layout.Dimensions {
	editor := material.Editor(th, ed, hint)
	send := material.Button(th, btn, "Send")
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: unit.Dp(8)}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions {
					return widget.Border{
						Color:        color.NRGBA{A: 0x60},
						Width:        unit.Dp(1),
						CornerRadius: unit.Dp(4),
					}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.UniformInset(unit.Dp(6)).Layout(gtx, editor.Layout)
					})
				})
		}),
		layout.Rigid(send.Layout),
	)
}

// Dot returns a small colored status indicator widget (green=online, grey=offline).
func Dot(online bool) layout.Widget {
	c := color.NRGBA{R: 0x99, G: 0x99, B: 0x99, A: 0xff} // grey
	if online {
		c = color.NRGBA{R: 0x2e, G: 0xb8, B: 0x4c, A: 0xff} // green
	}
	return func(gtx layout.Context) layout.Dimensions {
		sz := gtx.Dp(unit.Dp(10))
		rect := image.Rect(0, 0, sz, sz)
		paint.FillShape(gtx.Ops, c, clip.Ellipse(rect).Op(gtx.Ops))
		return layout.Dimensions{Size: image.Pt(sz, sz)}
	}
}
