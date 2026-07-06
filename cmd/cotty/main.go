// Command cotty is a multiplayer terminal: host your shell, let others
// join over a websocket, watch together, type together.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/tylerbroqs/cotty/internal/client"
	"github.com/tylerbroqs/cotty/internal/host"
)

const version = "0.1.0-dev"

const usage = `cotty — the multiplayer terminal

Usage:
  cotty host [flags]      Share your shell with guests
  cotty join <url>        Join a hosted session
  cotty version           Print version

Host flags:
  -addr string    Listen address for guests (default ":7373")
  -shell string   Shell to run (default $SHELL, then /bin/sh)
  -write          Allow guests to type into the session (default view-only)
  -code string    Use a fixed session code instead of a random one

Join:
  The host prints the URL to use, e.g.
    cotty join "ws://192.168.1.10:7373/ws?code=XJ4K2P"
  Press Ctrl-] to leave a session.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "host":
		printBanner(os.Stderr)
		fs := flag.NewFlagSet("host", flag.ExitOnError)
		addr := fs.String("addr", ":7373", "listen address for guests")
		shell := fs.String("shell", "", "shell to run (default $SHELL)")
		write := fs.Bool("write", false, "allow guests to type")
		code := fs.String("code", "", "fixed session code")
		fs.Parse(os.Args[2:])
		err := host.Run(host.Options{
			Addr:       *addr,
			Shell:      *shell,
			AllowWrite: *write,
			Code:       *code,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "cotty: %v\n", err)
			os.Exit(1)
		}
	case "join":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: cotty join <url>")
			os.Exit(2)
		}
		if err := client.Run(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "cotty: %v\n", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println("cotty " + version)
	case "-h", "--help", "help":
		printBanner(os.Stderr)
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "cotty: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
