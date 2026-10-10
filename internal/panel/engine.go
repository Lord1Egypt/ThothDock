package panel

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// engineClient talks to the engine's Unix socket: the panel's only way in.
type engineClient struct {
	hc *http.Client
}

func newEngineClient(socket string) *engineClient {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return &engineClient{hc: &http.Client{Transport: tr}}
}

// engineError is a non-2xx answer from the engine, with its message.
type engineError struct {
	Status  int
	Message string
}

func (e *engineError) Error() string { return e.Message }

func (c *engineClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://thothdock/v1.41"+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, &engineError{Status: http.StatusBadGateway, Message: "the ThothDock engine is not running"}
	}
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		defer resp.Body.Close()
		var m struct{ Message string }
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&m)
		if m.Message == "" {
			m.Message = fmt.Sprintf("engine answered %d", resp.StatusCode)
		}
		return nil, &engineError{Status: resp.StatusCode, Message: m.Message}
	}
	return resp, nil
}

func (c *engineClient) getJSON(ctx context.Context, path string, v any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}

func (c *engineClient) call(ctx context.Context, method, path string) error {
	resp, err := c.do(ctx, method, path, nil)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.Body.Close()
}

// demux turns a non-TTY log stream (8-byte frame headers) into text.
func demux(r io.Reader, limit int64) (string, error) {
	var out bytes.Buffer
	hdr := make([]byte, 8)
	for int64(out.Len()) < limit {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return out.String(), err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		if _, err := io.CopyN(&out, r, min(n, limit-int64(out.Len()))); err != nil {
			break
		}
	}
	return strings.ToValidUTF8(out.String(), "�"), nil
}
