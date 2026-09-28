package ui

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// colorModel is how much colour the terminal can actually show. Interpolating
// between two shades only looks like a gradient if the terminal can express the
// shades in between, so the wordmark asks this first and scales its ambition to
// the answer.
type colorModel int

const (
	modelBasic colorModel = iota // the 8 classic colours
	model256                     // xterm's 256-entry palette
	modelTrue                    // 24-bit RGB
)

// resolveColorModel sniffs terminal capability out of the environment. There is
// no query that works everywhere, so this reads the conventional variables: the
// ones terminals set to advertise themselves, with TERM_PROGRAM covering the
// GUI terminals that leave TERM unset.
func resolveColorModel() colorModel {
	switch strings.ToLower(os.Getenv("COLORTERM")) {
	case "truecolor", "24bit", "24-bit":
		return modelTrue
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "vscode", "Hyper":
		return modelTrue
	}
	term := os.Getenv("TERM")
	switch term {
	case "alacritty", "wezterm", "kitty", "xterm-kitty":
		return modelTrue
	}
	if strings.Contains(term, "256color") {
		return model256
	}
	return modelBasic
}

// rgb is a 24-bit colour.
type rgb struct{ r, g, b uint8 }

// code renders c the way the given terminal can read it.
func (c rgb) code(m colorModel) string {
	switch m {
	case modelTrue:
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.r, c.g, c.b)
	case model256:
		return "\x1b[38;5;" + strconv.Itoa(int(cubeIndex(c))) + "m"
	default:
		return nearestBasic(c)
	}
}

// cubeIndex maps a colour onto xterm's 256-entry palette. The 24-step grey ramp
// is checked first because the 6x6x6 cube has no true greys, and a near-white
// highlight dropped into the cube comes out green.
func cubeIndex(c rgb) uint8 {
	if c.r == c.g && c.g == c.b {
		switch {
		case c.r < 8:
			return 16
		case c.r > 248:
			return 231
		default:
			return uint8(232 + (int(c.r)-8)*24/247)
		}
	}
	level := func(v uint8) int {
		switch {
		case v < 48:
			return 0
		case v < 115:
			return 1
		default:
			return (int(v) - 35) / 40
		}
	}
	return uint8(16 + 36*level(c.r) + 6*level(c.g) + level(c.b))
}

// basicColors is the classic ANSI 8 in both the shade terminals usually show
// and the code that selects it.
var basicColors = []struct {
	rgb  rgb
	code string
}{
	{rgb{0, 0, 0}, "\x1b[30m"},
	{rgb{205, 49, 49}, "\x1b[31m"},
	{rgb{13, 188, 121}, "\x1b[32m"},
	{rgb{229, 229, 16}, "\x1b[33m"},
	{rgb{36, 114, 200}, "\x1b[34m"},
	{rgb{188, 63, 188}, "\x1b[35m"},
	{rgb{17, 168, 205}, "\x1b[36m"},
	{rgb{229, 229, 229}, "\x1b[37m"},
}

// nearestBasic picks the closest classic colour by plain RGB distance. The
// gradient's violet end lands on magenta here: the 8-colour set has no purple of
// its own, and magenta is the least bad answer to "show me violet".
func nearestBasic(c rgb) string {
	best, bestDist := basicColors[0].code, math.MaxInt32
	for _, b := range basicColors {
		dr := int(c.r) - int(b.rgb.r)
		dg := int(c.g) - int(b.rgb.g)
		db := int(c.b) - int(b.rgb.b)
		if d := dr*dr + dg*dg + db*db; d < bestDist {
			best, bestDist = b.code, d
		}
	}
	return best
}

// gradientStops is how many distinct shades a gradient is quantised into. Enough
// that the fade reads as smooth, few enough that a run of adjacent blocks shares
// a single escape sequence instead of carrying one per character.
const gradientStops = 48

// shade samples a multi-stop gradient at t, which runs 0..1 across the stops.
func shade(stops []rgb, t float64) rgb {
	if len(stops) == 0 {
		return rgb{}
	}
	if len(stops) == 1 {
		return stops[0]
	}
	t = math.Max(0, math.Min(1, t))
	at := t * float64(len(stops)-1)
	i := int(at)
	if i >= len(stops)-1 {
		return stops[len(stops)-1]
	}
	f := at - float64(i)
	a, b := stops[i], stops[i+1]
	blend := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x) + (float64(y)-float64(x))*f))
	}
	return rgb{blend(a.r, b.r), blend(a.g, b.g), blend(a.b, b.b)}
}
