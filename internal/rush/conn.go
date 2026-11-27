package rush

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// userConn is the dedicated-mode HTTP/1.1 client (LOADTEST §3.3): one
// keep-alive TCP connection per simulated user, like a browser tab, driven
// by that user's own goroutine.
//
// It replaces a per-user http.Transport. The server sees the same thing:
// one connection per user, kept alive between requests, re-dialled after an
// error or a timeout, and byte-identical requests (TestUserConnWireFormat
// compares them with net/http's). What changes is the swarm's own cost.
// net/http runs a read and a write goroutine for every connection and hands
// each request between three goroutines over channels. The old client also
// added a context, two timers and an httptrace hook per request. Here the
// user's goroutine writes the request and reads the response itself, the
// per-call timeout is a socket deadline, and the 4 KiB read buffer is
// borrowed from a pool only while a response is being read.
type userConn struct {
	addr string // host:port to dial
	host string // Host header
	pfx  string // path prefix (/api/v1)

	mu      sync.Mutex // guards conn and aborted against abort()
	conn    net.Conn
	reused  bool // conn has already carried a full exchange
	aborted bool // the run is over: no new dials
	stop    func() bool
}

var readerPool = sync.Pool{New: func() any { return bufio.NewReaderSize(nil, 4096) }}

var reqBufPool = sync.Pool{New: func() any { b := make([]byte, 0, 1024); return &b }}

func getReader() *bufio.Reader {
	if br, ok := readerPool.Get().(*bufio.Reader); ok {
		return br
	}
	return bufio.NewReaderSize(nil, 4096)
}

func getReqBuf() *[]byte {
	if bp, ok := reqBufPool.Get().(*[]byte); ok {
		return bp
	}
	b := make([]byte, 0, 1024)
	return &b
}

var dialer = &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}

// newUserConn parses base (http://host:port/prefix). The connection closes
// when ctx ends, which also unblocks an exchange in flight.
func newUserConn(ctx context.Context, base string) (*userConn, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" {
		return nil, fmt.Errorf("rush: dedicated connections support http:// targets only, got %q", u.Scheme)
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "80")
	}
	c := &userConn{addr: addr, host: u.Host, pfx: u.EscapedPath()}
	c.stop = context.AfterFunc(ctx, c.abort)
	return c, nil
}

func (c *userConn) abort() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.aborted = true
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// Close ends the connection (the user is done).
func (c *userConn) Close() {
	c.stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *userConn) drop(nc net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = nc.Close()
	if c.conn == nc {
		c.conn, c.reused = nil, false
	}
}

// get returns the live connection or dials one.
func (c *userConn) get(ctx context.Context) (net.Conn, bool, error) {
	c.mu.Lock()
	if c.aborted {
		c.mu.Unlock()
		return nil, false, context.Canceled
	}
	if c.conn != nil {
		nc, reused := c.conn, c.reused
		c.mu.Unlock()
		return nc, reused, nil
	}
	c.mu.Unlock()
	nc, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.aborted {
		_ = nc.Close()
		return nil, false, context.Canceled
	}
	c.conn, c.reused = nc, false
	return nc, false, nil
}

// exchange is one finished request on the wire.
type exchange struct {
	status     int
	body       []byte
	retryAfter time.Duration
}

// errNoResponse: the connection failed before any response byte arrived.
var errNoResponse = errors.New("rush: connection closed before a response")

// roundTrip sends q once, retrying once on a fresh connection when a reused
// keep-alive connection turns out to be dead before any response byte, under
// net/http's own rule (Transport.shouldRetryRequest: a replayable request,
// i.e. GET/HEAD/OPTIONS/TRACE or one carrying an Idempotency-Key). The
// timeout runs from the moment the request has its connection.
func (c *userConn) roundTrip(ctx context.Context, q *req, body []byte, xff string) (exchange, error) {
	replayable := q.ik != "" || q.method == "GET" || q.method == "HEAD" || q.method == "OPTIONS" || q.method == "TRACE"
	for try := 0; ; try++ {
		nc, reused, err := c.get(ctx)
		if err != nil {
			return exchange{}, err
		}
		ex, err := c.once(nc, q, body, xff)
		if err == nil {
			return ex, nil
		}
		c.drop(nc)
		if ctx.Err() != nil {
			return exchange{}, ctx.Err()
		}
		if try == 0 && reused && replayable && errors.Is(err, errNoResponse) {
			continue
		}
		return exchange{}, err
	}
}

func (c *userConn) once(nc net.Conn, q *req, body []byte, xff string) (exchange, error) {
	if err := nc.SetDeadline(time.Now().Add(q.timeout)); err != nil {
		return exchange{}, err
	}
	bp := getReqBuf()
	buf := appendRequest((*bp)[:0], c.host, c.pfx, q, body, xff)
	_, werr := nc.Write(buf)
	*bp = buf
	reqBufPool.Put(bp)
	if werr != nil {
		if isTimeout(werr) {
			return exchange{}, werr
		}
		return exchange{}, fmt.Errorf("%w: %w", errNoResponse, werr)
	}
	br := getReader()
	br.Reset(nc)
	ex, keep, err := readResponse(br, q.method)
	clean := br.Buffered() == 0
	br.Reset(nil)
	readerPool.Put(br)
	if err != nil {
		return exchange{}, err
	}
	if !keep || !clean { // close, or bytes we did not ask for
		c.drop(nc)
		return ex, nil
	}
	c.mu.Lock()
	if c.conn == nc {
		c.reused = true
	}
	c.mu.Unlock()
	return ex, nil
}

