package grpc

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/ashishsinghbhadoria/goLearn/internal/storage/memory"
	"github.com/ashishsinghbhadoria/goLearn/internal/tokens"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
	userpb "github.com/ashishsinghbhadoria/goLearn/proto/gen/userpb"
)

// newTestClient spins up an in-process gRPC server backed by a
// memory user repo and a fresh token store, then dials it over a
// bufconn pipe (no kernel sockets, no port allocation). Returns a
// client and the underlying user.Service for test setup.
func newTestClient(t *testing.T, capacity, ratePerMin int64) (userpb.UserServiceClient, *user.Service) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := memory.NewUserRepo(logger)
	svc := user.NewService(repo, logger, metrics.New())
	store := tokens.NewStore(tokens.Config{Capacity: capacity, RatePerMin: ratePerMin})

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	userpb.RegisterUserServiceServer(grpcSrv, NewServer(svc, store, logger))
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return userpb.NewUserServiceClient(conn), svc
}

func TestGetTokens_FullBucketOnFirstCall(t *testing.T) {
	cli, _ := newTestClient(t, 100, 0)
	resp, err := cli.GetTokens(context.Background(), &userpb.GetTokensRequest{UserId: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Available != 100 || resp.Capacity != 100 {
		t.Fatalf("got available=%d capacity=%d", resp.Available, resp.Capacity)
	}
}

func TestTakeTokens_GrantedAndDenied(t *testing.T) {
	cli, _ := newTestClient(t, 100, 0)
	ctx := context.Background()

	resp, err := cli.TakeTokens(ctx, &userpb.TakeTokensRequest{UserId: "alice", Count: 80})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Granted || resp.Remaining != 20 {
		t.Fatalf("first take: granted=%v remaining=%d", resp.Granted, resp.Remaining)
	}

	resp, err = cli.TakeTokens(ctx, &userpb.TakeTokensRequest{UserId: "alice", Count: 50})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Granted || resp.Remaining != 20 {
		t.Fatalf("second take: granted=%v remaining=%d, want denied with 20", resp.Granted, resp.Remaining)
	}
}

func TestReturnTokens_RefundsButCaps(t *testing.T) {
	cli, _ := newTestClient(t, 100, 0)
	ctx := context.Background()
	_, _ = cli.TakeTokens(ctx, &userpb.TakeTokensRequest{UserId: "alice", Count: 60})

	resp, err := cli.ReturnTokens(ctx, &userpb.ReturnTokensRequest{UserId: "alice", Count: 40})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Remaining != 80 {
		t.Fatalf("after Return(40): remaining=%d want 80", resp.Remaining)
	}

	// Over-return must not exceed capacity.
	resp, _ = cli.ReturnTokens(ctx, &userpb.ReturnTokensRequest{UserId: "alice", Count: 1000})
	if resp.Remaining != 100 {
		t.Fatalf("after Return(1000): remaining=%d want capped at 100", resp.Remaining)
	}
}

func TestPerUserIsolation(t *testing.T) {
	cli, _ := newTestClient(t, 100, 0)
	ctx := context.Background()
	_, _ = cli.TakeTokens(ctx, &userpb.TakeTokensRequest{UserId: "alice", Count: 100})

	// Bob still has a fresh bucket.
	resp, err := cli.GetTokens(ctx, &userpb.GetTokensRequest{UserId: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Available != 100 {
		t.Fatalf("bob.available=%d, want 100 (per-user isolation)", resp.Available)
	}
}

func TestGetUser_NotFoundIsTypedStatus(t *testing.T) {
	cli, _ := newTestClient(t, 100, 0)
	_, err := cli.GetUser(context.Background(), &userpb.GetUserRequest{Id: "u-deadbeef"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected grpc status, got %v", err)
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("code=%v, want NotFound", st.Code())
	}
}

func TestListUsers_PaginationAndPasswordHashScrubbed(t *testing.T) {
	cli, svc := newTestClient(t, 100, 0)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := svc.Register(ctx, "User", "u"+string(rune('a'+i))+"@example.com", "secret123")
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	resp, err := cli.ListUsers(ctx, &userpb.ListUsersRequest{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 3 || len(resp.Users) != 2 {
		t.Fatalf("total=%d page=%d, want total=3 page=2", resp.Total, len(resp.Users))
	}
	// .proto User has no password_hash field, so this is enforced by
	// the schema. Sanity-check the field set anyway.
	for _, u := range resp.Users {
		if u.GetId() == "" || u.GetEmail() == "" {
			t.Fatalf("missing id/email on returned user: %+v", u)
		}
	}
}

func TestInvalidArgument(t *testing.T) {
	cli, _ := newTestClient(t, 100, 0)
	_, err := cli.TakeTokens(context.Background(), &userpb.TakeTokensRequest{UserId: ""})
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("code=%v, want InvalidArgument", st.Code())
	}
}
