# cerberOS Compose Conventions

## When to use
When adding a new service to the root docker-compose.yml or modifying existing service definitions.

## Build contexts

All build contexts are relative to the repo root since docker-compose.yml lives there:

```yaml
services:
  orchestrator:
    build:
      context: ./orchestrator     # subdirectory containing Dockerfile
  vault:
    build:
      context: ./vault/engine     # nested subdirectory
  io:
    build:
      context: ./io
      args:
        ENABLE_VOICE: "false"     # build args go under build:
```

The Dockerfile must be in the build context directory. If it's elsewhere, use `dockerfile:` to override.

## Naming conventions

- **Service names**: lowercase, hyphenated (e.g., `memory-api`, `memory-db`)
- **Volume names**: lowercase, hyphenated (e.g., `nats-data`, `postgres-data`)
- **Network**: single network named `cerberos` for all default-profile services
- **Profiles**: lowercase, no hyphens (e.g., `agents`, `databus`)

## Network rules

1. All services join the `cerberos` network (not external — defined in root compose)
2. Do NOT use `external: true` — the root compose owns the network
3. Use network aliases when a service needs to be reachable by a legacy hostname:
   ```yaml
   networks:
     cerberos:
       aliases:
         - db    # legacy hostname from subdirectory compose
   ```

## Environment variables

- Use Compose interpolation with defaults: `${VAR:-default}`
- Use required vars with error messages: `${VAR:?Error message}`
- All secrets come from `.env` (never hardcoded in compose)
- Document new vars in `.env.example`

## Adding a new service — checklist

1. Add the service definition to `docker-compose.yml`
2. Assign a non-conflicting host port (check `skills/cerberos-service-ports.md`)
3. Add to the `cerberos` network
4. Set `depends_on` with `condition: service_healthy` where healthchecks exist
5. Use `restart: unless-stopped` for long-running services
6. Update `.env.example` if new env vars are needed
7. Update `skills/cerberos-service-ports.md` with the new port mapping

## Profiles

Services behind a profile are only started when explicitly requested:

```yaml
services:
  aegis-agents:
    profiles:
      - agents    # only starts with: docker compose --profile agents up
```

Use profiles for:
- Optional or experimental services
- Services that require additional credentials (e.g., ANTHROPIC_API_KEY)
- Heavy services not needed for core development

## Volume mounts

- Config files: mount `:ro` (read-only)
- Data directories: use named volumes
- Init scripts: mount to `/docker-entrypoint-initdb.d/` with numeric prefix for ordering

## Using `include:` (future)

If the compose file grows too large, split into `compose.d/*.yml` fragments and use:

```yaml
include:
  - compose.d/memory.yml
  - compose.d/vault.yml
```

Avoid this unless the file exceeds ~300 lines. One file is easier to reason about.
