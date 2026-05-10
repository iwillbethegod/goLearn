package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

func TestGrpcAccessLog_LogsMethodDurationAndNilErr(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	interceptor := grpcAccessLog(logger)
	resp, err := interceptor(
		context.Background(),
		"req",
		&grpc.UnaryServerInfo{FullMethod: "/user.v1.UserService/GetUser"},
		func(_ context.Context, _ any) (any, error) { return "ok", nil },
	)
	if err != nil {
		t.Fatalf("unexpected interceptor err: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("resp = %v, want ok", resp)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if got["method"] != "/user.v1.UserService/GetUser" {
		t.Fatalf("method = %v, want /user.v1.UserService/GetUser", got["method"])
	}
	if got["msg"] != "grpc" {
		t.Fatalf("msg = %v, want grpc", got["msg"])
	}
	if _, ok := got["dur"]; !ok {
		t.Fatalf("dur missing from log line: %+v", got)
	}
}

func TestGrpcAccessLog_LogsHandlerError(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	want := errors.New("downstream-failed")
	interceptor := grpcAccessLog(logger)
	resp, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/user.v1.UserService/AddUser"},
		func(_ context.Context, _ any) (any, error) { return nil, want },
	)
	if !errors.Is(err, want) {
		t.Fatalf("interceptor must propagate handler err, got %v", err)
	}
	if resp != nil {
		t.Fatalf("resp = %v, want nil on err", resp)
	}
	// The error string MUST appear in the log line so on-call can grep.
	if !strings.Contains(buf.String(), "downstream-failed") {
		t.Fatalf("log line missing err: %q", buf.String())
	}
}
