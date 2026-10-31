package contract

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	componentRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	versionRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~:^-]*$`)
	hostRE      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	hashRE      = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func safeComponent(s string, version bool) bool {
	if s == "" || strings.Contains(s, "..") {
		return false
	}
	if version {
		return versionRE.MatchString(s)
	}
	return componentRE.MatchString(s)
}

func validHost(s string) bool {
	if !hostRE.MatchString(s) || net.ParseIP(s) != nil || len(s) > 253 || s == "localhost" || strings.HasSuffix(s, ".localhost") || strings.HasSuffix(s, ".local") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return true
}

func validateHosts(hosts []string) error {
	seen := map[string]bool{}
	for _, h := range hosts {
		if !validHost(h) || seen[h] {
			return errors.New("invalid or duplicate download host")
		}
		seen[h] = true
	}
	return nil
}

// ValidateSourceURL checks a public URL without including it in errors.
// A redirect uses the union of source and redirect host allowlists.
func ValidateSourceURL(raw string, hosts []string) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || strings.Contains(raw, "#") || u.Host == "" || u.Port() != "" || u.Host != u.Hostname() || !validHost(u.Hostname()) {
		return errors.New("invalid HTTPS source URL")
	}
	allowed := false
	for _, h := range hosts {
		allowed = allowed || u.Hostname() == h
	}
	if !allowed {
		return errors.New("source host is not allowed")
	}
	if u.RawQuery != "" || strings.Contains(raw, "?") {
		return errors.New("source URL query parameters are not supported")
	}
	if strings.ContainsAny(raw, "\r\n\t") || u.Path == "" {
		return errors.New("invalid HTTPS source URL")
	}
	return nil
}

func (r Repository) Validate() error {
	if r.Schema != 1 || r.Repository.Layout != "flat-v1" || !safeComponent(r.Repository.Branch, false) {
		return errors.New("invalid repository schema, layout, or branch")
	}
	if err := validateHTTPSBase(r.Repository.URL); err != nil {
		return fmt.Errorf("repository URL: %w", err)
	}
	if err := validateHTTPSBase(r.Site.BaseURL); err != nil {
		return fmt.Errorf("site base URL: %w", err)
	}
	if !safeComponent(r.APT.Suite, false) || !safeComponent(r.APT.Component, false) || !safeComponent(r.RPM.ID, false) || strings.TrimSpace(r.RPM.Name) == "" || strings.IndexFunc(r.RPM.Name, unicode.IsControl) >= 0 {
		return errors.New("invalid APT or RPM configuration")
	}
	if err := validateComponents(r.APT.Architectures); err != nil || len(r.APT.Architectures) == 0 {
		return errors.New("invalid APT architectures")
	}
	if err := validateComponents(r.RPM.Architectures); err != nil || len(r.RPM.Architectures) == 0 {
		return errors.New("invalid RPM architectures")
	}
	if r.RPM.PackageSignature != "metadata-only" && r.RPM.PackageSignature != "trusted-packages" {
		return errors.New("invalid RPM package signature policy")
	}
	if r.RPM.PackageSignature == "trusted-packages" && len(r.RPM.TrustedKeyPaths) == 0 {
		return errors.New("trusted RPM package keys are required")
	}
	keyPaths := map[string]bool{}
	for _, p := range r.RPM.TrustedKeyPaths {
		if err := SafePath(p); err != nil {
			return fmt.Errorf("invalid trusted key path: %w", err)
		}
		if keyPaths[p] {
			return errors.New("duplicate trusted key path")
		}
		keyPaths[p] = true
	}
	if len(r.Downloads.SourceHosts) == 0 || validateHosts(r.Downloads.SourceHosts) != nil || validateHosts(r.Downloads.RedirectHosts) != nil {
		return errors.New("invalid download host allowlist")
	}
	if r.Downloads.MaxBytes <= 0 || r.Downloads.MaxBytes > 2147483647 || r.Site.MaxBytes <= 0 || r.Site.MaxBytes > 1073741824 || r.Site.RetentionDays < 30 {
		return errors.New("invalid size or retention limit")
	}
	if r.Storage.Backend != "" && r.Storage.Backend != "none" && r.Storage.Backend != "github-releases" {
		return errors.New("invalid storage backend")
	}
	if r.Storage.Backend == "github-releases" {
		if !safeComponent(r.Storage.ReleasePrefix, false) || r.Storage.BucketSize < 1 || r.Storage.BucketSize > 1000 {
			return errors.New("invalid release storage settings")
		}
	} else if r.Storage.ReleasePrefix != "" || r.Storage.BucketSize != 0 {
		return errors.New("release storage settings require github-releases")
	}
	if strings.ContainsAny(r.Site.CNAME, "/:@?#\\\r\n\t ") || (r.Site.CNAME != "" && !validHost(r.Site.CNAME)) {
		return errors.New("invalid site CNAME")
	}
	return nil
}

func validateHTTPSBase(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || strings.ContainsAny(raw, "?#") || u.Port() != "" || !validHost(u.Hostname()) || strings.HasSuffix(raw, "/") {
		return errors.New("invalid HTTPS URL")
	}
	return nil
}

func validateComponents(v []string) error {
	seen := map[string]bool{}
	for _, s := range v {
		if !safeComponent(s, false) || seen[s] {
			return errors.New("invalid or duplicate component")
		}
		seen[s] = true
	}
	return nil
}

// SafePath accepts only literal relative site paths.
func SafePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\%?#\r\n\t") || path.Clean(p) != p {
		return errors.New("unsafe path")
	}
	for _, s := range strings.Split(p, "/") {
		if !safeComponent(s, true) {
			return errors.New("unsafe path component")
		}
	}
	return nil
}

// ServedPath derives the only permitted site path from inspected identity and bytes.
func ServedPath(p Package) (string, error) {
	i := p.Identity
	if !safeComponent(i.Name, false) || !safeComponent(i.Version, true) || !safeComponent(i.Architecture, false) || !hashRE.MatchString(p.SHA256) {
		return "", errors.New("invalid package identity or SHA-256")
	}
	short := p.SHA256[:12]
	switch p.Format {
	case "deb":
		if i.Epoch != "" || i.Release != "" {
			return "", errors.New("DEB identity has RPM fields")
		}
		return "deb/pool/" + i.Name + "_" + i.Version + "_" + i.Architecture + "_" + short + ".deb", nil
	case "rpm":
		if !safeComponent(i.Epoch, false) || !safeComponent(i.Release, true) {
			return "", errors.New("invalid RPM epoch or release")
		}
		epoch, err := strconv.ParseUint(i.Epoch, 10, 32)
		if err != nil || strconv.FormatUint(epoch, 10) != i.Epoch {
			return "", errors.New("invalid RPM epoch")
		}
		return "rpm/packages/" + i.Name + "-" + i.Epoch + "_" + i.Version + "-" + i.Release + "." + i.Architecture + "." + short + ".rpm", nil
	default:
		return "", errors.New("invalid package format")
	}
}

func (m Manifest) Validate(r Repository) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if m.Schema != 1 || m.Packages == nil {
		return errors.New("invalid manifest schema or missing packages")
	}
	paths := map[string]bool{}
	identities := map[string]bool{}
	for n, p := range m.Packages {
		if err := SafePath(p.Path); err != nil {
			return fmt.Errorf("package %d: %w", n, err)
		}
		want, err := ServedPath(p)
		if err != nil || p.Path != want {
			return fmt.Errorf("package %d: invalid served path or identity", n)
		}
		if p.Size <= 0 || p.Size > r.Downloads.MaxBytes || !hashRE.MatchString(p.SHA256) {
			return fmt.Errorf("package %d: invalid size or SHA-256", n)
		}
		if err := ValidateSourceURL(p.URL, r.Downloads.SourceHosts); err != nil {
			return fmt.Errorf("package %d: %w", n, err)
		}
		if p.Source != "upstream" && p.Source != "owned" {
			return fmt.Errorf("package %d: invalid source ownership", n)
		}
		if p.State != "active" && p.State != "retained" {
			return fmt.Errorf("package %d: invalid state", n)
		}
		if p.State == "retained" {
			t, err := time.Parse(time.RFC3339, p.RetainedAt)
			if err != nil || t.Location() != time.UTC || !strings.HasSuffix(p.RetainedAt, "Z") {
				return fmt.Errorf("package %d: invalid retention timestamp", n)
			}
		} else if p.RetainedAt != "" {
			return fmt.Errorf("package %d: active package has retention timestamp", n)
		}
		allowed := r.APT.Architectures
		if p.Format == "rpm" {
			allowed = r.RPM.Architectures
		}
		archOK := (p.Format == "deb" && p.Identity.Architecture == "all") || (p.Format == "rpm" && p.Identity.Architecture == "noarch")
		for _, a := range allowed {
			archOK = archOK || a == p.Identity.Architecture
		}
		if !archOK {
			return fmt.Errorf("package %d: architecture is not configured", n)
		}
		identity := p.Format + "/" + p.Identity.Name + "/" + p.Identity.Epoch + "/" + p.Identity.Version + "/" + p.Identity.Release + "/" + p.Identity.Architecture
		if paths[p.Path] || identities[identity] {
			return fmt.Errorf("package %d: duplicate path or identity", n)
		}
		paths[p.Path], identities[identity] = true, true
	}
	return nil
}

func (l Local) Validate() error {
	if l.Schema != 1 {
		return errors.New("invalid local schema")
	}
	for _, value := range []string{l.GPG.KeyID, l.GPG.Home, l.GPG.Keyring, l.GitHub.Account, l.GitHub.Token, l.GitHub.EnvFile, l.Storage.ReleaseRepository} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid local setting")
		}
	}
	if l.GitHub.Account != "" && !safeComponent(l.GitHub.Account, false) {
		return errors.New("invalid local setting")
	}
	if l.Storage.ReleaseRepository != "" {
		owner, repo, ok := strings.Cut(l.Storage.ReleaseRepository, "/")
		if !ok || !safeComponent(owner, false) || !safeComponent(repo, false) {
			return errors.New("invalid local release repository")
		}
	}
	return nil
}
