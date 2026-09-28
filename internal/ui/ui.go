// Package ui renders terminal output: colour, tables, markdown and streaming.
//
// Colour is opt-out in three ways, checked in order: NO_COLOR (the informal
// standard), SYNCHRO_COLOR=never|always, and the user's stored config. Nothing
// here writes escape sequences when colour is off, so piping to a file or
// grep produces clean text.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// ANSI styles.
const (
	reset     = "\x1b[0m"
	bold      = "\x1b[1m"
	dim       = "\x1b[2m"
	italic    = "\x1b[3m"
	underline = "\x1b[4m"
	red       = "\x1b[31m"
	green     = "\x1b[32m"
	yellow    = "\x1b[33m"
	blue      = "\x1b[34m"
	magenta   = "\x1b[35m"
	cyan      = "\x1b[36m"
	gray      = "\x1b[90m"
	white     = "\x1b[97m"
)

// Printer writes styled output to a stream.
type Printer struct {
	Out   io.Writer
	Err   io.Writer
	Color bool
	// Width is the render width in columns.
	Width int
	// IsTTY drives cursor-based live output (spinners, streaming).
	IsTTY bool

	mu sync.Mutex
}

// New builds a Printer for stdout/stderr, resolving colour and width from the
// environment and an explicit preference ("auto", "always", "never").
func New(pref string) *Printer {
	tty := term.IsTerminal(int(os.Stdout.Fd()))
	return &Printer{
		Out:   os.Stdout,
		Err:   os.Stderr,
		Color: resolveColor(pref),
		Width: resolveWidth(tty),
		IsTTY: tty,
	}
}

// NewWriter builds a non-interactive Printer around explicit streams. Used by
// tests and by commands whose output is meant to be piped.
func NewWriter(out, errw io.Writer, color bool) *Printer {
	return &Printer{Out: out, Err: errw, Color: color, Width: 100}
}

func resolveColor(pref string) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	switch strings.ToLower(os.Getenv("SYNCHRO_COLOR")) {
	case "never", "off", "0":
		return false
	case "always", "on", "1":
		return true
	}
	switch strings.ToLower(pref) {
	case "always":
		return true
	case "never":
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func resolveWidth(tty bool) int {
	if tty {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 40 {
			return w
		}
	}
	if w := os.Getenv("SYNCHRO_WIDTH"); w != "" {
		var n int
		if _, err := fmt.Sscanf(w, "%d", &n); err == nil && n > 40 {
			return n
		}
	}
	return 100
}

// style wraps s in an ANSI code when colour is enabled.
func (p *Printer) style(s, code string) string {
	if !p.Color || s == "" {
		return s
	}
	return code + s + reset
}

// Semantic styles.
func (p *Printer) Bold(s string) string      { return p.style(s, bold) }
func (p *Printer) Dim(s string) string       { return p.style(s, dim) }
func (p *Printer) Italic(s string) string    { return p.style(s, italic) }
func (p *Printer) Underline(s string) string { return p.style(s, underline) }
func (p *Printer) Red(s string) string       { return p.style(s, red) }
func (p *Printer) Green(s string) string     { return p.style(s, green) }
func (p *Printer) Yellow(s string) string    { return p.style(s, yellow) }
func (p *Printer) Blue(s string) string      { return p.style(s, blue) }
func (p *Printer) Magenta(s string) string   { return p.style(s, magenta) }
func (p *Printer) Cyan(s string) string      { return p.style(s, cyan) }
func (p *Printer) Gray(s string) string      { return p.style(s, gray) }
func (p *Printer) White(s string) string     { return p.style(s, white) }

// Printf writes formatted text to stdout.
func (p *Printer) Printf(format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.Out, format, a...)
}

// Println writes a line to stdout.
func (p *Printer) Println(a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintln(p.Out, a...)
}

// Errf writes formatted text to stderr.
func (p *Printer) Errf(format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.Err, format, a...)
}

// Success prints a check-marked confirmation.
func (p *Printer) Success(format string, a ...any) {
	p.Printf("%s %s\n", p.Green("✓"), fmt.Sprintf(format, a...))
}

// Info prints a dot-prefixed note.
func (p *Printer) Info(format string, a ...any) {
	p.Printf("%s %s\n", p.Blue("•"), fmt.Sprintf(format, a...))
}

// Warn prints a warning.
func (p *Printer) Warn(format string, a ...any) {
	p.Printf("%s %s\n", p.Yellow("!"), fmt.Sprintf(format, a...))
}

// Error prints an error to stderr.
func (p *Printer) Error(format string, a ...any) {
	fmt.Fprintln(p.Err, p.Red("error: ")+fmt.Sprintf(format, a...))
}

// Hint prints a dimmed hint line.
func (p *Printer) Hint(format string, a ...any) {
	p.Printf("%s\n", p.Gray("  "+fmt.Sprintf(format, a...)))
}

