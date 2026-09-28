package ui

import (
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

// wordmarkLetters is how many glyphs the word has. The art lays them out in
// equal slots so a cell can be mapped back to its letter.
const wordmarkLetters = 7

// wordmarkHues is one neon colour per letter: violet, indigo, azure, sky, cyan,
// spring green, green.
//
// One flat colour per letter, not a colour per column. A column gradient drags
// every shade through the middle of a single glyph, so each letter comes out
// looking smeared and the word reads as noise instead of as seven decisions.
//
// The ramp stays on the cool side of the wheel. Violet, yellow and green cannot
// all sit next to each other on the hue circle -- between violet and yellow lies
// red, and a red band across the middle of a logo is the tasteless part, not a
// gradient. Cyan and green are the neighbours that stay neon without it.
var wordmarkHues = []rgb{
	{r: 167, g: 139, b: 250}, // violet
	{r: 129, g: 140, b: 248}, // indigo
	{r: 96, g: 165, b: 250},  // azure
	{r: 56, g: 189, b: 248},  // sky
	{r: 34, g: 211, b: 238},  // cyan
	{r: 52, g: 211, b: 153},  // spring green
	{r: 74, g: 222, b: 128},  // green
}

// wordmarkLight dims each row from the top down. Painting the top row brightest
// and letting the bottom sink back is what gives flat block letters the sense of
// being lit from above, which is most of what separates "neon sign" from
// "coloured text". Without it, five rows of the same hue read as a smear.
var wordmarkLight = []float64{1.18, 1.06, 1.0, 0.9, 0.78}

// tintRow paints one row of the wordmark, giving every cell the colour of the
// letter it belongs to, scaled by that row's light level. Spaces are copied
// through untouched and consecutive cells sharing an escape sequence are
// emitted as one styled run, which on a 256-colour or 8-colour terminal is the
// difference between ~2 KB of banner and ~6 KB of it.
func tintRow(p *Printer, line string, stride, row int) string {
	if !p.Color {
		return line
	}
	model := resolveColorModel()

	light := 1.0
	if row >= 0 && row < len(wordmarkLight) {
		light = wordmarkLight[row]
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
		letter := col / stride
		if letter >= len(wordmarkHues) {
			letter = len(wordmarkHues) - 1
		}
		if code := lit(wordmarkHues[letter], light).code(model); code != cur {
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

	// One slot per letter: the glyphs are six columns wide with a one-column
	// gap, so a cell at column N belongs to letter N/(glyph+gap).
	stride := (cols + 1) / wordmarkLetters
	if stride < 1 {
		stride = 1
	}

	if p.Width >= wordmarkWidth+2 {
		p.Println()
		for i, line := range rows {
			// Pad to a common width so every row has the same letters in the same
			// columns; otherwise the colours shear between rows and the word
			// falls apart. The padding is trimmed again after tinting so nothing
			// trailing lands in a diff or a copied selection.
			if pad := cols - len([]rune(line)); pad > 0 {
				line += strings.Repeat(" ", pad)
			}
			p.Printf("  %s\n", strings.TrimRight(tintRow(p, line, stride, i), " "))
		}
	} else {
		// Too narrow for the art, but a spaced-out word still reads as a logo.
		p.Printf("\n  %s\n", p.style("S Y N C H R O", wordmarkHues[0].code(resolveColorModel())))
	}
	if subtitle != "" {
		p.Printf("  %s\n", p.Gray(subtitle))
	}
	p.Println()
}
