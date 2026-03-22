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

const usage = `vault — execute scripts in the cerberOS vault engine

Usage:
  vault execute [flags]

Flags:
  -f, --file <path>       read script from file
  -s, --script <text>     inline script text
  -e, --env KEY=VAL       set an environment variable (repeatable)
  --host <url>            engine base URL (default: http://localhost:8000)

If neither -f nor -s is given, the script is read from stdin.

Examples:
  vault execute -f deploy.sh
  vault execute -s 'echo hello'
  vault execute -f deploy.sh -e API_KEY=abc -e REGION=us-east-1
  echo 'ls /tmp' | vault execute
`

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ", ") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

type executeRequest struct {
	Script string            `json:"script"`
	Env    map[string]string `json:"env"`
}

type executeResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

type executeResponse struct {
	Response executeResult `json:"response"`
}

// run is the testable entry point. stdin may be nil when no pipe is attached.
// Returns the process exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if args[0] != "execute" {
		fmt.Fprintf(stderr, "error: unknown command %q\n\n%s", args[0], usage)
		return 1
	}

	fs := flag.NewFlagSet("execute", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		file   string
		script string
		host   string
		envs   multiFlag
	)
	fs.StringVar(&file, "f", "", "")
	fs.StringVar(&file, "file", "", "")
	fs.StringVar(&script, "s", "", "")
	fs.StringVar(&script, "script", "", "")
	fs.StringVar(&host, "host", "http://localhost:8000", "")
	fs.Var(&envs, "e", "")
	fs.Var(&envs, "env", "")

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

	// Parse -e KEY=VAL pairs
	env := make(map[string]string, len(envs))
	for _, kv := range envs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fmt.Fprintf(stderr, "error: invalid env flag %q: expected KEY=VAL\n", kv)
			return 1
		}
		env[k] = v
	}

	req := executeRequest{Script: string(scriptBytes), Env: env}
	body, _ := json.Marshal(req)

	resp, err := http.Post(host+"/execute", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "error: connecting to engine at %s: %v\n", host, err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(stderr, "error: engine returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(msg)))
		return 1
	}

	var result executeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Fprintf(stderr, "error: decoding response: %v\n", err)
		return 1
	}

	fmt.Fprint(stdout, result.Response.Output)
	return result.Response.ExitCode
}

func main() {
	// Only attach stdin if it is a pipe/file, not an interactive terminal.
	var stdinReader io.Reader
	if stat, err := os.Stdin.Stat(); err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		stdinReader = os.Stdin
	}
	os.Exit(run(os.Args[1:], stdinReader, os.Stdout, os.Stderr))
}
