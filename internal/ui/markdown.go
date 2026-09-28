package ui

import (
	"fmt"
	"strings"
)

// Markdown renders a model answer for a terminal: headings bold, code in a dim
// block, inline code cyan, links underlined with the URL kept visible.
//
// It handles a useful subset, not CommonMark. The goal is that an agent's
// answer looks like a document rather than like escape codes, and that unknown
// syntax degrades to plain text instead of breaking the layout.
func (p *Printer) Markdown(md string) {
	for _, line := range strings.Split(strings.TrimRight(md, "\n"), "\n") {
		p.Println(p.RenderLine(line))
	}
}

// RenderLine renders one line, tracking fenced-code state across calls via the
// printer's own state is not possible, so callers rendering line-by-line should
// use Render. RenderLine is for single lines only.
func (p *Printer) RenderLine(line string) string {
	trimmed := strings.TrimRight(line, " \t")

	// Fenced code delimiters.
	if strings.HasPrefix(strings.TrimSpace(trimmed), "```") {
		return p.Gray("```")
	}

	// Horizontal rule.
	if t := strings.TrimSpace(trimmed); t == "---" || t == "***" || t == "___" {
		return p.Gray(strings.Repeat("─", min(p.Width, 80)))
	}

	// ATX headings.
	if level, rest, ok := heading(trimmed); ok {
		text := p.inline(rest)
		switch {
		case level <= 1:
			return p.Bold(p.Underline(text))
		case level == 2:
			return p.Bold(text)
		default:
			return p.Bold(p.Cyan(text))
		}
	}

	// Blockquote.
	if t := strings.TrimLeft(trimmed, " "); strings.HasPrefix(t, "> ") || t == ">" {
		return "  " + p.Gray(p.Italic(p.inline(strings.TrimPrefix(strings.TrimPrefix(t, ">"), " "))))
	}

	// Unordered list.
	for _, marker := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(strings.TrimLeft(trimmed, " "), marker) {
			indent := trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, " "))]
			return indent + p.Gray("• ") + p.inline(strings.TrimPrefix(strings.TrimLeft(trimmed, " "), marker))
		}
	}

	// Ordered list.
	if num, rest, ok := ordered(trimmed); ok {
		indent := trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, " "))]
		return indent + p.Gray(fmt.Sprintf("%d. ", num)) + p.inline(rest)
	}

	// Indented block (code inside a list, or a plain preformatted line).
	if strings.HasPrefix(trimmed, "    ") && strings.TrimSpace(trimmed) != "" {
		return p.Gray("  " + strings.TrimSpace(trimmed))
	}

	return p.inline(trimmed)
}

func heading(s string) (int, string, bool) {
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(s) || s[n] != ' ' {
		return 0, "", false
	}
	return n, strings.TrimSpace(s[n:]), true
}

func ordered(s string) (int, string, bool) {
	t := strings.TrimLeft(s, " ")
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(t) || t[i] != '.' || t[i+1] != ' ' {
		return 0, "", false
	}
	n := 0
	for _, c := range t[:i] {
		n = n*10 + int(c-'0')
	}
	return n, strings.TrimSpace(t[i+2:]), true
}

// inline applies span-level formatting: code, bold, italic, links, strikethrough.
func (p *Printer) inline(s string) string {
	if s == "" {
		return ""
	}
	var out strings.Builder
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		c := runes[i]
		switch {
		case c == '`':
			if end, code, ok := matchDelim(runes, i, '`'); ok {
				out.WriteString(p.Cyan(code))
				i = end
				continue
			}
		case c == '*' || c == '_':
			marker := c
			if i+1 < len(runes) && runes[i+1] == marker {
				if end, txt, ok := matchDelim(runes, i, marker, marker); ok {
					out.WriteString(p.Bold(p.inlinePlain(txt)))
					i = end
					continue
				}
			} else if end, txt, ok := matchDelim(runes, i, marker); ok && txt != "" {
				out.WriteString(p.Italic(p.inlinePlain(txt)))
				i = end
				continue
			}
		case c == '~' && i+1 < len(runes) && runes[i+1] == '~':
			if end, txt, ok := matchDelim(runes, i, '~', '~'); ok {
				out.WriteString(p.Gray(p.Strike(txt)))
				i = end
				continue
			}
		case c == '[':
			if end, text, url, ok := matchLink(runes, i); ok {
				out.WriteString(p.Underline(text))
				if url != "" && url != text {
					out.WriteString(p.Gray(" (" + url + ")"))
				}
				i = end
				continue
			}
		}
		out.WriteRune(c)
		i++
	}
	return out.String()
}

