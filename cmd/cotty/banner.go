package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// The wordmark, in ANSI-shadow style. Rows shade from light to deep
// orange, top to bottom, like the logo.
var banner = []string{
	" ██████╗  ██████╗ ████████╗████████╗██╗   ██╗",
	"██╔════╝ ██╔═══██╗╚══██╔══╝╚══██╔══╝╚██╗ ██╔╝",
	"██║      ██║   ██║   ██║      ██║    ╚████╔╝ ",
	"██║      ██║   ██║   ██║      ██║     ╚██╔╝  ",
	"╚██████╗ ╚██████╔╝   ██║      ██║      ██║   ",
	" ╚═════╝  ╚═════╝    ╚═╝      ╚═╝      ╚═╝   ",
}

// bannerShades maps each banner row to a 256-color orange, light to deep.
var bannerShades = []string{
	"\x1b[1;38;5;214m",
	"\x1b[1;38;5;214m",
	"\x1b[1;38;5;208m",
	"\x1b[1;38;5;208m",
	"\x1b[1;38;5;202m",
	"\x1b[1;38;5;202m",
}

const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
)

// printBanner writes the wordmark to w, colored when w is a terminal.
func printBanner(w *os.File) {
	color := term.IsTerminal(int(w.Fd()))
	for i, line := range banner {
		if color {
			fmt.Fprintf(w, "%s%s%s\n", bannerShades[i], line, ansiReset)
		} else {
			fmt.Fprintln(w, line)
		}
	}
	tagline := "t h e   m u l t i p l a y e r   t e r m i n a l"
	if color {
		fmt.Fprintf(w, "%s%s%s\n\n", ansiDim, tagline, ansiReset)
	} else {
		fmt.Fprintf(w, "%s\n\n", tagline)
	}
}
