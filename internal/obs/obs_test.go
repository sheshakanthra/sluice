package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoggerAddsContextIDs(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want map[string]string
		none []string
	}{
		{"no ids", context.Background(), nil, []string{"request_id", "user_id", "event_id"}},
		{
			"all ids",
			WithEventID(WithUserID(WithRequestID(context.Background(), "req-1"), "user-1"), "evt-1"),
			map[string]string{"request_id": "req-1", "user_id": "user-1", "event_id": "evt-1"},
			nil,
		},
		{"empty id omitted", WithRequestID(context.Background(), ""), nil, []string{"request_id"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			NewLogger(&buf, slog.LevelInfo).With("svc", "api").InfoContext(tc.ctx, "hello")
			var rec map[string]any
			if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
				t.Fatalf("log line is not JSON: %v: %s", err, buf.String())
			}
			if rec["svc"] != "api" || rec["msg"] != "hello" {
				t.Errorf("base fields missing: %v", rec)
			}
			for k, v := range tc.want {
				if rec[k] != v {
					t.Errorf("%s = %v, want %s", k, rec[k], v)
				}
			}
			for _, k := range tc.none {
				if _, ok := rec[k]; ok {
					t.Errorf("%s should be absent: %v", k, rec)
				}
			}
		})
	}
}

func TestLoggerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(&buf, slog.LevelWarn)
	l.Info("dropped")
	l.Warn("kept")
	if strings.Contains(buf.String(), "dropped") || !strings.Contains(buf.String(), "kept") {
		t.Fatalf("level filtering broken: %s", buf.String())
	}
}

func TestRegistryServesRuntimeMetrics(t *testing.T) {
	rec := httptest.NewRecorder()
	MetricsHandler(NewRegistry()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Fatalf("metrics: %d %s", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
}

func TestPprofServer(t *testing.T) {
	if NewPprofServer("") != nil {
		t.Fatal("empty addr must disable pprof")
	}
	srv := NewPprofServer("127.0.0.1:0")
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("pprof index: %d", rec.Code)
	}
}
