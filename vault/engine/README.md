# vault

Vault is a credential broker for cerberOS agents. It accepts shell scripts with `{{PLACEHOLDER}}` markers, resolves secrets, injects them into the script, and returns the completed script for the agent to execute in its own environment.

Authorization is atomic — if any requested secret is missing or denied, the entire request fails with no partial injection.

## How it works

```
POST /inject
      │
      ▼
 Preprocessor          Extracts {{PLACEHOLDER}} tokens, resolves secrets
      │                via SecretManager (all-or-nothing)
      ▼
 Inject Response       Returns completed script with secrets substituted
```

### Preprocessor (`preprocessor/`)

Scans the script for `{{KEY_NAME}}` placeholders and replaces them with values fetched from a `SecretManager`. If any key is not found, the entire request fails — no partial injection occurs.

The `SecretManager` interface is pluggable — the current implementation is a mock. Drop in HashiCorp Vault, AWS Secrets Manager, etc. without changing anything else.

### Secret Manager (`secretmanager/`)

Defines the `SecretManager` interface for pluggable secret backends:

```go
type SecretManager interface {
    Resolve(keys []string) (map[string]string, error)
}
```

The mock implementation provides dev secrets. Real implementations (HashiCorp Vault, AWS KMS, etc.) can be swapped in without changing any other package.

### Audit Logger (`audit/`)

Structured audit logging for agent interactions. Every `/inject` call produces an audit event recording the agent identity and requested secret names (never values).

Events are written as newline-delimited JSON to stdout by default:

```json
{"time":"2026-03-08T12:00:00Z","kind":"injection","agent":"my-agent","keys":["API_KEY","DB_PASS"],"message":"agent requested secret injection"}
{"time":"2026-03-08T12:00:00Z","kind":"secret_access","agent":"my-agent","keys":["API_KEY","DB_PASS"],"message":"agent requested secrets"}
```

The logger is pluggable — ship events anywhere by implementing `audit.Exporter`:

```go
type Exporter interface {
    Export(e Event) error
}
```

### HTTP Server (`main.go`, `handlers/`)

Listens on `:8000`. `handlers/main.go` wires `handlers/inject` and `handlers/secrets`; shared JSON error type lives in `handlers/common`. Endpoints:

| Endpoint         | Method | Description                                  |
| ---------------- | ------ | -------------------------------------------- |
| `/inject`        | POST   | Inject secrets into a script and return it   |
| `/secrets/get`   | POST   | Read secrets by key list                     |
| `/secrets/put`   | POST   | Write or update a secret                     |
| `/secrets/delete`| POST   | Delete a secret                              |

`/inject` request body:

```json
{
  "agent": "my-agent",
  "script": "#!/bin/sh\necho {{API_KEY}}"
}
```

Response:

```json
{
  "agent": "my-agent",
  "script": "#!/bin/sh\necho actual-api-key-value"
}
```

## CLI (`vault`)

The `vault` CLI lives in `cmd/vault/` and talks to a running vault service over HTTP.

### Build

```sh
# from engine/
go build -o vault ./cmd/vault/

# install to $GOPATH/bin
go install ./cmd/vault/
```

### Usage

```
vault inject [flags]

Flags:
  -f, --file <path>       read script from file
  -s, --script <text>     inline script text
  -o, --output <path>     write injected script to file (default: stdout)
  --host <url>            vault service URL (default: http://localhost:8000)
```

If neither `-f` nor `-s` is given, the script is read from stdin.

### Examples

```sh
# inline script
vault inject -s 'echo {{API_KEY}}'

# script file
vault inject -f deploy.sh

# write output to file
vault inject -f deploy.sh -o deploy_ready.sh

# pipe from stdin
echo '#!/bin/sh\ncurl -H "Authorization: {{TOKEN}}" https://api.example.com' | vault inject

# point at a remote vault
vault inject --host http://10.0.0.5:8000 -s 'echo {{API_KEY}}'
```

The CLI prints the completed script to stdout (or writes to a file with `-o`). Exit code 0 on success, non-zero on failure.

---

## Building

The `Dockerfile` is a two-stage build:

1. **Build** — compiles the Go binary (static, no CGO)
2. **Production** — runs as non-root `altuser` (UID 1001)

```sh
docker build -t vault .
docker run -p 8000:8000 vault
```

## Dependencies

None. Standard library Go only (`go 1.24`).
