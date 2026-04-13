# Test Coverage Gaps

## Purpose

Track the remaining test gaps and red future-contract areas.

## Remaining Gaps

### Vault contract consistency

Remaining gap:

- implementation is not yet aligned with the intended contract

### CLI contract

Remaining gap:

- CLI semantics around structured facts vs source-material operations still need product-level clarification

### Chat ownership and security

Missing:

- read-side cross-user access checks are still not covered because the current API does not take a user identifier on chat history reads
- tests for a future explicit session ownership resource

### Agent endpoint contract

Missing:

- no new remaining-work-specific tests beyond the existing contract

### Personal info basic flow

Missing:

- archive visibility behavior
- contradiction/supersession lifecycle behavior
- decay behavior
- stronger ranking assertions

## Future-Contract Areas

### Scheduled jobs

Status:

- black-box future contract coverage exists and is currently red

Covered by:

- `memory/tests/scheduling_contract_test.go`

What the tests reveal:

- no scheduled-jobs HTTP surface exists yet
- the invented contract routes currently return `404 page not found`

Remaining gap:

- implement and document the scheduled-jobs API surface

### External dispatch / memory -> orchestrator BUS

Status:

- black-box future contract coverage exists and is currently red

Covered by:

- `memory/tests/scheduling_contract_test.go`

What the tests reveal:

- no external-dispatch scheduler contract exists yet because the scheduling surface itself is missing

Remaining gap:

- implement the scheduling surface and dispatch result contract

### Memory decay

Status:

- indirectly covered by future archive contract and currently red

Covered by:

- `memory/tests/fact_archive_contract_test.go`

What the tests reveal:

- no lifecycle/archive surface exists yet

Remaining gap:

- implement lifecycle/archive endpoints and scheduled decay execution

### Fact archival

Status:

- black-box future contract coverage exists and is currently red

Covered by:

- `memory/tests/fact_archive_contract_test.go`

What the tests reveal:

- archive route does not exist yet
- archive-aware retrieval contract does not exist yet

Remaining gap:

- implement archive lifecycle surface and archive-aware reads

### Contradiction / supersession

Status:

- black-box future contract coverage exists and is currently red

Covered by:

- `memory/tests/fact_supersession_contract_test.go`

What the tests reveal:

- supersede route does not exist yet

Remaining gap:

- implement contradiction/supersession surface and archive relationship visibility

### Explicit session ownership model

No tests currently cover:

- session ownership resource creation
- ownership lookup
- cross-user denial against an explicit ownership table

### Swagger regeneration workflow

No tests or checks currently cover:

- `go generate ./cmd/server`
- Swagger drift detection
- generated artifact verification

## Priority Order

1. vault contract tests should remain in place and be used to drive the vault cleanup to green
2. add explicit ownership-boundary tests for chat/session behavior
3. add scheduled job tests
4. add decay/archive lifecycle tests
5. add contradiction/supersession tests
6. add retrieval ordering tests
7. add external dispatch/BUS tests
