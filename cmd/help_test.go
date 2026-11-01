package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelp(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"root", []string{"--help"}, "Commands:"},
		{"short root", []string{"-h"}, "Commands:"},
		{"help command", []string{"help"}, "Commands:"},
		{"init", []string{"init", "--help"}, "Usage: ghrepo init"},
		{"source", []string{"source", "--help"}, "Usage: ghrepo source set"},
		{"source set", []string{"source", "set", "--help"}, "Usage: ghrepo source set"},
		{"repository directory", []string{"--repo-dir", t.TempDir(), "--help"}, "Commands:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := (Runner{Out: &out}).Run(context.Background(), tc.args); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("help output %q does not contain %q", out.String(), tc.want)
			}
		})
	}
}
