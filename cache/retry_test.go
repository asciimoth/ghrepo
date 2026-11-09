package cache

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeTracker struct {
	io.Reader
	closed bool
}

func (b *closeTracker) Close() error { b.closed = true; return nil }

func TestHTTPFetchRetries(t *testing.T) {
	for _, status := range []int{408, 429, 500, 502, 503, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				var bodies []*closeTracker
				start := time.Now()
				fetcher := HTTPFetcher{Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.URL.String() != "https://source.test/file" {
						t.Fatalf("retry changed source: %s", req.URL)
					}
					code := status
					if calls == 3 {
						code = http.StatusOK
					}
					body := &closeTracker{Reader: strings.NewReader("package-data")}
					bodies = append(bodies, body)
					return &http.Response{StatusCode: code, Header: make(http.Header), Body: body, Request: req}, nil
				})}}
				body, err := fetcher.Fetch(context.Background(), "https://source.test/file", []string{"source.test"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = body.Close() }()
				if calls != 3 || time.Since(start) != 3*time.Second {
					t.Fatalf("attempts=%d delay=%s", calls, time.Since(start))
				}
				if !bodies[0].closed || !bodies[1].closed || bodies[2].closed {
					t.Fatal("incorrect response body cleanup")
				}
			})
		})
	}
}

func TestHTTPFetchRetryLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		fetcher := HTTPFetcher{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, syscall.ECONNRESET
		})}}
		_, err := fetcher.Fetch(context.Background(), "https://source.test/file", []string{"source.test"}, nil)
		if calls != 3 || !errors.Is(err, syscall.ECONNRESET) {
			t.Fatalf("attempts=%d error=%v", calls, err)
		}
	})
}

func TestHTTPFetchDoesNotRetryPermanentStatus(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 501} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			fetcher := HTTPFetcher{Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
			})}}
			_, err := fetcher.Fetch(context.Background(), "https://source.test/file", []string{"source.test"}, nil)
			if calls != 1 || err == nil {
				t.Fatalf("attempts=%d error=%v", calls, err)
			}
		})
	}
}

func TestHTTPFetchCancellationDuringRetryWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		fetcher := HTTPFetcher{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, io.EOF
		})}}
		go func() { time.Sleep(100 * time.Millisecond); cancel() }()
		start := time.Now()
		_, err := fetcher.Fetch(ctx, "https://source.test/file", []string{"source.test"}, nil)
		if calls != 1 || !errors.Is(err, context.Canceled) || time.Since(start) != 100*time.Millisecond {
			t.Fatalf("attempts=%d delay=%s error=%v", calls, time.Since(start), err)
		}
	})
}

func TestTemporaryFetchError(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"timeout", context.DeadlineExceeded, true},
		{"dns temporary", &net.DNSError{IsTemporary: true}, true},
		{"dns not found", &net.DNSError{IsNotFound: true}, false},
		{"reset", &net.OpError{Err: syscall.ECONNRESET}, true},
		{"refused", syscall.ECONNREFUSED, true},
		{"pipe", syscall.EPIPE, true},
		{"eof", io.EOF, true},
		{"short read", io.ErrUnexpectedEOF, true},
		{"canceled", context.Canceled, false},
		{"certificate", &tls.CertificateVerificationError{Err: errors.New("invalid certificate")}, false},
		{"policy", &downloadError{message: "rejected", cause: context.DeadlineExceeded}, false},
		{"unknown", errors.New("unknown"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := temporaryFetchError(tt.err); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHTTPFetchRetryChecksRedirectPolicy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		fetcher := HTTPFetcher{Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.Host != "source.test" {
				t.Fatal("requested disallowed redirect host")
			}
			status := http.StatusServiceUnavailable
			headers := make(http.Header)
			if calls > 1 {
				status = http.StatusFound
				headers.Set("Location", "https://other.test/file?token=private-credential")
			}
			return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		})}}
		_, err := fetcher.Fetch(context.Background(), "https://source.test/file", []string{"source.test"}, nil)
		if calls != 2 || err == nil || !strings.Contains(err.Error(), "redirect rejected") || strings.Contains(err.Error(), "private-credential") {
			t.Fatalf("attempts=%d error=%v", calls, err)
		}
	})
}
