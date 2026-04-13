# Swagger Swaggo Migration

Converted Swagger from a static file-serving setup into a `swaggo`-driven setup in `memory/cmd/server/main.go`.

Completed:

- added `go:generate` for `swag`
- added top-level Swagger annotations (`@title`, `@version`, security definition, etc.)
- imported the generated docs package
- populated `docs.SwaggerInfo` at startup
- removed the manual static `swagger.json` serving path
- added a documented `healthz` handler so health is part of the API spec

Handler annotation alignment completed:

- added Swagger annotations for the singular agent routes in `memory/internal/api/agent_handler.go`
- added Swagger annotations for `DELETE /api/v1/personal_info/{userId}/facts/{factId}` in `memory/internal/api/personal_info_handler.go`

Generated docs updated:

- `memory/docs/docs.go`
- `memory/docs/swagger.json`
- `memory/docs/swagger.yaml`

The generated docs now include:

- `GET /api/v1/healthz`
- `GET /POST /api/v1/agent/{taskId}/executions`
- `DELETE /api/v1/personal_info/{userId}/facts/{factId}`

Tooling/docs support added:

- created `memory/tools.go` to track the `swag` generator
- added a Swagger generation section to `memory/README.md` explaining `go generate ./cmd/server`
