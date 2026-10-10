package panel

import (
	"bufio"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The Web Panel is HTTPS only. A browser pointed at http://host:port reaches
// the TLS listener and used to get Go's raw "Client sent an HTTP request to an
// HTTPS server". sniffListener looks at the first byte of every connection: a
// TLS handshake (0x16) goes on to the TLS server unchanged, anything else is
// answered here with a short, friendly HTTP reply and closed. Nothing of the
// panel is ever served over plain HTTP: no page, no API, no cookies. The reply
// is a redirect to the same host and port over https:// (only when the Host
// header is an IP literal or "localhost" on our own port, so it cannot be
// steered to another site) or a plain explanation.

const (
	sniffTimeout    = 5 * time.Second
	maxSniffers     = 64
	plainReadLimit  = 8 << 10
	tlsHandshakeRec = 0x16
)

type sniffListener struct {
	net.Listener
	port string

	conns chan net.Conn
	errc  chan error
	done  chan struct{}
	once  sync.Once
	slots chan struct{}
}

func newSniffListener(inner net.Listener) *sniffListener {
	_, port, _ := net.SplitHostPort(inner.Addr().String())
	l := &sniffListener{Listener: inner, port: port,
		conns: make(chan net.Conn), errc: make(chan error, 1), done: make(chan struct{}),
		slots: make(chan struct{}, maxSniffers)}
	go l.loop()
	return l
}

func (l *sniffListener) loop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			select {
			case l.errc <- err:
			default:
			}
			return
		}
		select {
		case l.slots <- struct{}{}:
			go l.sniff(c)
		default:
			c.Close() // too many connections still undecided
		}
	}
}

func (l *sniffListener) sniff(c net.Conn) {
	defer func() { <-l.slots }()
	c.SetReadDeadline(time.Now().Add(sniffTimeout))
	br := bufio.NewReaderSize(c, 4096)
	b, err := br.Peek(1)
	if err != nil {
		c.Close()
		return
	}
	if b[0] != tlsHandshakeRec {
		l.plain(c, br)
		return
	}
	c.SetReadDeadline(time.Time{})
	select {
	case l.conns <- &sniffedConn{Conn: c, r: br}:
	case <-l.done:
		c.Close()
	}
}

// Accept returns connections that began with a TLS handshake.
func (l *sniffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.errc:
		return nil, err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *sniffListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

// sniffedConn replays the bytes already buffered by the sniffer.
type sniffedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *sniffedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// plain answers a non-TLS client and closes the connection.
func (l *sniffListener) plain(c net.Conn, br *bufio.Reader) {
	defer c.Close()
	c.SetWriteDeadline(time.Now().Add(sniffTimeout))
	req, err := http.ReadRequest(bufio.NewReader(io.LimitReader(br, plainReadLimit)))
	target, redirect := "", false
	if err == nil {
		target, redirect = l.httpsTarget(req.Host)
	}
	writePlainReply(c, target, redirect, req != nil && req.Method == http.MethodHead)
}

// httpsTarget returns https://host:port/ for a Host header that names an IP
// literal or localhost on this panel's own port.
func (l *sniffListener) httpsTarget(hostHeader string) (string, bool) {
	host, port, err := net.SplitHostPort(hostHeader)
	if err != nil || port != l.port {
		return "", false
	}
	if host != "localhost" && net.ParseIP(host) == nil {
		return "", false
	}
	return "https://" + net.JoinHostPort(host, port) + "/", true
}

func writePlainReply(w io.Writer, target string, redirect, headOnly bool) {
	var body, status, location string
	if redirect {
		status = "307 Temporary Redirect"
		location = "Location: " + target + "\r\n"
		body = page("This panel uses HTTPS",
			fmt.Sprintf(`<p>You opened the panel with <b>http://</b>. It only answers over an encrypted connection.</p><p>Use exactly this address: <a href="%[1]s">%[1]s</a></p>`, html.EscapeString(target)))
	} else {
		status = "400 Bad Request"
		body = page("This panel uses HTTPS",
			`<p>This is the ThothDock Web Panel and it only answers over an encrypted connection.</p><p>Open <b>https://</b> followed by the phone's address and port exactly as the ThothDock app shows it (for example <code>https://192.168.1.20:7690/</code>). The browser will warn that the certificate is self-signed: compare its fingerprint with the one in the app before you continue.</p>`)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "HTTP/1.1 %s\r\n%sContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\n", status, location, len(body))
	sb.WriteString("Cache-Control: no-store\r\nConnection: close\r\nX-Content-Type-Options: nosniff\r\nReferrer-Policy: no-referrer\r\n")
	sb.WriteString("Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'\r\n\r\n")
	if !headOnly {
		sb.WriteString(body)
	}
	io.WriteString(w, sb.String())
}

func page(title, inner string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` +
		html.EscapeString(title) + `</title><style>body{margin:0;background:#070B10;color:#E7F2F8;font:16px/1.5 system-ui,sans-serif;display:grid;place-items:center;min-height:100vh}` +
		`main{max-width:520px;margin:24px;padding:24px;background:#111820;border:1px solid #253542;border-radius:12px}h1{margin:0 0 12px;font-size:20px;color:#18E7FF}` +
		`a{color:#6CF4FF}code{background:#18222D;padding:2px 6px;border-radius:4px}p{color:#9BAEBB}b{color:#E7F2F8}</style></head><body><main><h1>` +
		html.EscapeString(title) + `</h1>` + inner + `</main></body></html>`
}
