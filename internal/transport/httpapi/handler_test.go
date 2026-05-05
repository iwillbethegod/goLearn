package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/memory"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi/gen"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

// newTestServer builds an in-process HTTP server with the same routing
// the production server uses. The validator middleware is intentionally
// omitted: these tests cover the handler's own behavior, including
// error mapping for malformed bodies that the validator would
// otherwise short-circuit.
func newTestServer(t *testing.T) (*httptest.Server, *user.Service) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := memory.NewUserRepo(logger)
	svc := user.NewService(repo, logger, metrics.New())
	h := NewHandler(svc, logger)

	mux := http.NewServeMux()
	gen.HandlerWithOptions(h, gen.StdHTTPServerOptions{BaseRouter: mux})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, svc
}

func mustRegister(t *testing.T, svc *user.Service, name, email, password string) model.User {
	t.Helper()
	u, err := svc.Register(context.Background(), name, email, password)
	if err != nil {
		t.Fatalf("register %s: %v", email, err)
	}
	return u
}

func decodeJSON(t *testing.T, r io.Reader, target any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(target); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestListUsers_EmptyAndPagination(t *testing.T) {
	srv, svc := newTestServer(t)

	resp, err := http.Get(srv.URL + "/users")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Total-Count"); got != "0" {
		t.Fatalf("X-Total-Count=%q want 0", got)
	}
	var page []gen.User
	decodeJSON(t, resp.Body, &page)
	if len(page) != 0 {
		t.Fatalf("expected empty page, got %d", len(page))
	}

	// Seed 5 users and try a page of 2 starting at offset 1.
	for i := 0; i < 5; i++ {
		mustRegister(t, svc, "User", "u"+string(rune('a'+i))+"@example.com", "secret123")
	}
	resp, err = http.Get(srv.URL + "/users?limit=2&offset=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Total-Count"); got != "5" {
		t.Fatalf("X-Total-Count=%q want 5", got)
	}
	var page2 []gen.User
	decodeJSON(t, resp.Body, &page2)
	if len(page2) != 2 {
		t.Fatalf("expected 2 users, got %d", len(page2))
	}
}

func TestCreateUser_Created(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"name":"Alice","email":"alice@example.com","password":"secret123"}`
	resp, err := http.Post(srv.URL+"/users", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/users/u-") {
		t.Fatalf("Location=%q", loc)
	}
	var got gen.User
	decodeJSON(t, resp.Body, &got)
	if got.Name != "Alice" || string(got.Email) != "alice@example.com" {
		t.Fatalf("unexpected body: %+v", got)
	}
	// password_hash must never appear in the response.
	raw, _ := json.Marshal(got)
	if bytes.Contains(raw, []byte("password")) {
		t.Fatal("response leaked a password-related field")
	}
}

func TestCreateUser_DuplicateConflict(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"name":"A","email":"a@example.com","password":"secret123"}`
	first, _ := http.Post(srv.URL+"/users", "application/json", strings.NewReader(body))
	first.Body.Close()

	resp, err := http.Post(srv.URL+"/users", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want 409", resp.StatusCode)
	}
	var e gen.Error
	decodeJSON(t, resp.Body, &e)
	if e.Code != gen.DuplicateUser {
		t.Fatalf("code=%q want %q", e.Code, gen.DuplicateUser)
	}
}

func TestCreateUser_MalformedJSON(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Post(srv.URL+"/users", "application/json", strings.NewReader(`{not-json`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

func TestGetUser_OKAndNotFound(t *testing.T) {
	srv, svc := newTestServer(t)
	u := mustRegister(t, svc, "Bob", "bob@example.com", "secret123")

	resp, err := http.Get(srv.URL + "/users/" + u.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + "/users/u-deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp2.StatusCode)
	}
	var e gen.Error
	decodeJSON(t, resp2.Body, &e)
	if e.Code != gen.UserNotFound {
		t.Fatalf("code=%q want %q", e.Code, gen.UserNotFound)
	}
}

func TestUpdateUser_RenameAndCollision(t *testing.T) {
	srv, svc := newTestServer(t)
	a := mustRegister(t, svc, "A", "a@example.com", "secret123")
	mustRegister(t, svc, "B", "b@example.com", "secret123")

	// Rename A.
	body := `{"name":"A Renamed"}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/users/"+a.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	// Try to take B's email.
	body2 := `{"email":"b@example.com"}`
	req2, _ := http.NewRequest(http.MethodPut, srv.URL+"/users/"+a.ID, strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want 409", resp2.StatusCode)
	}
}

func TestDeleteUser_NoContentThenNotFound(t *testing.T) {
	srv, svc := newTestServer(t)
	u := mustRegister(t, svc, "C", "c@example.com", "secret123")

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/users/"+u.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d, want 204", resp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + "/users/" + u.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp2.StatusCode)
	}
}

func TestPasswordHashNeverInResponse(t *testing.T) {
	srv, svc := newTestServer(t)
	mustRegister(t, svc, "Eve", "eve@example.com", "secret123")

	resp, err := http.Get(srv.URL + "/users")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(bytes.ToLower(body), []byte("password")) {
		t.Fatalf("password leaked in list response: %s", body)
	}
}

// Sanity check that the gen package surfaces the symbols the tests
// rely on; if oapi-codegen ever renames them, this fails fast.
func TestGenSymbolsExist(t *testing.T) {
	if !errors.Is(error(nil), error(nil)) {
		t.Fatal("standard library is broken")
	}
	_ = gen.User{}
	_ = gen.Error{}
	_ = gen.ListUsersParams{}
}
