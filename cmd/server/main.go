package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ashishsinghbhadoria/goLearn/internal/app"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/logger"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

func main() {
	add := flag.Bool("add", false, "Add a new user interactively")
	remove := flag.Bool("remove", false, "Remove a user by ID interactively")
	list := flag.Bool("list", false, "List all users")
	dataPath := flag.String("data", ".data/users.json", "Path to the user data file")
	storageType := flag.String("storage", "jsonfile", "Storage strategy (memory, jsonfile)")
	flag.Parse()

	log := logger.NewLogger()
	metricsCollector := metrics.New()

	// Use factory pattern to create the appropriate repository
	cfg := app.RepositoryConfig{
		Type:     app.RepositoryType(*storageType),
		JSONPath: *dataPath,
		Logger:   log,
	}
	repository, err := app.NewRepository(cfg)
	if err != nil {
		log.Error("failed to initialize user repository", "error", err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	service := user.NewService(repository, log, metricsCollector)

	if *add {
		if err := addUserInteractive(service); err != nil {
			os.Exit(1)
		}
		return
	}

	if *remove {
		if err := removeUserInteractive(service); err != nil {
			os.Exit(1)
		}
		return
	}

	if *list {
		if err := listUsers(service); err != nil {
			os.Exit(1)
		}
		return
	}

	printUsage()
}

func addUserInteractive(service *user.Service) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Enter user name: ")
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)

	fmt.Print("Enter user email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)

	fmt.Printf("Enter password (min %d chars): ", user.MinPasswordLen)
	password, _ := reader.ReadString('\n')
	password = strings.TrimSpace(password)

	addedUser, err := service.Register(context.Background(), name, email, password)
	if err != nil {
		service.Logger().Error("failed to add user", "error", err, "user_id", "n/a")
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return err
	}

	service.Logger().Info("user added successfully", "user_id", addedUser.ID)
	fmt.Printf("✓ Added user %s <%s> with id %s\n", addedUser.Name, addedUser.Email, addedUser.ID)
	return nil
}

func removeUserInteractive(service *user.Service) error {
	// First list all users
	users, err := service.ListUsers()
	if err != nil {
		service.Logger().Error("failed to list users", "error", err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return err
	}

	if len(users) == 0 {
		fmt.Println("No users found to remove")
		return nil
	}

	fmt.Println("Available users:")
	for _, u := range users {
		fmt.Printf("  %s: %s (%s)\n", u.ID, u.Name, u.Email)
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\nEnter user ID to remove: ")
	userID, _ := reader.ReadString('\n')
	userID = strings.TrimSpace(userID)

	// Confirm deletion
	fmt.Print("Are you sure? (yes/no): ")
	confirm, _ := reader.ReadString('\n')
	confirm = strings.TrimSpace(strings.ToLower(confirm))
	if confirm != "yes" && confirm != "y" {
		fmt.Println("Cancelled")
		return nil
	}

	if err := service.RemoveUser(context.Background(), userID); err != nil {
		service.Logger().Error("failed to remove user", "error", err, "user_id", userID)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return err
	}

	service.Logger().Info("user removed successfully", "user_id", userID)
	fmt.Printf("✓ Removed user with id %s\n", userID)
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
	fmt.Println("  go run ./cmd/server --add          Add a new user (interactive)")
	fmt.Println("  go run ./cmd/server --remove       Remove a user (interactive)")
	fmt.Println("  go run ./cmd/server --list         List all users")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --storage=<type>    Storage strategy: memory or jsonfile (default: jsonfile)")
	fmt.Println("  --data=<path>       Path to user data file (default: .data/users.json)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  go run ./cmd/server --add")
	fmt.Println("  go run ./cmd/server --remove")
	fmt.Println("  go run ./cmd/server --list")
	fmt.Println("  go run ./cmd/server --storage=memory --list")
}
