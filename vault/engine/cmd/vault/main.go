package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const usage = `vault — credential broker for cerberOS agents

Usage:
  vault inject [flags]

Flags:
  -f, --file <path>       read script from file
  -s, --script <text>     inline script text
  -o, --output <path>     write injected script to file (default: stdout)
  --host <url>            vault service URL (default: http://localhost:8000)

If neither -f nor -s is given, the script is read from stdin.

The script may contain {{PLACEHOLDER}} markers that will be replaced with
the corresponding secret values. All referenced secrets must be accessible
to the agent — if any secret is denied, the entire request fails with no
partial injection.

Examples:
  vault inject -s 'echo {{API_KEY}}'
  vault inject -f deploy.sh
  vault inject -f deploy.sh -o deploy_ready.sh
  echo '#!/bin/sh\ncurl -H "Authorization: {{TOKEN}}" https://api.example.com' | vault inject
`

type injectRequest struct {
	Agent  string `json:"agent"`
	Script string `json:"script"`
}

type injectResponse struct {
	Agent  string `json:"agent"`
	Script string `json:"script"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// run is the testable entry point. stdin may be nil when no pipe is attached.
// Returns the process exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if args[0] != "inject" {
		fmt.Fprintf(stderr, "error: unknown command %q\n\n%s", args[0], usage)
		return 1
	}

	fs := flag.NewFlagSet("inject", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		file   string
		script string
		host   string
		output string
	)
	fs.StringVar(&file, "f", "", "")
	fs.StringVar(&file, "file", "", "")
	fs.StringVar(&script, "s", "", "")
	fs.StringVar(&script, "script", "", "")
	fs.StringVar(&host, "host", "http://localhost:8000", "")
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&output, "output", "", "")

	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintf(stderr, "error: %v\n\n%s", err, usage)
		return 1
	}

	// Resolve script source: -f > -s > stdin
	var scriptBytes []byte
	switch {
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(stderr, "error: reading file: %v\n", err)
			return 1
		}
		scriptBytes = b
	case script != "":
		scriptBytes = []byte(script)
	default:
		if stdin == nil {
			fmt.Fprintf(stderr, "error: no script provided — use -f <file>, -s <text>, or pipe via stdin\n")
			return 1
		}
		b, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "error: reading stdin: %v\n", err)
			return 1
		}
		scriptBytes = b
	}

	req := injectRequest{Script: string(scriptBytes)}
	body, _ := json.Marshal(req)

	resp, err := http.Post(host+"/inject", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "error: connecting to vault at %s: %v\n", host, err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var errResp errorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			fmt.Fprintf(stderr, "error: %s\n", errResp.Error)
		} else {
			fmt.Fprintf(stderr, "error: vault returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return 1
	}

	var result injectResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Fprintf(stderr, "error: decoding response: %v\n", err)
		return 1
	}

	// Write injected script to file or stdout
	if output != "" {
		if err := os.WriteFile(output, []byte(result.Script), 0755); err != nil {
			fmt.Fprintf(stderr, "error: writing output file: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprint(stdout, result.Script)
	}
	return 0
}

func main() {
	var stdinReader io.Reader
	if stat, err := os.Stdin.Stat(); err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		stdinReader = os.Stdin
	}
	os.Exit(run(os.Args[1:], stdinReader, os.Stdout, os.Stderr))
}
