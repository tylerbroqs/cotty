package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// The wordmark, in ANSI-shadow style. The solid █ blocks are the letter
// bodies; the box-drawing characters are their drop shadow.
var banner = []string{
	" ██████╗  ██████╗ ████████╗████████╗██╗   ██╗",
	"██╔════╝ ██╔═══██╗╚══██╔══╝╚══██╔══╝╚██╗ ██╔╝",
	"██║      ██║   ██║   ██║      ██║    ╚████╔╝ ",
	"██║      ██║   ██║   ██║      ██║     ╚██╔╝  ",
	"╚██████╗ ╚██████╔╝   ██║      ██║      ██║   ",
	" ╚═════╝  ╚═════╝    ╚═╝      ╚═╝      ╚═╝   ",
}

// Letter bodies are gold on the top half and orange on the bottom, with
// the shadow in a dark burnt orange — the retro two-tone of the logo.
const (
	bannerTop    = "\x1b[1;38;5;220m" // gold
	bannerBottom = "\x1b[1;38;5;208m" // orange
	bannerShadow = "\x1b[38;5;130m"   // burnt orange
	ansiReset    = "\x1b[0m"
	ansiDim      = "\x1b[2m"
)

// printBanner writes the wordmark to w, colored when w is a terminal:
// solid blocks carry the letter color for their half, everything else is
// shadow.
func printBanner(w *os.File) {
	color := term.IsTerminal(int(w.Fd()))
	for i, line := range banner {
		if !color {
			fmt.Fprintln(w, line)
			continue
		}
		fill := bannerTop
		if i >= len(banner)/2 {
			fill = bannerBottom
		}
		var b strings.Builder
		cur := ""
		for _, r := range line {
			want := cur
			switch {
			case r == '█':
				want = fill
			case r != ' ':
				want = bannerShadow
			}
			if want != cur {
				b.WriteString(want)
				cur = want
			}
			b.WriteRune(r)
		}
		b.WriteString(ansiReset)
		fmt.Fprintln(w, b.String())
	}
	tagline := "t h e   m u l t i p l a y e r   t e r m i n a l"
	if color {
		fmt.Fprintf(w, "%s%s%s\n\n", ansiDim, tagline, ansiReset)
	} else {
		fmt.Fprintf(w, "%s\n\n", tagline)
	}
}
