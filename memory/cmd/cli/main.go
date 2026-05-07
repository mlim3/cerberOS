package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

// Global flags
var (
	dbURL  = flag.String("db", os.Getenv("DATABASE_URL"), "Database connection string (e.g. postgres://user:pass@localhost:5432/db)")
	apiURL = flag.String("api", os.Getenv("API_URL"), "HTTP API URL (e.g. http://localhost:8080)")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <command> [subcommands]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  facts    Manage and query user facts\n")
		fmt.Fprintf(os.Stderr, "  chat     View chat history\n")
		fmt.Fprintf(os.Stderr, "  agent    View agent task executions\n")
		fmt.Fprintf(os.Stderr, "  system   View system events\n")
		fmt.Fprintf(os.Stderr, "  vault    Manage user vault secrets (HTTP API only)\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	ctx := context.Background()
	cmd := flag.Arg(0)

	// Determine connection type
	// If both are empty, default to HTTP localhost
	if *dbURL == "" && *apiURL == "" {
		*apiURL = "http://localhost:8080"
	}

	var client MemoryClient
	var err error

	if *dbURL != "" {
		fmt.Fprintf(os.Stderr, "[Using Direct DB Connection]\n")
		client, err = NewDBClient(ctx, *dbURL)
	} else {
		fmt.Fprintf(os.Stderr, "[Using HTTP API Connection]\n")
		client, err = NewHTTPClient(*apiURL)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize client: %v\n", err)
		os.Exit(1)
	}

	// Route to subcommand
	switch cmd {
	case "facts":
		handleFactsCmd(ctx, client, flag.Args()[1:])
	case "chat":
		handleChatCmd(ctx, client, flag.Args()[1:])
	case "agent":
		handleAgentCmd(ctx, client, flag.Args()[1:])
	case "system":
		handleSystemCmd(ctx, client, flag.Args()[1:])
	case "vault":
		handleVaultCmd(ctx, client, flag.Args()[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		flag.Usage()
		os.Exit(1)
	}
}
