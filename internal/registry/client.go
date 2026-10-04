package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/oci"
)

// MaxManifestSize bounds manifest and index documents.
const MaxManifestSize = 4 << 20

// Credentials authenticate to a registry (from the Docker CLI's
// X-Registry-Auth header). Empty means anonymous.
type Credentials struct {
	Username string
	Password string
}

// Client is a pull-only OCI distribution client. It speaks HTTPS only and
// refuses redirects to anything else.
type Client struct {
	HTTP      *http.Client
	UserAgent string

	mu     sync.Mutex
	tokens map[string]string // host + scope -> bearer token
}

// NewClient returns a client using hc, or a default HTTPS client.
func NewClient(hc *http.Client, userAgent string) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 0}
	}
	c := *hc
	prev := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing redirect to non-HTTPS URL %s", req.URL.Redacted())
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if prev != nil {
			return prev(req, via)
		}
		return nil
	}
	return &Client{HTTP: &c, UserAgent: userAgent, tokens: map[string]string{}}
}

// Error is a registry response error.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", strings.ToLower(strings.ReplaceAll(e.Code, "_", " ")), e.Message)
	}
	return fmt.Sprintf("registry returned HTTP %d", e.Status)
}

// IsNotFound reports a missing repository, tag or blob. Docker Hub answers
// an unknown repository with 401, so that counts too.
func IsNotFound(err error) bool {
	var re *Error
	return errors.As(err, &re) && (re.Status == http.StatusNotFound || re.Status == http.StatusUnauthorized)
}

func (c *Client) do(ctx context.Context, ref Reference, creds Credentials, path string, accept []string) (*http.Response, error) {
	u := "https://" + ref.Host() + "/v2/" + ref.Path + path
	scope := "repository:" + ref.Path + ":pull"
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		for _, a := range accept {
			req.Header.Add("Accept", a)
		}
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		c.mu.Lock()
		tok := c.tokens[ref.Host()+" "+scope]
		c.mu.Unlock()
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		} else if creds.Username != "" && attempt > 0 {
			req.SetBasicAuth(creds.Username, creds.Password)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, nil
		}
		challenge := resp.Header.Get("WWW-Authenticate")
		drain(resp)
		scheme, params := parseChallenge(challenge)
		switch scheme {
		case "bearer":
			if err := c.fetchToken(ctx, ref.Host(), scope, params, creds); err != nil {
				return nil, err
			}
		case "basic":
			if creds.Username == "" {
				return nil, &Error{Status: http.StatusUnauthorized, Code: "UNAUTHORIZED", Message: "authentication required"}
			}
		default:
			return nil, &Error{Status: http.StatusUnauthorized, Code: "UNAUTHORIZED", Message: "unsupported authentication challenge"}
		}
	}
	return nil, errors.New("unreachable")
}

func (c *Client) fetchToken(ctx context.Context, host, scope string, params map[string]string, creds Credentials) error {
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" {
		return fmt.Errorf("registry %s offered an unusable token realm %q", host, params["realm"])
	}
	q := realm.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return err
	}
	if creds.Username != "" {
		req.SetBasicAuth(creds.Username, creds.Password)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return &Error{Status: resp.StatusCode, Code: "UNAUTHORIZED", Message: fmt.Sprintf("token request failed with HTTP %d", resp.StatusCode)}
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return fmt.Errorf("decoding token response: %w", err)
	}
	tok := tr.Token
	if tok == "" {
		tok = tr.AccessToken
	}
	if tok == "" {
		return errors.New("token response carried no token")
	}
	c.mu.Lock()
	c.tokens[host+" "+scope] = tok
	c.mu.Unlock()
	return nil
}

// parseChallenge reads `Bearer realm="...",service="...",scope="..."`.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(v, `"`) {
			end := strings.IndexByte(v[1:], '"')
			if end < 0 {
				break
			}
			params[k] = v[1 : end+1]
			rest = v[end+2:]
		} else {
			val, after, _ := strings.Cut(v, ",")
			params[k] = strings.TrimSpace(val)
			rest = after
		}
	}
	return strings.ToLower(scheme), params
}

func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var er struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	e := &Error{Status: resp.StatusCode}
	if json.Unmarshal(body, &er) == nil && len(er.Errors) > 0 {
		e.Code, e.Message = er.Errors[0].Code, er.Errors[0].Message
	}
	return e
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

var manifestAccept = []string{oci.MediaTypeOCIIndex, oci.MediaTypeDockerManifestList, oci.MediaTypeOCIManifest, oci.MediaTypeDockerManifest}

// Manifest is a fetched, digest-checked manifest or index.
type Manifest struct {
	Data      []byte
	MediaType string
	Digest    oci.Digest
}

// GetManifest fetches the manifest or index named by selector (a tag or a
// digest). The returned digest is computed from the bytes; it must equal
// the selector when that is a digest and the registry's
// Docker-Content-Digest header when present.
func (c *Client) GetManifest(ctx context.Context, ref Reference, selector string, creds Credentials) (*Manifest, error) {
	resp, err := c.do(ctx, ref, creds, "/manifests/"+selector, manifestAccept)
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxManifestSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxManifestSize {
		return nil, fmt.Errorf("manifest for %s exceeds %d bytes", ref, MaxManifestSize)
	}
	got := oci.FromBytes(data)
	if strings.HasPrefix(selector, "sha256:") && oci.Digest(selector) != got {
		return nil, fmt.Errorf("manifest digest mismatch: requested %s, content hashes to %s", selector, got)
	}
	if h := resp.Header.Get("Docker-Content-Digest"); h != "" {
		if hd, err := oci.ParseDigest(h); err == nil && hd != got {
			return nil, fmt.Errorf("manifest digest mismatch: registry says %s, content hashes to %s", hd, got)
		}
	}
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("manifest for %s is not JSON: %w", ref, err)
	}
	mt := probe.MediaType
	if mt == "" {
		mt, _, _ = strings.Cut(resp.Header.Get("Content-Type"), ";")
		mt = strings.TrimSpace(mt)
	}
	if !oci.IsIndex(mt) && !oci.IsManifest(mt) {
		return nil, fmt.Errorf("unsupported manifest media type %q for %s", mt, ref)
	}
	return &Manifest{Data: data, MediaType: mt, Digest: got}, nil
}

// OpenBlob streams a blob. The caller must verify its digest and size;
// store.Blobs.Ingest does.
func (c *Client) OpenBlob(ctx context.Context, ref Reference, d oci.Digest, creds Credentials) (io.ReadCloser, error) {
	resp, err := c.do(ctx, ref, creds, "/blobs/"+string(d), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer drain(resp)
		return nil, responseError(resp)
	}
	return resp.Body, nil
}

// DefaultHTTPClient is used for real registries.
func DefaultHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: t}
}
