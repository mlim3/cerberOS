-- name: UpsertSkill :one
INSERT INTO agents_schema.skill_cache (
    id, domain, name, origin, description, payload, embedding, seed_hash
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
ON CONFLICT (domain, name) DO UPDATE
SET
    origin      = EXCLUDED.origin,
    description = EXCLUDED.description,
    payload     = EXCLUDED.payload,
    embedding   = EXCLUDED.embedding,
    seed_hash   = EXCLUDED.seed_hash,
    updated_at  = NOW()
RETURNING *;

-- name: SearchSkills :many
SELECT id, domain, name, origin, description, payload, seed_hash, created_at, updated_at
FROM agents_schema.skill_cache
ORDER BY embedding <=> $1
LIMIT $2;

-- name: SearchSkillsByDomain :many
SELECT id, domain, name, origin, description, payload, seed_hash, created_at, updated_at
FROM agents_schema.skill_cache
WHERE domain = $2
ORDER BY embedding <=> $1
LIMIT $3;

-- name: GetSkillBySeedHash :many
SELECT id, domain, name, origin, description, payload, seed_hash, created_at, updated_at
FROM agents_schema.skill_cache
WHERE seed_hash = $1;

-- name: GetSkillsByDomain :many
SELECT id, domain, name, origin, description, payload, seed_hash, created_at, updated_at
FROM agents_schema.skill_cache
WHERE domain = $1
ORDER BY name;
