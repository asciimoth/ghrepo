// Package cache stores verified package bytes outside the Git tree.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/asciimoth/ghrepo/contract"
)

// Fetcher opens a public source and returns its body. It must enforce URL and
// redirect policy before each network request.
type Fetcher interface {
	Fetch(context.Context, string, []string, []string) (io.ReadCloser, error)
}

type HTTPFetcher struct{ Client *http.Client }

func publicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

func dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid source address")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, errors.New("source DNS lookup failed")
	}
	foundPublic := false
	for _, address := range addresses {
		if publicIP(address.IP) {
			foundPublic = true
			conn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return conn, nil
			}
		}
	}
	if foundPublic {
		return nil, errors.New("source connection failed")
	}
	return nil, errors.New("source resolves to a private address")
}

func (h HTTPFetcher) Fetch(ctx context.Context, raw string, sources, redirects []string) (io.ReadCloser, error) {
	if err := contract.ValidateSourceURL(raw, sources); err != nil {
		return nil, err
	}
	client := http.Client{}
	if h.Client != nil {
		client = *h.Client
	} else {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = dialPublic
		client.Transport = transport
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		allowed := append(append([]string(nil), sources...), redirects...)
		return contract.ValidateSourceURL(req.URL.String(), allowed)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, errors.New("invalid source request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("source download failed")
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("source returned HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// VerifyFile checks a regular file against the manifest entry.
func VerifyFile(name string, p contract.Package) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != p.Size {
		return errors.New("cached package has invalid type or size")
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return errors.New("cached package changed while opening")
	}
	if err := verifyReader(f, io.Discard, p); err != nil {
		return errors.New("cached package has invalid SHA-256 or size")
	}
	return nil
}

func verifyReader(input io.Reader, output io.Writer, p contract.Package) error {
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, p.Size+1))
	if err != nil || n != p.Size || hex.EncodeToString(hash.Sum(nil)) != p.SHA256 {
		return errors.New("package has invalid SHA-256 or size")
	}
	return nil
}

// Ensure returns a verified cache path. Failed downloads do not replace files.
func Ensure(ctx context.Context, root string, r contract.Repository, p contract.Package, fetcher Fetcher) (string, error) {
	return ensure(ctx, root, r, p, fetcher, false)
}

// FetchVerified checks the public source even when the expected bytes are in
// the cache. Source changes and site builds must use this function.
func FetchVerified(ctx context.Context, root string, r contract.Repository, p contract.Package, fetcher Fetcher) (string, error) {
	return ensure(ctx, root, r, p, fetcher, true)
}

func ensure(ctx context.Context, root string, r contract.Repository, p contract.Package, fetcher Fetcher, force bool) (string, error) {
	if err := (contract.Manifest{Schema: 1, Packages: []contract.Package{p}}).Validate(r); err != nil {
		return "", err
	}
	if fetcher == nil {
		fetcher = HTTPFetcher{}
	}
	dir := filepath.Join(root, "sha256")
	name := filepath.Join(dir, p.SHA256)
	if !force {
		if err := VerifyFile(name, p); err == nil {
			return name, nil
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	body, err := fetcher.Fetch(ctx, p.URL, r.Downloads.SourceHosts, r.Downloads.RedirectHosts)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("source download failed")
	}
	if body == nil {
		return "", errors.New("source download returned no body")
	}
	defer func() { _ = body.Close() }()
	f, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := verifyReader(body, f, p); err != nil {
		_ = f.Close()
		return "", errors.New("downloaded package has invalid SHA-256 or size")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), name); err != nil {
		return "", err
	}
	return name, nil
}
