package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/asciimoth/ghrepo/inspect"
)

func main() {
	if err := checkPackages(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func checkPackages() error {
	data, err := os.ReadFile("dist/artifacts.json")
	if err != nil {
		return err
	}
	var artifacts []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &artifacts); err != nil {
		return fmt.Errorf("read GoReleaser artifacts: %w", err)
	}
	count := 0
	for _, artifact := range artifacts {
		if artifact.Type != "Linux Package" {
			continue
		}
		if _, _, _, err := inspect.File(artifact.Path, 1<<31-1); err != nil {
			return fmt.Errorf("inspect release package %s: %w", artifact.Name, err)
		}
		count++
	}
	if count == 0 {
		return errors.New("GoReleaser produced no packages")
	}
	fmt.Printf("Checked %d release packages\n", count)
	return nil
}
