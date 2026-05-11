package httpapi_test

// This file exercises the cross-protocol cohesion contract: a user
// registered via the REST API must be visible (and operable) via the
// gRPC UserService that shares the same user.Service + tokens.Store.
// It complements the in-package handler_test.go (REST only) and the
// transport/grpc/server_test.go (gRPC only) by gluing them together.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/ashishsinghbhadoria/goLearn/internal/storage/memory"
	"github.com/ashishsinghbhadoria/goLearn/internal/tokens"
	grpctransport "github.com/ashishsinghbhadoria/goLearn/internal/transport/grpc"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi"
	httpgen "github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi/gen"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
	userpb "github.com/ashishsinghbhadoria/goLearn/proto/gen/userpb"
)

// crossSetup wires both transports against a single user.Service and
// tokens.Store, mirroring how cmd/api boots in production.
type crossSetup struct {
	rest     *httptest.Server
	grpcCli  userpb.UserServiceClient
	grpcSrv  *grpc.Server
	tokenCfg tokens.Config
}

func newCrossSetup(t *testing.T) *crossSetup {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := memory.NewUserRepo(logger)
	svc := user.NewService(repo, logger, metrics.New())
	cfg := tokens.Config{Capacity: 1000, RatePerMin: 0} // disable refill for deterministic math
	store := tokens.NewStore(cfg)

	// REST.
	mux := http.NewServeMux()
	httpgen.HandlerWithOptions(httpapi.NewHandler(svc, logger), httpgen.StdHTTPServerOptions{BaseRouter: mux})
	rest := httptest.NewServer(mux)
	t.Cleanup(rest.Close)

	// gRPC over bufconn.
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	userpb.RegisterUserServiceServer(grpcSrv, grpctransport.NewServer(svc, store, logger))
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &crossSetup{
		rest:     rest,
		grpcCli:  userpb.NewUserServiceClient(conn),
		grpcSrv:  grpcSrv,
		tokenCfg: cfg,
	}
}

// TestRESTRegisterVisibleViaGRPC asserts that a user POSTed to the
// REST endpoint is immediately retrievable via gRPC GetUser /
// ListUsers, and that the bucket created for that user starts at the
// configured capacity.
func TestRESTRegisterVisibleViaGRPC(t *testing.T) {
	s := newCrossSetup(t)

	body := `{"name":"Alice","email":"alice@example.com","password":"secret123"}`
	resp, err := http.Post(s.rest.URL+"/users", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d, want 201", resp.StatusCode)
	}
	var created struct{ Id, Name, Email string }
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Id == "" || !strings.HasPrefix(created.Id, "u-") {
		t.Fatalf("bad id: %q", created.Id)
	}

	// Same user via gRPC.
	got, err := s.grpcCli.GetUser(context.Background(), &userpb.GetUserRequest{Id: created.Id})
	if err != nil {
		t.Fatalf("grpc GetUser: %v", err)
	}
	if got.GetEmail() != "alice@example.com" || got.GetName() != "Alice" {
		t.Fatalf("grpc GetUser returned %+v", got)
	}
	if got.GetId() != created.Id {
		t.Fatalf("grpc id mismatch: rest=%s grpc=%s", created.Id, got.GetId())
	}

	// gRPC ListUsers also sees Alice.
	list, err := s.grpcCli.ListUsers(context.Background(), &userpb.ListUsersRequest{})
	if err != nil {
		t.Fatalf("grpc ListUsers: %v", err)
	}
	if list.GetTotal() != 1 || len(list.GetUsers()) != 1 {
		t.Fatalf("grpc ListUsers total=%d len=%d", list.GetTotal(), len(list.GetUsers()))
	}
	if list.GetUsers()[0].GetEmail() != "alice@example.com" {
		t.Fatalf("grpc ListUsers email mismatch: %+v", list.GetUsers()[0])
	}

	// Bucket starts full at the configured capacity.
	tk, err := s.grpcCli.GetTokens(context.Background(), &userpb.GetTokensRequest{UserId: created.Id})
	if err != nil {
		t.Fatalf("GetTokens: %v", err)
	}
	if tk.GetAvailable() != s.tokenCfg.Capacity || tk.GetCapacity() != s.tokenCfg.Capacity {
		t.Fatalf("bucket: avail=%d cap=%d, want %d/%d",
			tk.GetAvailable(), tk.GetCapacity(), s.tokenCfg.Capacity, s.tokenCfg.Capacity)
	}
}