// inlinePlain strips markup without styling, for nested spans.
func (p *Printer) inlinePlain(s string) string {
	var out strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '`' || runes[i] == '*' || runes[i] == '_' {
			continue
		}
		out.WriteRune(runes[i])
	}
	return out.String()
}

// Strike is a small helper for strikethrough spans.
func (p *Printer) Strike(s string) string { return p.style(s, "\x1b[9m") }

// matchDelim finds the closing delimiter starting at i, returning the index just
// past it and the text between. The opener itself must be followed by a
// non-space character, so "a * b" is not treated as emphasis.
func matchDelim(runes []rune, i int, delim ...rune) (int, string, bool) {
	j := i + len(delim)
	if j >= len(runes) || runes[j] == ' ' {
		return 0, "", false
	}
	for k := j + 1; k < len(runes); k++ {
		if runes[k] != delim[0] {
			continue
		}
		if len(delim) == 2 {
			if k+1 < len(runes) && runes[k+1] == delim[1] {
				return k + 2, string(runes[j:k]), true
			}
			continue
		}
		if k+1 < len(runes) && runes[k+1] == ' ' {
			continue
		}
		return k + 1, string(runes[j:k]), true
	}
	return 0, "", false
}

func matchLink(runes []rune, i int) (int, string, string, bool) {
	// [text](url)
	depth := 0
	for k := i; k < len(runes); k++ {
		if runes[k] == '[' {
			depth++
		} else if runes[k] == ']' {
			depth--
			if depth == 0 {
				text := string(runes[i+1 : k])
				if k+1 < len(runes) && runes[k+1] == '(' {
					for m := k + 2; m < len(runes); m++ {
						if runes[m] == ')' {
							return m + 1, text, string(runes[k+2 : m]), true
						}
					}
				}
				// Reference-style [text][ref] is not resolved; keep the text.
				return k + 1, text, "", true
			}
		}
	}
	return 0, "", "", false
}

// ---------------------------------------------------------------------------
// Streaming renderer
// ---------------------------------------------------------------------------

// Stream renders markdown as it arrives, one line at a time, so long agent
// runs show readable output while they are still being generated.
type Stream struct {
	p      *Printer
	buf    strings.Builder
	inCode bool
	// Raw disables markdown rendering and echoes tokens verbatim. Used when
	// output is being piped somewhere that does not want styling.
	Raw bool
}

// NewStream starts a streaming renderer.
func (p *Printer) NewStream(raw bool) *Stream { return &Stream{p: p, Raw: raw} }

// Write consumes a chunk of model output. It returns the number of bytes
// accepted so it can be used directly as an io.Writer.
func (s *Stream) Write(b []byte) (int, error) {
	s.buf.Write(b)
	for {
		text := s.buf.String()
		idx := strings.IndexByte(text, '\n')
		if idx < 0 {
			break
		}
		line := text[:idx]
		s.buf.Reset()
		s.buf.WriteString(text[idx+1:])
		s.emitLine(line)
	}
	return len(b), nil
}

// emitLine renders one complete line, tracking fenced code blocks.
func (s *Stream) emitLine(line string) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") {
		s.inCode = !s.inCode
	}
	if s.Raw {
		if s.inCode {
			s.p.Printf("%s\n", s.p.Gray("  "+line))
		} else {
			s.p.Printf("%s\n", line)
		}
		return
	}
	s.p.Println(s.p.RenderLine(line))
}

// Flush renders whatever is left in the buffer. Call it once the provider call
// completes so the final partial line is not lost.
func (s *Stream) Flush() {
	if rest := s.buf.String(); rest != "" {
		s.buf.Reset()
		s.emitLine(rest)
	}
}
