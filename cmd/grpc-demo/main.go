// Package main is a tiny standalone client for the day-4
// UserService. It demonstrates the four steps every gRPC client
// goes through: dial, build the typed client, call a unary RPC,
// shut the connection. Useful as a smoke test (`grpc-demo -tokens
// -user u-...`) and as a reference for what the cmd/ingest token
// gate is doing under the hood.
//
// Usage:
//
//	go run ./cmd/grpc-demo -addr :9090 -user <id>            # GetTokens
//	go run ./cmd/grpc-demo -addr :9090 -list                 # ListUsers
//	go run ./cmd/grpc-demo -addr :9090 -get <id>             # GetUser
//	go run ./cmd/grpc-demo -addr :9090 -take <id> -count 100 # TakeTokens
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	userpb "github.com/ashishsinghbhadoria/goLearn/proto/gen/userpb"
)

func main() {
	addr := flag.String("addr", ":9090", "gRPC server address")
	user := flag.String("user", "", "user ID (for -tokens / -take / -return)")
	get := flag.String("get", "", "fetch user by ID via GetUser")
	list := flag.Bool("list", false, "list users via ListUsers")
	take := flag.Bool("take", false, "TakeTokens(-count) for -user")
	rtn := flag.Bool("return", false, "ReturnTokens(-count) for -user")
	count := flag.Int64("count", 100, "token count for -take / -return")
	timeout := flag.Duration("timeout", 5*time.Second, "per-RPC timeout")
	flag.Parse()

	conn, err := grpc.NewClient(*addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		fail("dial %s: %v", *addr, err)
	}
	defer conn.Close()
	cli := userpb.NewUserServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	switch {
	case *list:
		runList(ctx, cli)
	case *get != "":
		runGet(ctx, cli, *get)
	case *take:
		needUser(*user)
		runTake(ctx, cli, *user, *count)
	case *rtn:
		needUser(*user)
		runReturn(ctx, cli, *user, *count)
	case *user != "":
		runGetTokens(ctx, cli, *user)
	default:
		fail("usage: grpc-demo [-list | -get <id> | -user <id> | -take -user <id> -count N | -return -user <id> -count N]")
	}
}

func runGetTokens(ctx context.Context, cli userpb.UserServiceClient, userID string) {
	resp, err := cli.GetTokens(ctx, &userpb.GetTokensRequest{UserId: userID})
	if err != nil {
		fail("GetTokens: %v", err)
	}
	fmt.Printf("user=%s available=%d capacity=%d\n", userID, resp.Available, resp.Capacity)
}

func runTake(ctx context.Context, cli userpb.UserServiceClient, userID string, count int64) {
	resp, err := cli.TakeTokens(ctx, &userpb.TakeTokensRequest{UserId: userID, Count: count})
	if err != nil {
		fail("TakeTokens: %v", err)
	}
	fmt.Printf("user=%s requested=%d granted=%v remaining=%d\n", userID, count, resp.Granted, resp.Remaining)
}

func runReturn(ctx context.Context, cli userpb.UserServiceClient, userID string, count int64) {
	resp, err := cli.ReturnTokens(ctx, &userpb.ReturnTokensRequest{UserId: userID, Count: count})
	if err != nil {
		fail("ReturnTokens: %v", err)
	}
	fmt.Printf("user=%s returned=%d remaining=%d\n", userID, count, resp.Remaining)
}

func runGet(ctx context.Context, cli userpb.UserServiceClient, id string) {
	u, err := cli.GetUser(ctx, &userpb.GetUserRequest{Id: id})
	if err != nil {
		fail("GetUser: %v", err)
	}
	fmt.Printf("id=%s name=%s email=%s\n", u.Id, u.Name, u.Email)
}

func runList(ctx context.Context, cli userpb.UserServiceClient) {
	resp, err := cli.ListUsers(ctx, &userpb.ListUsersRequest{Limit: 100, Offset: 0})
	if err != nil {
		fail("ListUsers: %v", err)
	}
	fmt.Printf("total=%d page=%d\n", resp.Total, len(resp.Users))
	for _, u := range resp.Users {
		fmt.Printf("  id=%s name=%s email=%s\n", u.Id, u.Name, u.Email)
	}
}

func needUser(userID string) {
	if userID == "" {
		fail("-user <id> is required for this operation")
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
