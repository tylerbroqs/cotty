package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// The wordmark: "COTT" and "Y" are kept separate so the final letter can
// carry the accent color, like the last letter of the logo.
var bannerLeft = []string{
	" ██████╗ ██████╗ ████████╗████████╗",
	"██╔════╝██╔═══██╗╚══██╔══╝╚══██╔══╝",
	"██║     ██║   ██║   ██║      ██║   ",
	"██║     ██║   ██║   ██║      ██║   ",
	"╚██████╗╚██████╔╝   ██║      ██║   ",
	" ╚═════╝ ╚═════╝    ╚═╝      ╚═╝   ",
}

var bannerRight = []string{
	"██╗   ██╗",
	"╚██╗ ██╔╝",
	" ╚████╔╝ ",
	"  ╚██╔╝  ",
	"   ██║   ",
	"   ╚═╝   ",
}

const (
	ansiReset  = "\x1b[0m"
	ansiWhite  = "\x1b[1;97m"
	ansiAccent = "\x1b[1;95m"
	ansiDim    = "\x1b[2m"
)

// printBanner writes the wordmark to w, colored when w is a terminal.
func printBanner(w *os.File) {
	color := term.IsTerminal(int(w.Fd()))
	for i := range bannerLeft {
		if color {
			fmt.Fprintf(w, "%s%s%s%s%s\n", ansiWhite, bannerLeft[i], ansiAccent, bannerRight[i], ansiReset)
		} else {
			fmt.Fprintf(w, "%s%s\n", bannerLeft[i], bannerRight[i])
		}
	}
	tagline := "          t h e   m u l t i p l a y e r   t e r m i n a l"
	if color {
		fmt.Fprintf(w, "%s%s%s\n\n", ansiDim, tagline, ansiReset)
	} else {
		fmt.Fprintf(w, "%s\n\n", tagline)
	}
}
