package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func TestHealth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name       string
		path       string
		dbErr      error
		wantStatus int
		wantBody   string
	}{
		{"root ok", "/healthz", nil, http.StatusOK, `"status":"ok"`},
		{"v1 ok", "/v1/healthz", nil, http.StatusOK, `"status":"ok"`},
		{"database down", "/v1/healthz", errors.New("down"), http.StatusServiceUnavailable, `"status":"degraded"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)

			NewRouter(logger, fakeDB{err: tt.dbErr}).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}
