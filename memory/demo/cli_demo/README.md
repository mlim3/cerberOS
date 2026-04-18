# Memory Service CLI Demo & Documentation

This directory contains a demonstration and detailed documentation of the new `memory-cli` tool.

The CLI allows for direct interactions with the Memory Service in two distinct ways:
1. **HTTP Mode (Default):** The CLI queries the running Memory Service REST API.
2. **Direct DB Mode:** The CLI establishes a direct database connection to PostgreSQL, avoiding the HTTP overhead and making it extremely fast for agents running on a trusted network.

## Prerequisites

1. The `memory-cli` binary must be compiled.
2. For testing, the database and/or the memory service needs to be running.
   
```bash
# 1. Start the memory service and DB locally
cd /Users/aniketthakker/Downloads/cerberOS
./scripts/mem-up.sh

# 2. Build the CLI binary
cd /Users/aniketthakker/Downloads/cerberOS/memory
go build -o memory-cli ./cmd/cli
```

---

## 1. Chat History Subcommand

The `chat` command allows agents or administrators to quickly retrieve the chat history for a specific session.

### Direct Database Connection

If we want the CLI to bypass the HTTP API entirely, we pass the `-db="env"` flag. This instructs the CLI to read the standard `DB_HOST`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` environment variables and execute the queries directly against PostgreSQL.

**Command:**
```bash
export DB_USER=user
export DB_PASSWORD=password
export DB_NAME=memory_db

./memory-cli -db "env" chat history --session aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa
```

**Expected Output:**
```json
[
  {
    "id": "019cd44f-6c84-7c40-aa14-27b98b1a3d3a",
    "role": "user",
    "content": "What did I say about Postgres last week?",
    "created_at": "2026-03-09 13:35:00.868803 -0700 PDT"
  },
  {
    "id": "019cd432-e7c7-7a29-a30b-7abd963266f4",
    "role": "user",
    "content": "What did I say about Postgres last week?",
    "created_at": "2026-03-09 13:03:51.879666 -0700 PDT"
  },
  {
    "id": "3e05ecdd-cbc4-41bc-aa59-2f5c2c0fdf82",
    "role": "user",
    "content": "I am working with some CSV files containing sales data. I prefer using pandas.",
    "created_at": "2026-03-08 00:19:33.307811 -0800 PST"
  },
  {
    "id": "17d41752-8340-49d6-a6ee-ffedce412675",
    "role": "assistant",
    "content": "Of course! I can help with that. What kind of data are you analyzing?",
    "created_at": "2026-03-08 00:18:33.307811 -0800 PST"
  },
  {
    "id": "f60a1799-8c55-4738-9521-8578bce18f5d",
    "role": "user",
    "content": "Hi, can you help me write a Python script for data analysis?",
    "created_at": "2026-03-08 00:17:33.307811 -0800 PST"
  }
]
```
*(Notice that the results are displayed chronologically, making it easy for an LLM to feed directly into its context window).*

---

## 2. Facts Query Subcommand

The `facts` command manages and runs semantic search across the user's saved personal facts. 

### Direct Database Connection

**Command (Query):**
```bash
export DB_USER=user
export DB_PASSWORD=password
export DB_NAME=memory_db

./memory-cli -db "env" facts query --user 11111111-1111-1111-1111-111111111111 "what programming language do I prefer?"
```

**Expected Output:**
```json
[
  {
    "id": "019cd450-6611-700f-a794-da314da8e67b",
    "content": "Colby prefers PostgreSQL with pgvector for memory service work."
  }
]
```

**Command (Save):**
```bash
./memory-cli -db "env" facts save --user 11111111-1111-1111-1111-111111111111 "I love writing Go code."
```

**Expected Output:**
```
Fact saved successfully.
```

**Command (All):**
```bash
./memory-cli -db "env" facts all --user 11111111-1111-1111-1111-111111111111
```

---

## 3. Agent Task Executions Subcommand

The `agent` command allows tracking execution history for specific Agent Tasks.

**Command:**
```bash
./memory-cli -db "env" agent history --task 11111111-1111-1111-1111-111111111111 --limit 5
```

**Expected Output:**
```json
[
  {
    "id": "22222222-2222-2222-2222-222222222222",
    "task_id": "11111111-1111-1111-1111-111111111111",
    "status": "completed",
    "created_at": "2026-04-06 13:00:00 -0700 PDT"
  }
]
```

---

## 4. System Events Subcommand

The `system` command lets an administrator review system-level logging and events securely.

**Command:**
```bash
./memory-cli -db "env" system events --limit 10
```

**Expected Output:**
```json
[
  {
    "id": "33333333-3333-3333-3333-333333333333",
    "event_type": "info",
    "message": "{\"userId\":\"11111111-1111-1111-1111-111111111111\",\"path\":\"/api/v1/chat/history\",\"status\":\"granted\"}",
    "created_at": "2026-04-06 13:05:00 -0700 PDT"
  }
]
```

---

## 5. Vault Management (HTTP API Only)

Because Vault keys are heavily encrypted and rely on a master runtime encryption key, reading and managing Vault secrets via the CLI is currently restricted to HTTP mode (where the Memory Service handles decryption safely).

**Command (HTTP API):**
```bash
./memory-cli -api "http://localhost:8080" vault list --user 11111111-1111-1111-1111-111111111111
```

---

## Testing

Automated testing for the CLI is available in the `tests/cli_test.go` file. The integration tests invoke the `exec.CommandContext` package to spawn the compiled binary and assert against the JSON output.

**Running the tests:**
```bash
cd /Users/aniketthakker/Downloads/cerberOS/memory/tests
go test -v cli_test.go
```

**Test Output:**
```
=== RUN   TestCLIFactsQuery
--- PASS: TestCLIFactsQuery (0.08s)
=== RUN   TestCLIChatHistory
--- PASS: TestCLIChatHistory (0.02s)
PASS
ok      command-line-arguments  0.414s
```
