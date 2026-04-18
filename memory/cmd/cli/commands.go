package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"
)

func handleFactsCmd(ctx context.Context, client MemoryClient, args []string) {
	fs := flag.NewFlagSet("facts", flag.ExitOnError)
	userIDStr := fs.String("user", "", "User UUID")
	topK := fs.Int("top", 5, "Number of results to return (for query)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: memory-cli facts <subcommand> [options]\n\n")
		fmt.Fprintf(os.Stderr, "Subcommands:\n")
		fmt.Fprintf(os.Stderr, "  query <query-string>  Semantic search for facts\n")
		fmt.Fprintf(os.Stderr, "  all                   Get all facts for user\n")
		fmt.Fprintf(os.Stderr, "  save <fact-string>    Save a new fact for user\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
	}

	if len(args) < 1 {
		fs.Usage()
		os.Exit(1)
	}

	subcmd := args[0]
	fs.Parse(args[1:])

	if *userIDStr == "" {
		fmt.Fprintf(os.Stderr, "Error: --user flag is required\n")
		fs.Usage()
		os.Exit(1)
	}

	userID, err := uuid.Parse(*userIDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid user UUID: %v\n", err)
		os.Exit(1)
	}

	switch subcmd {
	case "query":
		if fs.NArg() < 1 {
			fmt.Fprintf(os.Stderr, "Error: query string is required\n")
			fs.Usage()
			os.Exit(1)
		}
		query := fs.Arg(0)

		facts, err := client.QueryFacts(ctx, userID, query, *topK)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error querying facts: %v\n", err)
			os.Exit(1)
		}
		outputJSON(facts)

	case "all":
		facts, err := client.GetAllFacts(ctx, userID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting all facts: %v\n", err)
			os.Exit(1)
		}
		outputJSON(facts)

	case "save":
		if fs.NArg() < 1 {
			fmt.Fprintf(os.Stderr, "Error: fact string is required\n")
			fs.Usage()
			os.Exit(1)
		}
		factStr := fs.Arg(0)

		err := client.SaveFact(ctx, userID, factStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error saving fact: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "Fact saved successfully.\n")

	default:
		fmt.Fprintf(os.Stderr, "Unknown facts command: %s\n", subcmd)
		fs.Usage()
		os.Exit(1)
	}
}

func handleChatCmd(ctx context.Context, client MemoryClient, args []string) {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	sessionIDStr := fs.String("session", "", "Session UUID")
	limit := fs.Int("limit", 100, "Number of messages to return")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: memory-cli chat history [options]\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
	}

	if len(args) < 1 {
		fs.Usage()
		os.Exit(1)
	}

	subcmd := args[0]
	fs.Parse(args[1:])

	if *sessionIDStr == "" {
		fmt.Fprintf(os.Stderr, "Error: --session flag is required\n")
		fs.Usage()
		os.Exit(1)
	}

	sessionID, err := uuid.Parse(*sessionIDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid session UUID: %v\n", err)
		os.Exit(1)
	}

	switch subcmd {
	case "history":
		messages, err := client.GetChatHistory(ctx, sessionID, *limit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting chat history: %v\n", err)
			os.Exit(1)
		}

		outputJSON(messages)

	default:
		fmt.Fprintf(os.Stderr, "Unknown chat command: %s\n", subcmd)
		fs.Usage()
		os.Exit(1)
	}
}

func handleAgentCmd(ctx context.Context, client MemoryClient, args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	taskIDStr := fs.String("task", "", "Task UUID")
	limit := fs.Int("limit", 100, "Number of executions to return")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: memory-cli agent history [options]\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
	}

	if len(args) < 1 {
		fs.Usage()
		os.Exit(1)
	}

	subcmd := args[0]
	fs.Parse(args[1:])

	if *taskIDStr == "" {
		fmt.Fprintf(os.Stderr, "Error: --task flag is required\n")
		fs.Usage()
		os.Exit(1)
	}

	taskID, err := uuid.Parse(*taskIDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid task UUID: %v\n", err)
		os.Exit(1)
	}

	switch subcmd {
	case "history":
		executions, err := client.GetAgentExecutions(ctx, taskID, *limit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting agent history: %v\n", err)
			os.Exit(1)
		}

		outputJSON(executions)

	default:
		fmt.Fprintf(os.Stderr, "Unknown agent command: %s\n", subcmd)
		fs.Usage()
		os.Exit(1)
	}
}

func handleSystemCmd(ctx context.Context, client MemoryClient, args []string) {
	fs := flag.NewFlagSet("system", flag.ExitOnError)
	limit := fs.Int("limit", 100, "Number of events to return")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: memory-cli system events [options]\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
	}

	if len(args) < 1 {
		fs.Usage()
		os.Exit(1)
	}

	subcmd := args[0]
	fs.Parse(args[1:])

	switch subcmd {
	case "events":
		events, err := client.GetSystemEvents(ctx, *limit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting system events: %v\n", err)
			os.Exit(1)
		}

		outputJSON(events)

	default:
		fmt.Fprintf(os.Stderr, "Unknown system command: %s\n", subcmd)
		fs.Usage()
		os.Exit(1)
	}
}

func handleVaultCmd(ctx context.Context, client MemoryClient, args []string) {
	fs := flag.NewFlagSet("vault", flag.ExitOnError)
	userIDStr := fs.String("user", "", "User UUID")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: memory-cli vault list [options]\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
	}

	if len(args) < 1 {
		fs.Usage()
		os.Exit(1)
	}

	subcmd := args[0]
	fs.Parse(args[1:])

	if *userIDStr == "" {
		fmt.Fprintf(os.Stderr, "Error: --user flag is required\n")
		fs.Usage()
		os.Exit(1)
	}

	userID, err := uuid.Parse(*userIDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid user UUID: %v\n", err)
		os.Exit(1)
	}

	switch subcmd {
	case "list":
		secrets, err := client.ListVaultSecrets(ctx, userID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting vault secrets: %v\n", err)
			os.Exit(1)
		}

		outputJSON(secrets)

	default:
		fmt.Fprintf(os.Stderr, "Unknown vault command: %s\n", subcmd)
		fs.Usage()
		os.Exit(1)
	}
}

func outputJSON(v interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
		os.Exit(1)
	}
}
