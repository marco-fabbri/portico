// Command portico is the Portico appliance binary.
//
//	portico serve -config /etc/portico/portico.yaml
//	portico version
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/marco-fabbri/portico/internal/config"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "portico %s\n", version)
		return 0
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(stderr)
		path := fs.String("config", "/etc/portico/portico.yaml", "path to the settings file")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		cfg, err := config.Load(*path)
		if err != nil {
			fmt.Fprintf(stderr, "portico: %v\n", err)
			return 1
		}
		if _, err := cfg.ClientSecret(); err != nil {
			fmt.Fprintf(stderr, "portico: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "portico: configuration ok")
		return 0
	default:
		usage(stderr)
		return 2
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: portico serve -config PATH | portico version")
}
