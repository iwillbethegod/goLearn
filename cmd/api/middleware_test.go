package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noopHandler is the "next" handler used when we want to assert
// behavior of the middleware in isolation.
var noopHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestWithRequestID_GeneratesIDWhenAbsent(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	withRequestID()(noopHandler).ServeHTTP(rec, req)

	got := rec.Header().Get("X-Request-Id")
	if len(got) != 16 { // 8 bytes hex
		t.Fatalf("X-Request-Id header = %q (len %d), want 16-char hex", got, len(got))
	}
}

func TestWithRequestID_PreservesProvidedID(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "client-supplied-id")
	withRequestID()(noopHandler).ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-Id"); got != "client-supplied-id" {
		t.Fatalf("X-Request-Id = %q, want client-supplied-id", got)
	}
}

func TestWithRequestID_PutsIDIntoCtx(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "ctx-test")

	captured := ""
	captureHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = requestID(r.Context())
	})
	withRequestID()(captureHandler).ServeHTTP(rec, req)

	if captured != "ctx-test" {
		t.Fatalf("requestID(ctx) = %q, want ctx-test", captured)
	}
}

// requestID(ctx) on a bare ctx must return "" (not panic, not a typed
// zero value masquerading as a real id).
func TestRequestID_OnBareContextReturnsEmpty(t *testing.T) {
	if got := requestID(context.Background()); got != "" {
		t.Fatalf("requestID(bare ctx) = %q, want empty", got)
	}
}

func TestWithBodyLimit_ClampsAtMaxBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	// 1 MiB + 1 byte body — read should stop just past the cap.
	body := bytes.NewReader(bytes.Repeat([]byte("a"), maxBodyBytes+1))
	req := httptest.NewRequest(http.MethodPost, "/", body)

	read := 0
	readHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		read = len(b)
	})
	withBodyLimit()(readHandler).ServeHTTP(rec, req)

	if read > maxBodyBytes {
		t.Fatalf("read %d bytes, body limit %d should have clamped", read, maxBodyBytes)
	}
}

func TestWithRecover_PanicBecomes500JSON(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelError}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	panicHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})

	withRecover(logger)(panicHandler).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var env map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if env["code"] != "internal_error" {
		t.Fatalf("code = %q, want internal_error", env["code"])
	}
	// Stack must NOT leak to client.
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("panic message leaked to client body: %q", rec.Body.String())
	}
	// But it MUST be in the slog output.
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("logger missed the panic value: %q", buf.String())
	}
}

func TestWithRecover_NoPanicPassesThrough(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})
	withRecover(logger)(h).ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusTeapot {
		t.Fatalf("handler not called / status mismatch: called=%v code=%d", called, rec.Code)
	}
}

func TestWithAccessLog_EmitsOneLineWithRequestFields(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/users/abc", nil)

	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	withAccessLog(logger)(h).ServeHTTP(rec, req)

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode log line: %v", err)
	}
	if got["method"] != "GET" || got["path"] != "/users/abc" || got["status"] != float64(201) {
		t.Fatalf("log line missing fields: %+v", got)
	}
}

// statusRecorder must report the status the handler actually wrote,
// not the default 200 — covers the gen.MiddlewareFunc adapter path.
func TestStatusRecorder_TracksWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	sr.WriteHeader(http.StatusForbidden)

	if sr.status != http.StatusForbidden {
		t.Fatalf("recorder status = %d, want 403", sr.status)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("delegate status = %d, want 403", rec.Code)
	}
}

func TestWriteJSONError_WritesEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusBadRequest, "validation_failed", "bad input")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json…", ct)
	}
	var env map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if env["code"] != "validation_failed" || env["message"] != "bad input" {
		t.Fatalf("envelope mismatch: %+v", env)
	}
}

func TestNewRequestID_Returns16HexChars(t *testing.T) {
	id := newRequestID()
	if len(id) != 16 {
		t.Fatalf("len(newRequestID()) = %d, want 16", len(id))
	}
	for _, c := range id {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			t.Fatalf("non-hex char %q in id %q", c, id)
		}
	}
}
