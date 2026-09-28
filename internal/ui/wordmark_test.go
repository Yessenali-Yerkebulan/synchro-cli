package ui

import (
	"bytes"
	"strings"
	"testing"
)

// bannerAt runs the wordmark with the environment the caller needs and returns
// everything it printed.
func bannerAt(t *testing.T, width int, color bool, env map[string]string) string {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	var out bytes.Buffer
	p := NewWriter(&out, &out, color)
	p.Width = width
	p.Wordmark("v0.1.0  ·  test")
	return out.String()
}

func TestWordmarkQuietWhenPiped(t *testing.T) {
	// The default path: stdout is not a terminal, so the banner stays out of the
	// way of anything reading the output.
	if got := bannerAt(t, 100, true, map[string]string{"SYNCHRO_BANNER": ""}); got != "" {
		t.Fatalf("expected no output when piped, got %q", got)
	}
}

func TestWordmarkNever(t *testing.T) {
	got := bannerAt(t, 100, true, map[string]string{"SYNCHRO_BANNER": "never"})
	if got != "" {
		t.Fatalf("SYNCHRO_BANNER=never printed %q", got)
	}
}

func TestWordmarkAlwaysDrawsTheWord(t *testing.T) {
	got := bannerAt(t, 100, false, map[string]string{"SYNCHRO_BANNER": "always"})
	if !strings.Contains(got, "██████") {
		t.Fatalf("expected block art, got:\n%s", got)
	}
	if !strings.Contains(got, "v0.1.0  ·  test") {
		t.Fatalf("expected subtitle, got:\n%s", got)
	}
	// Five rows of art, one subtitle, plus the blank lines around them.
	if n := strings.Count(strings.TrimRight(got, "\n"), "\n"); n != 6 {
		t.Fatalf("expected 6 newlines in the banner, got %d:\n%s", n, got)
	}
}

func TestWordmarkNoColorWhenDisabled(t *testing.T) {
	got := bannerAt(t, 100, false, map[string]string{"SYNCHRO_BANNER": "always"})
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("colour is off but escape sequences were written:\n%q", got)
	}
}

func TestWordmarkColorsWhenEnabled(t *testing.T) {
	t.Setenv("SYNCHRO_BANNER", "always")
	t.Setenv("COLORTERM", "truecolor")
	got := bannerAt(t, 100, true, map[string]string{})
	if !strings.Contains(got, "\x1b[38;2;") {
		t.Fatalf("expected 24-bit colour, got:\n%q", got)
	}
	// The banner must not rewind colour and leave the rest of the line tinted.
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "\x1b[0m") {
		t.Fatalf("banner does not end with a reset:\n%q", got)
	}
}

func TestWordmarkFallsBackToNarrowForm(t *testing.T) {
	got := bannerAt(t, 20, false, map[string]string{"SYNCHRO_BANNER": "always"})
	if !strings.Contains(got, "S Y N C H R O") {
		t.Fatalf("expected the compact form, got:\n%s", got)
	}
	if strings.Contains(got, "██████") {
		t.Fatalf("compact terminal still drew the full art:\n%s", got)
	}
}

func TestWordmarkHasNoTrailingWhitespace(t *testing.T) {
	for _, colour := range []bool{false, true} {
		got := bannerAt(t, 100, colour, map[string]string{"SYNCHRO_BANNER": "always"})
		for _, line := range strings.Split(got, "\n") {
			if line != strings.TrimRight(line, " ") {
				t.Fatalf("line has trailing whitespace (colour=%v): %q", colour, line)
			}
		}
	}
}

// The colour at the left edge should be violet-ish and the colour at the right
// edge green-ish, which is what makes the word sweep through the palette.
func TestGradientRunsVioletToGreen(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	if got := resolveColorModel(); got != modelTrue {
		t.Fatalf("expected truecolor, got %v", got)
	}
	left := shade(wordmarkStops, 0)
	right := shade(wordmarkStops, 1)
	if left.b <= left.g {
		t.Errorf("left stop %v is not violet", left)
	}
	if right.g <= right.r || right.g <= right.b {
		t.Errorf("right stop %v is not green", right)
	}
	mid := shade(wordmarkStops, 0.5)
	if mid.r < 200 || mid.g < 150 || mid.b > 90 {
		t.Errorf("middle stop %v is not yellow", mid)
	}
}

func TestShadeClampsOutOfRange(t *testing.T) {
	if got := shade(wordmarkStops, -1); got != wordmarkStops[0] {
		t.Errorf("t=-1 gave %v, want the first stop", got)
	}
	if got := shade(wordmarkStops, 2); got != wordmarkStops[len(wordmarkStops)-1] {
		t.Errorf("t=2 gave %v, want the last stop", got)
	}
	if got := shade(wordmarkStops, 0); got != wordmarkStops[0] {
		t.Errorf("t=0 gave %v, want the first stop", got)
	}
}

func TestColorModelDetection(t *testing.T) {
	cases := []struct {
		colorterm string
		term      string
		want      colorModel
	}{
		{"truecolor", "xterm-256color", modelTrue},
		{"24bit", "", modelTrue},
		{"", "xterm-256color", model256},
		{"", "screen-256color", model256},
		{"", "xterm", modelBasic},
		{"", "", modelBasic},
	}
	for _, c := range cases {
		t.Setenv("COLORTERM", c.colorterm)
		t.Setenv("TERM", c.term)
		if got := resolveColorModel(); got != c.want {
			t.Errorf("COLORTERM=%q TERM=%q gave %v, want %v", c.colorterm, c.term, got, c.want)
		}
	}
}

// Every model has to produce something the terminal will accept, and basic mode
// has to stay inside the classic 8 so old terminals are not fed a shade they
// cannot draw.
func TestEveryModelEmitsItsOwnSyntax(t *testing.T) {
	c := rgb{139, 92, 246}
	if got := c.code(modelTrue); !strings.HasPrefix(got, "\x1b[38;2;") {
		t.Errorf("truecolor code looks wrong: %q", got)
	}
	if got := c.code(model256); !strings.HasPrefix(got, "\x1b[38;5;") {
		t.Errorf("256 code looks wrong: %q", got)
	}
	if got := c.code(modelBasic); !strings.HasPrefix(got, "\x1b[3") {
		t.Errorf("basic code looks wrong: %q", got)
	}
}

func TestCubeIndexGreysUseTheGreyRamp(t *testing.T) {
	// A near-white must not land in the colour cube, where it would come out
	// green and read as a rendering bug. The grey ramp is 232-255; the cube is
	// 16-231.
	if got := cubeIndex(rgb{240, 240, 240}); got < 232 {
		t.Errorf("white mapped to %d, want the grey ramp (232-255)", got)
	}
	if got := cubeIndex(rgb{128, 128, 128}); got < 232 {
		t.Errorf("grey mapped to %d, want the grey ramp (232-255)", got)
	}
	if got := cubeIndex(rgb{0, 0, 0}); got != 16 {
		t.Errorf("black mapped to %d, want 16", got)
	}
	if got := cubeIndex(rgb{255, 255, 255}); got != 231 {
		t.Errorf("pure white mapped to %d, want 231", got)
	}
	if got := cubeIndex(rgb{139, 92, 246}); got < 16 || got > 231 {
		t.Errorf("violet mapped outside the cube: %d", got)
	}
}
