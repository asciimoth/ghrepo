package cmd

import (
	"errors"
	"fmt"
)

const rootHelp = `Usage: ghrepo [--repo-dir DIR] COMMAND [OPTIONS]

Commands:
  init          Create or update repository configuration
  add           Add packages by URL or upload local files
  update        Add newer packages from GitHub releases
  replace       Replace a package and retain its old path
  sync          Download and verify packages
  source set    Change a package source URL
  metadata set  Change a package comment, source URL, or tags
  remove        Mark packages as retained
  prune         List or remove eligible retained packages
  rebuild       Build signed repository metadata
  check         Check repository data and the rendered site
  publish       Build, check, commit, and push changes
  gc            List unreferenced GitHub release assets
  site-build    Build the Pages site
  readme-build  Render the repository README

Global options:
  --repo-dir DIR  Select the repository directory (also: GHREPO_REPO_DIR)
  --dry-run       Preview a command without changes
  -h, --help     Show help for the command

Run "ghrepo COMMAND --help" for command usage.
`

var commandHelp = map[string]string{
	"init":         "Usage: ghrepo init [DIR] [--repo USER/REPO] [--site URL] [--branch NAME] [--source-hosts HOSTS] [--redirect-hosts HOSTS] [--key-id ID] [--create-remote] [--configure-pages] [--dry-run]",
	"add":          "Usage: ghrepo add [--comment TEXT] [--source-url URL] [--tags TAGS] [--dry-run] URL...\n       ghrepo add --upload [--sign-rpm] [--comment TEXT] [--source-url URL] [--tags TAGS] [--dry-run] FILE...",
	"update":       "Usage: ghrepo update [--dry-run]",
	"replace":      "Usage: ghrepo replace [--dry-run] SELECTOR URL",
	"sync":         "Usage: ghrepo sync [--dry-run]",
	"source set":   "Usage: ghrepo source set [--dry-run] SELECTOR URL",
	"metadata set": "Usage: ghrepo metadata set [--comment TEXT] [--source-url URL] [--tags TAGS] [--dry-run] SELECTOR",
	"remove":       "Usage: ghrepo remove [--dry-run] SELECTOR...",
	"prune":        "Usage: ghrepo prune [--apply] [--dry-run]",
	"rebuild":      "Usage: ghrepo rebuild [--dry-run]",
	"check":        "Usage: ghrepo check [--dry-run]",
	"publish":      "Usage: ghrepo publish [--dry-run] [--message TEXT]",
	"gc":           "Usage: ghrepo gc",
	"site-build":   "Usage: ghrepo site-build [--dry-run]",
	"readme-build": "Usage: ghrepo readme-build [--dry-run]",
}

func helpCommand(args []string) (string, bool) {
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		if len(args) == 1 {
			return "", true
		}
		if args[1] == "source" {
			return "source set", true
		}
		if args[1] == "metadata" {
			return "metadata set", true
		}
		return args[1], true
	}
	for _, arg := range args[1:] {
		if arg == "--help" || arg == "-h" {
			if args[0] == "source" {
				return "source set", true
			}
			if args[0] == "metadata" {
				return "metadata set", true
			}
			return args[0], true
		}
	}
	return "", false
}

func (c Runner) printHelp(command string) error {
	if command == "" {
		_, err := fmt.Fprint(c.output(), rootHelp)
		return err
	}
	usage, ok := commandHelp[command]
	if !ok {
		return errors.New("unknown command")
	}
	_, err := fmt.Fprintln(c.output(), usage)
	return err
}
