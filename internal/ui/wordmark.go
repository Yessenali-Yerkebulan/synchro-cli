package ui

import (
	"math"
	"os"
	"strings"
)

// The wordmark is the one piece of decoration in the tool: a block-letter
// SYNCHRO that shows up when the shell starts and during setup. It is kept as a
// literal rather than rendered from a font table — the word never changes, and
// a table would be hundreds of lines to maintain for the same result.
//
// The letters are upright and separated by a space on purpose. The usual
// ANSI Shadow look for this word is slanted and sets the glyphs flush against
// each other, where "╗██╗" runs together and the name stops being readable at
// a glance — which is the one job a logo has to do.
//
// The art is 50 columns wide including the two-space indent. Narrower terminals
// get the compact one-line form, because wrapped ASCII art looks like a mistake.

const wordmark = `██████ ██  ██ ██  ██  █████ ██  ██ █████   ████
██     ██████ ███ ██ ██     ██  ██ ██  ██  ██  ██
█████   ████  ██████ ██     ██████ █████   ██  ██
    ██   ██   ██ ███ ██     ██  ██ ██ ██   ██  ██
██████   ██   ██  ██  █████ ██  ██ ██  ██   ████ `

// wordmarkWidth is the rendered width of the art above, in columns, including
// the two-space indent the printer adds.
const wordmarkWidth = 50

// wordmarkStops are the colours the wordmark fades between, left to right:
// violet, then yellow through the middle, then green. The middle stop lands on
// the join between the two halves of the word, where the eye reads the gradient
// as a single sweep rather than as two separate fades.
var wordmarkStops = []rgb{
	{r: 139, g: 92, b: 246}, // violet
	{r: 250, g: 204, b: 21}, // yellow
	{r: 52, g: 211, b: 153}, // green
}

// tintSweep paints one row of the wordmark so its colour moves across it left
// to right. Spaces are copied through untouched, and consecutive characters that
// end up with the same escape sequence are emitted as one styled run.
//
// Runs are broken on the resolved code rather than on the quantised step. On a
// 256-colour or 8-colour terminal many neighbouring steps collapse onto the same
// palette entry, and comparing codes is what stops the banner emitting an
// identical escape in front of every single block.
//
// Coloring by column rather than by character is what makes the word read as a
// gradient instead of a stack of flat bands: every row picks the same colour for
// the same column, so the shades line up vertically into stripes.
func tintSweep(p *Printer, line string, cols int) string {
	if !p.Color {
		return line
	}
	model := resolveColorModel()

	// Resolve each step once. Resolving per character would rebuild the same
	// escape sequence a hundred times over for the sake of five rows of art.
	codes := make([]string, gradientStops)
	for i := range codes {
		codes[i] = shade(wordmarkStops, float64(i)/float64(gradientStops-1)).code(model)
	}

	var out, run strings.Builder
	cur := ""
	flush := func() {
		if run.Len() == 0 {
			return
		}
		out.WriteString(cur)
		out.WriteString(run.String())
		out.WriteString(reset)
		run.Reset()
	}
	col := 0
	for _, r := range line {
		if r == ' ' {
			// The gap is written after the pending run and before the next one,
			// so clear the current code to force the break.
			flush()
			cur = ""
			out.WriteRune(' ')
			col++
			continue
		}
		t := 0.0
		if cols > 1 {
			t = float64(col) / float64(cols-1)
		}
		step := int(math.Round(t * float64(gradientStops-1)))
		if code := codes[step]; code != cur {
			flush()
			cur = code
		}
		run.WriteRune(r)
		col++
	}
	flush()
	return out.String()
}

// Wordmark prints the SYNCHRO logo with an optional subtitle underneath.
//
// It prints nothing when stdout is not a terminal, so piping a command into grep
// or a file never gets six lines of art in the middle of the output. A narrow
// terminal gets the compact form instead. Set SYNCHRO_BANNER=always to force the
// art even when piped, or never to suppress it.
func (p *Printer) Wordmark(subtitle string) {
	switch os.Getenv("SYNCHRO_BANNER") {
	case "never":
		return
	case "always":
		// Print even when piped.
	default:
		if !p.IsTTY {
			return
		}
	}

	lines := strings.Split(wordmark, "\n")
	rows := make([]string, len(lines))
	cols := 0
	for i, line := range lines {
		rows[i] = strings.TrimRight(line, " ")
		if n := len([]rune(rows[i])); n > cols {
			cols = n
		}
	}

	if p.Width >= wordmarkWidth+2 {
		p.Println()
		for _, line := range rows {
			// Pad to a common width so column N is the same colour on every row;
			// otherwise the shades shear and the gradient stops looking like one
			// sweep. The padding is trimmed again after tinting so nothing
			// trailing lands in a diff or a copied selection.
			if pad := cols - len([]rune(line)); pad > 0 {
				line += strings.Repeat(" ", pad)
			}
			p.Printf("  %s\n", strings.TrimRight(tintSweep(p, line, cols), " "))
		}
	} else {
		// Too narrow for the art, but a spaced-out word still reads as a logo.
		p.Printf("\n  %s\n", p.style("S Y N C H R O", wordmarkStops[0].code(resolveColorModel())))
	}
	if subtitle != "" {
		p.Printf("  %s\n", p.Gray(subtitle))
	}
	p.Println()
}
