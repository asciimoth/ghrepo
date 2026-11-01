package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/asciimoth/ghrepo/contract"
)

type metadataOptions struct {
	comment, sourceURL, tags string
	set                      map[string]bool
}

var metadataTagRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

func (o *metadataOptions) flags(fs *flag.FlagSet) {
	fs.StringVar(&o.comment, "comment", "", "package comment")
	fs.StringVar(&o.sourceURL, "source-url", "", "package source URL")
	fs.StringVar(&o.tags, "tags", "", "comma-separated tags")
}

func (o *metadataOptions) mark(fs *flag.FlagSet) {
	o.set = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { o.set[f.Name] = true })
}

func (o metadataOptions) apply(p *contract.Package) {
	if o.set["comment"] {
		p.Comment = o.comment
	}
	if o.set["source-url"] {
		p.SourceURL = o.sourceURL
	}
	if o.set["tags"] {
		p.Tags = nil
		if o.tags != "" {
			p.Tags = strings.Split(o.tags, ",")
		}
	}
}

func (c Runner) metadataSet(args []string) error {
	fs := flag.NewFlagSet("metadata set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var options metadataOptions
	options.flags(fs)
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return errors.New("usage: ghrepo metadata set [--comment TEXT] [--source-url URL] [--tags TAGS] SELECTOR")
	}
	options.mark(fs)
	if len(options.set) == 0 {
		return errors.New("metadata set requires at least one field")
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	i, err := selectOne(m, fs.Arg(0))
	if err != nil {
		return err
	}
	options.apply(&m.Packages[i])
	if err := m.Validate(r); err != nil {
		return err
	}
	if c.dryRun {
		return c.preview("would update metadata for %s", m.Packages[i].Path)
	}
	return c.save(m, r)
}

func githubSourceURL(raw string) (string, releaseSource, bool) {
	source, ok := parseReleaseSource(raw)
	if !ok {
		return "", releaseSource{}, false
	}
	return "https://github.com/" + source.owner + "/" + source.repo, source, true
}

func (c Runner) enrichGitHub(ctx context.Context, p *contract.Package) {
	repoURL, source, ok := githubSourceURL(p.URL)
	if !ok {
		return
	}
	if p.SourceURL == "" {
		p.SourceURL = repoURL
	}
	if p.Comment != "" && len(p.Tags) != 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	base := c.GitHubAPIURL
	if base == "" {
		base = "https://api.github.com"
	}
	endpoint := strings.TrimRight(base, "/") + "/repos/" + url.PathEscape(source.owner) + "/" + url.PathEscape(source.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		c.reportMetadataFailure(repoURL, "invalid request")
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := os.Getenv("GH_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{Timeout: 3 * time.Second}
	if c.GitHubClient != nil {
		client = *c.GitHubClient
		client.Timeout = 3 * time.Second
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return errors.New("redirect not allowed") }
	resp, err := client.Do(req)
	if err != nil {
		c.reportMetadataFailure(repoURL, "request failed")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		c.reportMetadataFailure(repoURL, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return
	}
	var info struct {
		Description string   `json:"description"`
		Topics      []string `json:"topics"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		c.reportMetadataFailure(repoURL, "invalid response")
		return
	}
	if p.Comment == "" {
		if len(info.Description) <= 4096 && strings.IndexFunc(info.Description, unicode.IsControl) < 0 {
			p.Comment = info.Description
		} else {
			c.reportMetadataFailure(repoURL, "invalid description")
		}
	}
	if len(p.Tags) == 0 {
		valid := len(info.Topics) <= 32
		seen := map[string]bool{}
		for _, tag := range info.Topics {
			if len(tag) > 64 || !metadataTagRE.MatchString(tag) || strings.Contains(tag, "..") || seen[tag] {
				valid = false
				break
			}
			seen[tag] = true
		}
		if valid {
			p.Tags = info.Topics
		} else {
			c.reportMetadataFailure(repoURL, "invalid topics")
		}
	}
}

func (c Runner) reportMetadataFailure(repoURL, reason string) {
	_, _ = fmt.Fprintf(c.output(), "warning: could not fetch metadata for %s: %s\n", repoURL, reason)
}
