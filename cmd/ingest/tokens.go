package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	userpb "github.com/ashishsinghbhadoria/goLearn/proto/gen/userpb"
)

// errInsufficientTokens is the sentinel returned when the user
// service denies a TakeTokens request. It satisfies the FileGate
// contract's "skip this file" signal.
var errInsufficientTokens = errors.New("insufficient tokens")

// tokenGate implements ingest.FileGate against the day-4 gRPC
// UserService. Each file is pre-charged for its data-row count;
// after the runner finishes we refund (reserved − handled) tokens
// so cancelled or errored rows don't permanently bill the user.
type tokenGate struct {
	cli    userpb.UserServiceClient
	userID string
	logger *slog.Logger
}

func newTokenGate(cli userpb.UserServiceClient, userID string, logger *slog.Logger) *tokenGate {
	return &tokenGate{cli: cli, userID: userID, logger: logger}
}

func (g *tokenGate) BeforeFile(ctx context.Context, path string) (int64, error) {
	rows, err := countDataRows(path)
	if err != nil {
		return 0, fmt.Errorf("count rows %s: %w", path, err)
	}
	if rows <= 0 {
		// Nothing to bill — file is empty or just a header.
		return 0, nil
	}
	resp, err := g.cli.TakeTokens(ctx, &userpb.TakeTokensRequest{
		UserId: g.userID,
		Count:  rows,
	})
	if err != nil {
		return 0, fmt.Errorf("TakeTokens: %w", err)
	}
	if !resp.GetGranted() {
		g.logger.Warn("tokens denied",
			"file", path,
			"requested", rows,
			"available", resp.GetRemaining(),
		)
		return 0, errInsufficientTokens
	}
	g.logger.Info("tokens reserved",
		"file", path,
		"rows", rows,
		"remaining", resp.GetRemaining(),
	)
	return rows, nil
}

func (g *tokenGate) AfterFile(ctx context.Context, path string, reserved, handled int64) {
	if reserved <= handled {
		return // nothing to refund
	}
	refund := reserved - handled
	resp, err := g.cli.ReturnTokens(ctx, &userpb.ReturnTokensRequest{
		UserId: g.userID,
		Count:  refund,
	})
	if err != nil {
		g.logger.Warn("ReturnTokens failed", "file", path, "refund", refund, "err", err)
		return
	}
	g.logger.Info("tokens refunded",
		"file", path,
		"refund", refund,
		"remaining", resp.GetRemaining(),
	)
}

// countDataRows scans the file once with bufio.Scanner and returns
// the number of CSV data rows (header excluded). For a 1 MiB file
// this is ~1 ms — negligible relative to the per-record processing
// cost. We use a 4 MiB scanner buffer to tolerate long quoted CSV
// fields that exceed the default 64 KiB.
func countDataRows(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<22)

	var lines int64
	for sc.Scan() {
		lines++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if lines <= 0 {
		return 0, nil
	}
	return lines - 1, nil // subtract header
}
