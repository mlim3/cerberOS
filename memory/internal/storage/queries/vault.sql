-- name: SaveSecret :exec
INSERT INTO vault_schema.secrets (
    id, user_id, key_name, encrypted_value, nonce, created_at
) VALUES (
    $1, $2, $3, $4, $5, NOW()
) ON CONFLICT (user_id, key_name) DO UPDATE
SET encrypted_value = EXCLUDED.encrypted_value,
    nonce = EXCLUDED.nonce;

-- name: GetSecretByKey :one
SELECT id, user_id, key_name, encrypted_value, nonce, created_at
FROM vault_schema.secrets
WHERE user_id = $1 AND key_name = $2;

-- name: DeleteSecret :exec
DELETE FROM vault_schema.secrets
WHERE user_id = $1 AND key_name = $2;