const userAgent = "Go-http-client/1.1" // what net/http sends by default

// appendRequest writes q exactly as net/http's Transport does for this
// request: request line, Host, User-Agent, Content-Length (also 0 for a
// body-less POST/PUT/PATCH), then the headers in sorted canonical order.
func appendRequest(b []byte, host, pfx string, q *req, body []byte, xff string) []byte {
	b = append(b, q.method...)
	b = append(b, ' ')
	b = append(b, pfx...)
	b = append(b, q.path...)
	b = append(b, " HTTP/1.1\r\nHost: "...)
	b = append(b, host...)
	b = append(b, "\r\nUser-Agent: "+userAgent+"\r\n"...)
	if body != nil || q.method == "POST" || q.method == "PUT" || q.method == "PATCH" {
		b = append(b, "Content-Length: "...)
		b = strconv.AppendInt(b, int64(len(body)), 10)
		b = append(b, "\r\n"...)
	}
	if q.token != "" {
		b = append(b, "Authorization: Bearer "...)
		b = append(b, q.token...)
		b = append(b, "\r\n"...)
	}
	if body != nil {
		b = append(b, "Content-Type: application/json\r\n"...)
	}
	if q.ik != "" {
		b = append(b, "Idempotency-Key: "...)
		b = append(b, q.ik...)
		b = append(b, "\r\n"...)
	}
	b = append(b, "X-Forwarded-For: "...)
	b = append(b, xff...)
	b = append(b, "\r\n\r\n"...)
	return append(b, body...)
}

// readResponse reads one HTTP/1.x response. keep reports whether the
// connection may carry another request.
func readResponse(br *bufio.Reader, method string) (ex exchange, keep bool, err error) {
	first := true
	for {
		line, err := readLine(br)
		if err != nil {
			if first {
				return ex, false, fmt.Errorf("%w: %w", errNoResponse, err)
			}
			return ex, false, err
		}
		first = false
		// "HTTP/1.1 200 OK"
		if len(line) < 12 || !bytes.HasPrefix(line, []byte("HTTP/1.")) || line[8] != ' ' {
			return ex, false, fmt.Errorf("rush: malformed status line %q", line)
		}
		code, err := strconv.Atoi(string(line[9:12]))
		if err != nil {
			return ex, false, fmt.Errorf("rush: malformed status line %q", line)
		}
		http10 := line[7] == '0'
		var (
			cl                 int64 = -1
			chunked, connClose bool
			keepAlive          bool
		)
		for {
			h, err := readLine(br)
			if err != nil {
				return ex, false, err
			}
			if len(h) == 0 {
				break
			}
			name, val, ok := bytes.Cut(h, []byte{':'})
			if !ok {
				continue
			}
			val = bytes.TrimSpace(val)
			switch {
			case asciiEqualFold(name, "Content-Length"):
				if cl, err = strconv.ParseInt(string(val), 10, 64); err != nil || cl < 0 {
					return ex, false, fmt.Errorf("rush: bad Content-Length %q", val)
				}
			case asciiEqualFold(name, "Transfer-Encoding"):
				chunked = asciiEqualFold(val, "chunked")
			case asciiEqualFold(name, "Connection"):
				connClose = connClose || asciiEqualFold(val, "close")
				keepAlive = keepAlive || asciiEqualFold(val, "keep-alive")
			case asciiEqualFold(name, "Retry-After"):
				if n, perr := strconv.Atoi(string(val)); perr == nil && n >= 0 {
					ex.retryAfter = time.Duration(n) * time.Second
				}
			}
		}
		if code >= 100 && code < 200 && code != 101 {
			ex.retryAfter = 0
			continue // interim response; the real one follows
		}
		ex.status = code
		keep = !connClose && (!http10 || keepAlive)
		switch {
		case method == "HEAD" || code == 204 || code == 304:
		case chunked:
			if ex.body, err = readChunked(br); err != nil {
				return ex, false, err
			}
		case cl >= 0:
			if cl > maxBody {
				ex.body = make([]byte, maxBody)
				_, err = io.ReadFull(br, ex.body)
				return ex, false, err // the rest is unread: drop the connection
			}
			ex.body = make([]byte, cl)
			if _, err = io.ReadFull(br, ex.body); err != nil {
				return ex, false, err
			}
		default: // body runs to EOF
			ex.body, err = io.ReadAll(io.LimitReader(br, maxBody))
			return ex, false, err
		}
		return ex, keep, nil
	}
}

func readChunked(br *bufio.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(httputil.NewChunkedReader(br), maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, errors.New("rush: chunked body over the limit")
	}
	for { // trailer section, up to the blank line
		l, err := readLine(br)
		if err != nil {
			return nil, err
		}
		if len(l) == 0 {
			return b, nil
		}
	}
}

// readLine returns one line without its CRLF. The slice is only valid until
// the next read.
func readLine(br *bufio.Reader) ([]byte, error) {
	l, err := br.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	l = l[:len(l)-1]
	if n := len(l); n > 0 && l[n-1] == '\r' {
		l = l[:n-1]
	}
	return l, nil
}

func asciiEqualFold[T []byte | string](a T, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(b) {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
