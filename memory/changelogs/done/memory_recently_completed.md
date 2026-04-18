# Memory Recently Completed

These items are already implemented enough that they should not be treated as open gaps:

- delete fact route exists and is wired
- agent singular route exists alongside the legacy route
- agent endpoints use the standard success/error envelope
- agent GET supports `limit`
- user existence validation exists in chat, personal info, and vault paths
- chat idempotency conflict handling exists
- chat idempotency uniqueness is already scoped per session in the schema
- personal info query now uses vector distance and a deterministic tie-break
- Swagger wiring now uses `swaggo` instead of manually serving a static file
- CLI `facts save` and `facts all` behavior was tightened so the fact flow is internally consistent