// TestTokenLifecycleViaGRPC asserts the Take → Return round-trip on a
// real registered user. With RatePerMin=0 the bucket doesn't refill,
// so we can assert exact balances.
func TestTokenLifecycleViaGRPC(t *testing.T) {
	s := newCrossSetup(t)

	body := `{"name":"Bob","email":"bob@example.com","password":"secret123"}`
	resp, err := http.Post(s.rest.URL+"/users", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	var created struct{ Id string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	// Take 300 of 1000.
	take, err := s.grpcCli.TakeTokens(context.Background(), &userpb.TakeTokensRequest{UserId: created.Id, Count: 300})
	if err != nil {
		t.Fatalf("TakeTokens: %v", err)
	}
	if !take.GetGranted() {
		t.Fatal("Take(300) should be granted")
	}
	if take.GetRemaining() != 700 {
		t.Fatalf("after Take(300): remaining=%d, want 700", take.GetRemaining())
	}

	// Take 800 — should be denied; bucket unchanged.
	denied, err := s.grpcCli.TakeTokens(context.Background(), &userpb.TakeTokensRequest{UserId: created.Id, Count: 800})
	if err != nil {
		t.Fatalf("TakeTokens(denied): %v", err)
	}
	if denied.GetGranted() {
		t.Fatal("Take(800) should have been denied")
	}
	if denied.GetRemaining() != 700 {
		t.Fatalf("after denied Take: remaining=%d, want 700 (unchanged)", denied.GetRemaining())
	}

	// Return 200 — bucket back to 900.
	ret, err := s.grpcCli.ReturnTokens(context.Background(), &userpb.ReturnTokensRequest{UserId: created.Id, Count: 200})
	if err != nil {
		t.Fatalf("ReturnTokens: %v", err)
	}
	if ret.GetRemaining() != 900 {
		t.Fatalf("after Return(200): remaining=%d, want 900", ret.GetRemaining())
	}

	// Return 500 more — capped at capacity (1000), not 1400.
	ret2, err := s.grpcCli.ReturnTokens(context.Background(), &userpb.ReturnTokensRequest{UserId: created.Id, Count: 500})
	if err != nil {
		t.Fatalf("ReturnTokens overflow: %v", err)
	}
	if ret2.GetRemaining() != s.tokenCfg.Capacity {
		t.Fatalf("after overflow Return: remaining=%d, want %d", ret2.GetRemaining(), s.tokenCfg.Capacity)
	}
}

// TestRESTUpdateVisibleViaGRPC asserts that a PUT /users/{id} on the
// REST side is immediately reflected in the gRPC GetUser response —
// proving both transports really share state.
func TestRESTUpdateVisibleViaGRPC(t *testing.T) {
	s := newCrossSetup(t)

	body := `{"name":"Carol","email":"carol@example.com","password":"secret123"}`
	resp, _ := http.Post(s.rest.URL+"/users", "application/json", strings.NewReader(body))
	var created struct{ Id string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	// Rename via REST.
	put := `{"name":"Carol Renamed"}`
	req, _ := http.NewRequest(http.MethodPut, s.rest.URL+"/users/"+created.Id, strings.NewReader(put))
	req.Header.Set("Content-Type", "application/json")
	rresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d", rresp.StatusCode)
	}

	// gRPC immediately sees the new name.
	got, err := s.grpcCli.GetUser(context.Background(), &userpb.GetUserRequest{Id: created.Id})
	if err != nil {
		t.Fatalf("grpc GetUser after PUT: %v", err)
	}
	if got.GetName() != "Carol Renamed" {
		t.Fatalf("grpc still sees old name: %+v", got)
	}
}

// TestUnknownUserGetTokensCreatesEmptyBucket confirms the gRPC token
// store's lazy-create semantics: GetTokens for a user that exists in
// neither the REST store nor the bucket map returns a fresh bucket at
// capacity. (Token operations are deliberately decoupled from the
// user repo so file processors can pre-warm.)
func TestUnknownUserGetTokensCreatesEmptyBucket(t *testing.T) {
	s := newCrossSetup(t)
	tk, err := s.grpcCli.GetTokens(context.Background(), &userpb.GetTokensRequest{UserId: "u-unknown"})
	if err != nil {
		t.Fatalf("GetTokens: %v", err)
	}
	if tk.GetAvailable() != s.tokenCfg.Capacity {
		t.Fatalf("unknown user bucket: avail=%d, want %d", tk.GetAvailable(), s.tokenCfg.Capacity)
	}
}
