package display

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const boxWidth = 63

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cCyan   = "\033[36m"
	cGreen  = "\033[32m"
	cRed    = "\033[31m"
	cYellow = "\033[33m"
)

// Heavy box-drawing (outer frame)
const (
	hTL = "┏"
	hTR = "┓"
	hBL = "┗"
	hBR = "┛"
	hH  = "━"
	hV  = "┃"
	hL  = "┣"
	hR  = "┫"
)

// Light box-drawing (internal dividers)
const (
	sH = "─"
	mL = "┠"
	mR = "┨"
)

// Exported markers for phase results.
const (
	MarkerCheck = "✓"
	MarkerCross = "✗"
)

// Box provides box-drawing UI output to any io.Writer.
type Box struct {
	w       io.Writer
	noColor bool
}

// NewBox creates a Box that writes to w.
func NewBox(w io.Writer, noColor bool) *Box {
	return &Box{w: w, noColor: noColor}
}

func (b *Box) c(code string) string {
	if b.noColor {
		return ""
	}
	return code
}

// writeLine writes content with heavy vertical borders and padding.
// If visible content exceeds inner width, text wraps onto continuation lines.
func (b *Box) writeLine(content string) {
	visible := StripANSI(content)
	maxInner := boxWidth - 2
	visLen := utf8.RuneCountInString(visible)

	if visLen <= maxInner {
		padding := maxInner - visLen
		_, _ = fmt.Fprintf(b.w, "%s%s%s%s%s%s%s%s\n",
			b.c(cDim), hV, b.c(cReset),
			content,
			strings.Repeat(" ", padding),
			b.c(cDim), hV, b.c(cReset))
		return
	}

	wrapAt := maxInner - 1
	first := truncateVisible(content, wrapAt)
	padding := maxInner - wrapAt
	_, _ = fmt.Fprintf(b.w, "%s%s%s%s%s%s%s%s\n",
		b.c(cDim), hV, b.c(cReset),
		first,
		strings.Repeat(" ", padding),
		b.c(cDim), hV, b.c(cReset))

	activeColor := ansiStateAt(content, wrapAt)
	runes := []rune(visible)
	const wrapIndent = 4
	wrapWidth := wrapAt - wrapIndent
	pos := wrapAt
	for pos < len(runes) {
		end := pos + wrapWidth
		if end > len(runes) {
			end = len(runes)
		}
		chunk := string(runes[pos:end])
		line := strings.Repeat(" ", wrapIndent) + b.c(activeColor) + chunk + b.c(cReset)
		padding := maxInner - utf8.RuneCountInString(strings.Repeat(" ", wrapIndent)+chunk)
		_, _ = fmt.Fprintf(b.w, "%s%s%s%s%s%s%s%s\n",
			b.c(cDim), hV, b.c(cReset),
			line,
			strings.Repeat(" ", padding),
			b.c(cDim), hV, b.c(cReset))
		pos = end
	}
}

// Header writes the heavy top border and title.
func (b *Box) Header(title string) {
	top := strings.Repeat(hH, boxWidth-2)
	_, _ = fmt.Fprintf(b.w, "%s%s%s%s%s\n", b.c(cDim), hTL, top, hTR, b.c(cReset))
	b.writeLine(fmt.Sprintf(" %s%s%s%s", b.c(cCyan), b.c(cBold), title, b.c(cReset)))
	sep := strings.Repeat(hH, boxWidth-2)
	_, _ = fmt.Fprintf(b.w, "%s%s%s%s%s\n", b.c(cDim), hL, sep, hR, b.c(cReset))
}

// Footer writes the heavy bottom border.
func (b *Box) Footer() {
	bottom := strings.Repeat(hH, boxWidth-2)
	_, _ = fmt.Fprintf(b.w, "%s%s%s%s%s\n", b.c(cDim), hBL, bottom, hBR, b.c(cReset))
}

// Divider writes a light horizontal separator with heavy-to-light junctions.
func (b *Box) Divider() {
	inner := strings.Repeat(sH, boxWidth-2)
	_, _ = fmt.Fprintf(b.w, "%s%s%s%s%s\n", b.c(cDim), mL, inner, mR, b.c(cReset))
}

// Label writes a bold label line.
func (b *Box) Label(label string) {
	b.writeLine(fmt.Sprintf(" %s%s%s", b.c(cBold), label, b.c(cReset)))
}

// Row writes a key: value pair.
func (b *Box) Row(key, value string) {
	b.writeLine(fmt.Sprintf("  %s: %s%s%s", key, b.c(cCyan), value, b.c(cReset)))
}

// Item writes a marker + text line with color based on marker.
func (b *Box) Item(marker, text string) {
	var color string
	switch marker {
	case MarkerCheck:
		color = cGreen
	case MarkerCross:
		color = cRed
	case "⚠":
		color = cYellow
	}
	if color != "" {
		b.writeLine(fmt.Sprintf("  %s%s%s %s", b.c(color), marker, b.c(cReset), text))
	} else {
		b.writeLine(fmt.Sprintf("  %s %s", marker, text))
	}
}

// Section writes a section header with a light divider above it.
func (b *Box) Section(label string) {
	b.Divider()
	b.writeLine(fmt.Sprintf(" %s%s%s", b.c(cBold), label, b.c(cReset)))
}

// Badge writes a colored [BADGE] message.
func (b *Box) Badge(badge, msg string) {
	var color string
	switch badge {
	case "OK", "SUCCESS", "PASS":
		color = cGreen
	case "ERROR", "FAIL", "FAILED":
		color = cRed
	default:
		color = cYellow
	}
	b.writeLine(fmt.Sprintf("  %s[%s]%s %s", b.c(color), badge, b.c(cReset), msg))
}

// StripANSI removes ANSI escape sequences from s.
func StripANSI(s string) string {
	var out strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// truncateVisible returns a prefix of s whose visible length is exactly n runes.
func truncateVisible(s string, n int) string {
	var out strings.Builder
	visible := 0
	inEscape := false
	hadEscape := false
	for _, r := range s {
		if visible >= n && !inEscape {
			break
		}
		out.WriteRune(r)
		if r == '\033' {
			inEscape = true
			hadEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		visible++
	}
	if hadEscape {
		out.WriteString(cReset)
	}
	return out.String()
}

// ansiStateAt returns the last ANSI color code active at visible position n.
func ansiStateAt(s string, n int) string {
	var lastCode string
	var cur strings.Builder
	visible := 0
	inEscape := false
	for _, r := range s {
		if visible >= n && !inEscape {
			break
		}
		if r == '\033' {
			inEscape = true
			cur.Reset()
			cur.WriteRune(r)
			continue
		}
		if inEscape {
			cur.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
				code := cur.String()
				if code == cReset {
					lastCode = ""
				} else {
					lastCode = code
				}
			}
			continue
		}
		visible++
	}
	return lastCode
}
