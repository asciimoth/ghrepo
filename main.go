package main

import (
	"context"
	"fmt"
	"os"

	"github.com/asciimoth/ghrepo/cmd"
)

func main() {
	if err := (cmd.Runner{}).Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ghrepo:", err)
		os.Exit(1)
	}
}
