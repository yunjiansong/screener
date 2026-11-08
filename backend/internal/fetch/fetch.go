// Package fetch is a small rate-limited HTTP client with an on-disk cache.
package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var ErrNotFound = errors.New("not found")

type Client struct {
	hc       *http.Client
	ua       string
	cacheDir string
	tokens   <-chan time.Time
}

func New(userAgent, cacheDir string, requestsPerSecond float64) *Client {
	return &Client{
		hc:       &http.Client{Timeout: 60 * time.Second},
		ua:       userAgent,
		cacheDir: cacheDir,
		tokens:   time.NewTicker(time.Duration(float64(time.Second) / requestsPerSecond)).C,
	}
}

// HTTP1 forces HTTP/1.1. Some unofficial endpoints (Google translate) answer Go's
// HTTP/2 client with 429 while accepting the same request over HTTP/1.1.
func (c *Client) HTTP1() *Client {
	c.hc = &http.Client{Timeout: c.hc.Timeout, Transport: &http.Transport{
		Proxy:        http.ProxyFromEnvironment,
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}}
	return c
}

// Get returns the body for url, served from cache/<key> when younger than maxAge.
// 404 responses are cached too, so missing resources are not re-requested every run.
func (c *Client) Get(ctx context.Context, url, key string, maxAge time.Duration) ([]byte, error) {
	path := filepath.Join(c.cacheDir, key)
	if fresh(path, maxAge) {
		return os.ReadFile(path)
	}
	if fresh(path+".404", maxAge) {
		return nil, ErrNotFound
	}
	var lastErr error
	for attempt := range 4 {
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(1<<attempt)*time.Second); err != nil {
				return nil, err
			}
		}
		resp, err := c.do(ctx, url)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch {
		case err != nil:
			lastErr = err
		case resp.StatusCode == http.StatusOK:
			return body, write(path, body)
		case resp.StatusCode == http.StatusNotFound:
			_ = write(path+".404", nil)
			return nil, ErrNotFound
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("GET %s: %s", url, resp.Status)
		default:
			return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
		}
	}
	return nil, lastErr
}

// Download streams url to cache/<key> unless a copy younger than maxAge exists, returning the path.
func (c *Client) Download(ctx context.Context, url, key string, maxAge time.Duration) (string, error) {
	path := filepath.Join(c.cacheDir, key)
	if fresh(path, maxAge) {
		return path, nil
	}
	resp, err := c.do(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.Create(path + ".part")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(path+".part", path)
}

func (c *Client) do(ctx context.Context, url string) (*http.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.tokens:
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	// A download-only client has no timeout; large archives take minutes.
	hc := c.hc
	if filepath.Ext(url) == ".zip" {
		hc = &http.Client{}
	}
	return hc.Do(req)
}

func fresh(path string, maxAge time.Duration) bool {
	st, err := os.Stat(path)
	return err == nil && time.Since(st.ModTime()) < maxAge
}

func write(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
