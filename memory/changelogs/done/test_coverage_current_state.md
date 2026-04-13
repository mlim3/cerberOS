# Test Coverage Current State

## Purpose

Track the areas that currently have meaningful or partial coverage.

## Covered Areas

### Vault contract consistency

Status:

- partial coverage

Covered by:

- `memory/tests/vault_contract_test.go`

What is covered:

- missing API key behavior
- invalid API key behavior
- malformed user id behavior
- unknown user behavior
- required field validation
- create/get/update/delete flow shape

What the tests reveal:

- current vault handlers still do not consistently return the standard JSON envelope

### CLI contract

Status:

- partial to moderate coverage

Covered by:

- `memory/tests/cli_facts_db_mode_test.go`
- `memory/tests/cli_chat_db_mode_test.go`
- `memory/tests/cli_agent_db_mode_test.go`
- `memory/tests/cli_system_db_mode_test.go`
- `memory/tests/cli_vault_db_mode_test.go`
- `memory/tests/cli_contract_test.go`

What is covered:

- `facts query`
- `facts all`
- `facts save`
- chat history output shape
- agent history output shape
- system events output shape
- empty list commands should emit JSON arrays

### Chat ownership and security

Status:

- moderate coverage

Covered by:

- `memory/tests/chat_integration_test.go`
- `memory/tests/chat_contract_test.go`

What is covered:

- chat write/read basic flow
- idempotency behavior
- owner can write to a new session
- different user cannot write to an owned session

### Agent endpoint contract

Status:

- strong coverage

Covered by:

- `memory/tests/agent_integration_test.go`
- `memory/tests/agent_contract_test.go`

What is covered:

- execution creation
- execution listing
- `limit` handling
- basic response shape expectations
- singular route contract
- legacy plural route contract
- shared execution history across both routes

### Personal info basic flow

Status:

- strong coverage

Covered by:

- `memory/tests/personal_info_integration_test.go`
- `memory/tests/personal_info_contract_test.go`

What is covered:

- save/query flow
- optimistic concurrency update flow
- delete fact flow
- malformed user handling on save/get-all
- unknown user handling on save/get-all
- malformed and unknown user handling on query/update/delete
- end-to-end success envelope coverage for save/get-all/query/update/delete
- stale update returns `409 conflict`

### Retrieval correctness for ranking

Status:

- partial coverage

Covered by:

- `memory/tests/personal_info_integration_test.go`

What is covered:

- more relevant chunk ranks ahead of a less relevant chunk
- deterministic tie-break behavior prefers more recent chunks for equal-distance results