// Blank prints an empty line.
func (p *Printer) Blank() { p.Println("") }

// Rule prints a horizontal divider.
func (p *Printer) Rule() {
	p.Printf("%s\n", p.Gray(strings.Repeat("─", min(p.Width, 100))))
}

// Heading prints a section heading.
func (p *Printer) Heading(s string) {
	p.Printf("\n%s\n%s\n", p.Bold(s), p.Gray(strings.Repeat("─", min(len([]rune(s)), p.Width))))
}

// KeyValue prints an aligned "key: value" line.
func (p *Printer) KeyValue(key, value string) {
	p.Printf("  %s %s\n", p.Gray(padRight(key+":", 16)), value)
}

// Title prints a top-of-screen title.
func (p *Printer) Title(s string) {
	p.Printf("\n%s\n", p.Bold(p.Magenta(s)))
}

// Table renders aligned columns. headers is a slice of column titles; rows is a
// slice of slices, each row the same length as headers.
func (p *Printer) Table(headers []string, rows [][]string) {
	if len(headers) == 0 {
		return
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len([]rune(h))
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && len([]rune(c)) > widths[i] {
				widths[i] = len([]rune(c))
			}
		}
	}
	// Header.
	line := ""
	for i, h := range headers {
		line += padRight(h, widths[i])
		if i < len(headers)-1 {
			line += "  "
		}
	}
	p.Printf("  %s\n", p.Bold(line))
	for _, r := range rows {
		out := ""
		for i, c := range r {
			if i < len(r)-1 {
				out += padRight(c, widths[i]) + "  "
			} else {
				out += c
			}
		}
		p.Printf("  %s\n", out)
	}
}

// Box prints a bordered note, used for tips and warnings.
func (p *Printer) Box(title, body string) {
	w := min(p.Width, 100) - 4
	if w < 20 {
		w = 20
	}
	p.Printf("%s\n", p.Gray("╭"+strings.Repeat("─", w+2)+"╮"))
	if title != "" {
		p.Printf("%s %s %s\n", p.Gray("│"), p.Bold(title), p.Gray(strings.Repeat(" ", maxInt(0, w-1-len([]rune(title))))+"│"))
	}
	for _, line := range wrap(body, w) {
		pad := maxInt(0, w-len([]rune(line)))
		p.Printf("%s %s %s%s\n", p.Gray("│"), line, strings.Repeat(" ", pad), p.Gray("│"))
	}
	p.Printf("%s\n", p.Gray("╰"+strings.Repeat("─", w+2)+"╯"))
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func padRight(s string, n int) string {
	d := n - len([]rune(s))
	if d <= 0 {
		return s
	}
	return s + strings.Repeat(" ", d)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// wrap hard-wraps text at n columns on word boundaries.
func wrap(s string, n int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(para)
		line := ""
		for _, w := range words {
			candidate := w
			if line != "" {
				candidate = line + " " + w
			}
			if len([]rune(candidate)) > n && line != "" {
				out = append(out, line)
				line = w
			} else {
				line = candidate
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Spinner
// ---------------------------------------------------------------------------

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner shows a live status line. It is a no-op when stdout is not a
// terminal, so piped output never contains control sequences.
type Spinner struct {
	p       *Printer
	labelMu sync.Mutex
	prefix  string
	label   string
	stop    chan struct{}
	done    chan struct{}
	active  bool
}

// StartSpinner begins an animated status line.
func (p *Printer) StartSpinner(label string) *Spinner {
	s := &Spinner{p: p, label: label, stop: make(chan struct{}), done: make(chan struct{})}
	if !p.IsTTY || !p.Color {
		return s
	}
	s.active = true
	go func() {
		defer close(s.done)
		i := 0
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				p.clearLine()
				return
			case <-t.C:
				frame := spinnerFrames[i%len(spinnerFrames)]
				i++
				s.labelMu.Lock()
				text := s.label
				s.labelMu.Unlock()
				p.mu.Lock()
				fmt.Fprintf(p.Out, "\r%s%s%s", p.Gray(frame), " ", p.Dim(truncate(text, maxInt(10, p.Width-6))))
				p.mu.Unlock()
			}
		}
	}()
	return s
}

// Update changes the spinner text.
func (s *Spinner) Update(label string) {
	s.labelMu.Lock()
	s.label = label
	s.labelMu.Unlock()
}

// Stop halts the animation and clears the line.
func (s *Spinner) Stop() {
	if !s.active {
		return
	}
	close(s.stop)
	<-s.done
	s.active = false
}

func (p *Printer) clearLine() {
	p.mu.Lock()
	fmt.Fprint(p.Out, "\r"+strings.Repeat(" ", p.Width)+"\r")
	p.mu.Unlock()
}

// Truncate shortens a string to n runes with an ellipsis.
func Truncate(s string, n int) string { return truncate(s, n) }

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
