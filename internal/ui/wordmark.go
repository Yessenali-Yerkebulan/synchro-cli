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
// The art is 52 columns wide. Narrower terminals get the compact one-line form,
// because wrapped ASCII art looks like a mistake.

const wordmark = ` ███████╗██╗   ██╗███╗   ██╗ ██████╗ ██╗   ██╗ ██████╗  ██████╗
 ██╔════╝██║   ██║████╗  ██║██╔═══██╗██║   ██║██╔══██╗██╔═══██╗
 █████╗  ██║   ██║██╔██╗ ██║██║   ██║███████║██████╔╝██║   ██║
 ██╔══╝  ██║   ██║██║╚██╗██║██║   ██║╚════██║██╔══██╗██║   ██║
 ███████╗╚██████╔╝██║ ╚████║╚██████╔╝     ██║██║  ██║╚██████╔╝
 ╚══════╝ ╚═════╝ ╚═╝  ╚═══╝ ╚═════╝      ╚═╝╚═╝  ╚═╝ ╚═════╝`

// wordmarkWidth is the rendered width of the art above, in columns.
const wordmarkWidth = 52

// wordmarkGradient tints the art line by line, fading from magenta at the top to
// cyan at the bottom rather than printing one flat block of colour. These are
// the raw SGR codes; p.style turns one into an escape sequence, or a no-op when
// colour is off.
var wordmarkGradient = []string{
	magenta, magenta, blue, cyan, cyan, blue,
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

	if p.Width >= wordmarkWidth+2 {
		p.Println()
		for i, line := range strings.Split(wordmark, "\n") {
			if i < len(wordmarkGradient) {
				line = p.style(line, wordmarkGradient[i])
			}
			p.Printf("  %s\n", line)
		}
	} else {
		// Too narrow for the art, but a spaced-out word still reads as a logo.
		p.Printf("\n  %s\n", p.style("S Y N C H R O", magenta))
	}
	if subtitle != "" {
		p.Printf("  %s\n", p.Gray(subtitle))
	}
	p.Println()
}
