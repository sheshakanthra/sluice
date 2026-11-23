package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sheshakanthra/sluice/internal/obs"
)

// HeaderRequestID carries the request id in both directions.
const HeaderRequestID = "X-Request-Id"

// statusWriter records the status code and body size of a response.
type statusWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush supports streaming handlers behind the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wroteHeader {
			w.WriteHeader(http.StatusOK)
		}
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func wrap(w http.ResponseWriter) *statusWriter {
	if sw, ok := w.(*statusWriter); ok {
		return sw
	}
	return &statusWriter{ResponseWriter: w, status: http.StatusOK}
}

// RequestID assigns every request an id (UUIDv7), or accepts a well-formed
// X-Request-Id from a trusted proxy, stores it in the context and echoes it
// on the response.
func RequestID(trusted []netip.Prefix) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if id == "" || !validRequestID(id) || !fromTrusted(r.RemoteAddr, trusted) {
				id = newID()
			}
			w.Header().Set(HeaderRequestID, id)
			next.ServeHTTP(w, r.WithContext(obs.WithRequestID(r.Context(), id)))
		})
	}
}

func newID() string {
	if u, err := uuid.NewV7(); err == nil {
		return u.String()
	}
	return uuid.NewString() // v4 fallback; NewV7 only fails if crypto/rand does
}

func validRequestID(s string) bool {
	if len(s) > 128 {
		return false
	}
	for _, c := range s {
		ok := c == '-' || c == '_' || c == '.' || c == ':' ||
			('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

// fromTrusted reports whether remoteAddr (host:port) is inside a trusted CIDR.
func fromTrusted(remoteAddr string, trusted []netip.Prefix) bool {
	ap, err := netip.ParseAddrPort(remoteAddr)
	if err != nil {
		return false
	}
	addr := ap.Addr().Unmap()
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// Recover turns a panic into a 500 INTERNAL problem and logs the stack.
// http.ErrAbortHandler is re-panicked so net/http can abort the connection.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := wrap(w)
			ctx := r.Context()
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
					panic(v)
				}
				log.ErrorContext(ctx, "panic serving request",
					slog.String("panic", fmt.Sprint(v)), slog.String("stack", string(debug.Stack())))
				if !sw.wroteHeader {
					WriteError(sw, r, CodeInternal, "")
				}
			}()
			next.ServeHTTP(sw, r)
		})
	}
}

// Timeout bounds the request context. Handlers must honour ctx; outbound
// calls inherit the deadline.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// HTTPMetrics holds the RED metrics for one HTTP server.
type HTTPMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
}

// NewHTTPMetrics registers sluice_http_* metrics with reg. server labels the
// process role (api, worker, mockpay).
func NewHTTPMetrics(reg prometheus.Registerer, server string) *HTTPMetrics {
	constLabels := prometheus.Labels{"server": server}
	m := &HTTPMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: obs.Namespace, Subsystem: "http", Name: "requests_total",
			Help: "HTTP requests by route pattern, method and status code.", ConstLabels: constLabels,
		}, []string{"route", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: obs.Namespace, Subsystem: "http", Name: "request_duration_seconds",
			Help:        "HTTP request latency by route pattern and method.",
			ConstLabels: constLabels,
			Buckets:     []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"route", "method"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: obs.Namespace, Subsystem: "http", Name: "requests_in_flight",
			Help: "HTTP requests currently being served.", ConstLabels: constLabels,
		}),
	}
	reg.MustRegister(m.requests, m.duration, m.inFlight)
	return m
}

// quietRoutes are logged at debug level: probes and scrapes would drown the log.
var quietRoutes = map[string]bool{
	"GET /healthz": true, "GET /readyz": true, "GET /metrics": true, "GET /api/v1/healthz": true,
}

// Observe records RED metrics and writes one access-log line per request.
// It must sit inside RequestID (so logs carry request_id) and outside the
// router (so it can read the matched route).
func Observe(log *slog.Logger, m *HTTPMetrics) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ctx, route := withRouteHolder(r.Context())
			sw := wrap(w)
			m.inFlight.Inc()
			defer func() {
				m.inFlight.Dec()
				elapsed := time.Since(start)
				method := normMethod(r.Method)
				m.requests.WithLabelValues(route.pattern, method, strconv.Itoa(sw.status)).Inc()
				m.duration.WithLabelValues(route.pattern, method).Observe(elapsed.Seconds())

				level := slog.LevelInfo
				switch {
				case sw.status >= 500:
					level = slog.LevelError
				case quietRoutes[route.pattern]:
					level = slog.LevelDebug
				}
				// Path only: query strings are never logged.
				log.LogAttrs(ctx, level, "http request",
					slog.String("method", r.Method),
					slog.String("route", route.pattern),
					slog.String("path", r.URL.Path),
					slog.Int("status", sw.status),
					slog.Int64("bytes", sw.bytes),
					slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
				)
			}()
			next.ServeHTTP(sw, r.WithContext(ctx))
		})
	}
}

func normMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	default:
		return "OTHER" // bound label cardinality
	}
}

var (
	corsAllowMethods  = strings.Join([]string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions}, ", ")
	corsAllowHeaders  = "Authorization, Content-Type, Idempotency-Key, If-None-Match, X-Request-Id"
	corsExposeHeaders = "X-Request-Id, Retry-After, Idempotent-Replayed, ETag, RateLimit, RateLimit-Policy"
)

// CORS allows the listed origins (exact match). Preflight requests are
// answered here with 204 and never reach the router.
func CORS(origins []string) Middleware {
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			ok := origin != "" && allowed[origin]
			if ok {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				if ok {
					h.Set("Access-Control-Allow-Methods", corsAllowMethods)
					h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
					h.Set("Access-Control-Max-Age", "600")
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
