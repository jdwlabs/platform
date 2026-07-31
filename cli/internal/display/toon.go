package display

import (
	"fmt"
	"io"
	"regexp"
	"strings"
)

// TOON (Token-Oriented Object Notation) output for agent-facing query
// commands. Only the subset platformctl emits is implemented — scalar lines,
// tabular arrays, and primitive list arrays — because a full encoder would be
// unreachable code that still has to be reviewed and kept correct.
//
// The comma delimiter is the document default, so it is never declared in a
// header; it is instead one of the characters that forces a value to be quoted.

const toonIndent = "  "

// numericLike matches the number forms a decoder would read back as a number
// rather than a string, which is why such values must be quoted.
var numericLike = regexp.MustCompile(`^-?(\d+)(\.\d+)?([eE][+-]?\d+)?$`)

// ToonScalar writes a single `key: value` line.
func ToonScalar(w io.Writer, key, value string) error {
	_, err := fmt.Fprintf(w, "%s: %s\n", ToonQuote(key), ToonQuote(value))
	return err
}

// ToonTable writes a tabular array: a `name[N]{fields}:` header followed by one
// indented comma-delimited row per record. Every row must have one cell per
// field — a ragged row is a caller bug, not something to paper over, because a
// short row silently shifts every later column in the agent's view.
func ToonTable(w io.Writer, name string, fields []string, rows [][]string) error {
	quoted := make([]string, len(fields))
	for i, f := range fields {
		quoted[i] = ToonQuote(f)
	}
	if _, err := fmt.Fprintf(w, "%s[%d]{%s}:\n", ToonQuote(name), len(rows), strings.Join(quoted, ",")); err != nil {
		return err
	}
	for i, row := range rows {
		if len(row) != len(fields) {
			return fmt.Errorf("toon table %q row %d has %d cells, want %d", name, i, len(row), len(fields))
		}
		cells := make([]string, len(row))
		for j, c := range row {
			cells[j] = ToonQuote(c)
		}
		if _, err := fmt.Fprintf(w, "%s%s\n", toonIndent, strings.Join(cells, ",")); err != nil {
			return err
		}
	}
	return nil
}

// ToonList writes a primitive list array, one `- item` line per item.
func ToonList(w io.Writer, name string, items []string) error {
	if _, err := fmt.Fprintf(w, "%s[%d]:\n", ToonQuote(name), len(items)); err != nil {
		return err
	}
	for _, it := range items {
		if _, err := fmt.Fprintf(w, "%s- %s\n", toonIndent, ToonQuote(it)); err != nil {
			return err
		}
	}
	return nil
}

// ToonQuote returns v quoted and escaped when the TOON spec requires it, and
// verbatim otherwise.
func ToonQuote(v string) string {
	if !toonNeedsQuote(v) {
		return v
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func toonNeedsQuote(v string) bool {
	if v == "" || strings.TrimSpace(v) != v {
		return true
	}
	switch v {
	case "true", "false", "null":
		return true
	}
	if numericLike.MatchString(v) {
		return true
	}
	if strings.HasPrefix(v, "-") || strings.HasPrefix(v, "#") {
		return true
	}
	if strings.ContainsAny(v, ",:\"\\[]{}") {
		return true
	}
	for _, r := range v {
		if r < 0x20 {
			return true
		}
	}
	return false
}
