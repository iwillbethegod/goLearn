package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ashishsinghbhadoria/goLearn/internal/storage/jsonfile"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/logger"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

func main() {
	addName := flag.String("name", "", "Name of the user to add")
	addEmail := flag.String("email", "", "Email of the user to add")
	list := flag.Bool("list", false, "List all users")
	dataPath := flag.String("data", ".data/users.json", "Path to the user data file")
	flag.Parse()

	log := logger.NewLogger()
	metricsCollector := metrics.New()
	repository, err := jsonfile.NewUserRepo(*dataPath, log)
	if err != nil {
		log.Error("failed to initialize user repository", "error", err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	service := user.NewService(repository, log, metricsCollector)

	if *list {
		if err := listUsers(service); err != nil {
			os.Exit(1)
		}
		return
	}

	if *addName != "" || *addEmail != "" {
		if err := addUser(service, context.Background(), *addName, *addEmail); err != nil {
			os.Exit(1)
		}
		return
	}

	printUsage()
}

func addUser(service *user.Service, ctx context.Context, name, email string) error {
	addedUser, err := service.AddUser(ctx, name, email)
	if err != nil {
		service.Logger().Error("failed to add user", "error", err, "user_id", "n/a")
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return err
	}

	service.Logger().Info("user added successfully", "user_id", addedUser.ID)
	fmt.Printf("Added user %s <%s> with id %s\n", addedUser.Name, addedUser.Email, addedUser.ID)
	return nil
}

func listUsers(service *user.Service) error {
	users, err := service.ListUsers()
	if err != nil {
		service.Logger().Error("failed to list users", "error", err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return err
	}

	if len(users) == 0 {
		fmt.Println("No users found")
		return nil
	}

	fmt.Println("Users:")
	for _, u := range users {
		fmt.Printf("- %s (%s) id=%s\n", u.Name, u.Email, u.ID)
	}
	return nil
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  go run ./cmd/server --list")
	fmt.Println("  go run ./cmd/server --name=\"Alice\" --email=\"alice@example.com\"")
	fmt.Println("  go run ./cmd/server --data=\".data/users.json\" --list")
}
