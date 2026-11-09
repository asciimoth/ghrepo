// Package cache stores verified package bytes outside the Git tree.
package cache

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/asciimoth/ghrepo/contract"
)

// Fetcher opens a public source and returns its body. It must enforce URL and
// redirect policy before each network request.
type Fetcher interface {
	Fetch(context.Context, string, []string, []string) (io.ReadCloser, error)
}

type HTTPFetcher struct {
	Client         *http.Client
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
}

// downloadError contains a safe diagnostic. The cause can include signed URLs
// or credentials and must not be included in Error.
type downloadError struct {
	message   string
	cause     error
	retryable bool
}

func (e *downloadError) Error() string { return e.message }
func (e *downloadError) Unwrap() error { return e.cause }

func sourceError(err error) error {
	message := "source request failed"
	var dns *net.DNSError
	var op *net.OpError
	var certificate *tls.CertificateVerificationError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		message = "source request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		message = "source request timed out"
	case errors.As(err, &dns):
		message = "source DNS lookup failed"
	case errors.As(err, &certificate):
		message = "source TLS certificate verification failed"
	case errors.As(err, &network) && network.Timeout():
		message = "source request timed out"
	case errors.As(err, &op):
		message = "source connection failed"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		message = "source connection closed unexpectedly"
	}
	return &downloadError{message: message, cause: err, retryable: temporaryFetchError(err)}
}

// temporaryFetchError uses error types, never error text, to decide whether a
// new request may succeed. Policy and certificate errors are not temporary.
func temporaryFetchError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var diagnostic *downloadError
	if errors.As(err, &diagnostic) {
		return diagnostic.retryable
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.IsTimeout || dns.IsTemporary
	}
	var network net.Error
	return errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &network) && network.Timeout()) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE)
}

const fetchAttempts = 3

const DefaultConnectTimeout = 10 * time.Second
const DefaultRequestTimeout = 10 * time.Minute

// RepositoryFetcher applies repository timeouts to the default HTTP fetcher.
func RepositoryFetcher(fetcher Fetcher, r contract.Repository) Fetcher {
	if fetcher != nil {
		return fetcher
	}
	return HTTPFetcher{
		ConnectTimeout: time.Duration(r.Downloads.ConnectTimeoutSeconds) * time.Second,
		RequestTimeout: time.Duration(r.Downloads.RequestTimeoutSeconds) * time.Second,
	}
}

func publicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

func dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	return dialPublicWithTimeout(ctx, network, address, DefaultConnectTimeout)
}

func dialPublicWithTimeout(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid source address")
	}
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(lookupCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("source DNS lookup failed: %w", context.DeadlineExceeded)
		}
		return nil, &downloadError{message: "source DNS lookup failed", cause: err, retryable: temporaryFetchError(err)}
	}
	foundPublic := false
	var lastErr error
	for _, address := range addresses {
		if publicIP(address.IP) {
			foundPublic = true
			conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
	}
	if foundPublic {
		return nil, fmt.Errorf("source connection failed: %w", lastErr)
	}
	return nil, &downloadError{message: "source resolves to a private address"}
}

func (h HTTPFetcher) Fetch(ctx context.Context, raw string, sources, redirects []string) (io.ReadCloser, error) {
	if err := contract.ValidateSourceURL(raw, sources); err != nil {
		return nil, err
	}
	client := http.Client{}
	connectTimeout := h.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = DefaultConnectTimeout
	}
	if h.Client != nil {
		client = *h.Client
	} else {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialPublicWithTimeout(ctx, network, address, connectTimeout)
		}
		client.Transport = transport
	}
	if client.Timeout == 0 {
		client.Timeout = h.RequestTimeout
		if client.Timeout <= 0 {
			client.Timeout = DefaultRequestTimeout
		}
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return &downloadError{message: "source has too many redirects"}
		}
		allowed := append(append([]string(nil), sources...), redirects...)
		// Release assets use a signed query on the redirect target. The
		// manifest source itself must still have no query parameters.
		target := *req.URL
		target.RawQuery = ""
		target.ForceQuery = false
		if err := contract.ValidateSourceURL(target.String(), allowed); err != nil {
			return &downloadError{message: "source redirect rejected by URL policy", cause: err}
		}
		return nil
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, sourceError(err)
		}
		body, err := fetchOnce(ctx, &client, raw)
		if err == nil || !temporaryFetchError(err) || attempt == fetchAttempts-1 {
			return body, err
		}
		timer := time.NewTimer(time.Second << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, sourceError(ctx.Err())
		case <-timer.C:
		}
	}
}

func fetchOnce(ctx context.Context, client *http.Client, raw string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, errors.New("invalid source request")
	}
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		var diagnostic *downloadError
		if errors.As(err, &diagnostic) {
			return nil, diagnostic
		}
		return nil, sourceError(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		retryable := resp.StatusCode == http.StatusRequestTimeout ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusInternalServerError ||
			resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout
		return nil, &downloadError{message: fmt.Sprintf("source returned HTTP %d", resp.StatusCode), retryable: retryable}
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
	if err != nil {
		return fmt.Errorf("read package: %w", err)
	}
	if n != p.Size || hex.EncodeToString(hash.Sum(nil)) != p.SHA256 {
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
		fetcher = RepositoryFetcher(nil, r)
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
		var diagnostic *downloadError
		if errors.As(err, &diagnostic) {
			return "", fmt.Errorf("source download failed: %w", diagnostic)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return "", fmt.Errorf("source download failed: %w", sourceError(err))
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
		return "", fmt.Errorf("downloaded package: %w", err)
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
