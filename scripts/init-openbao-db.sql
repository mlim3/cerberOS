-- Create the openbao database for OpenBao storage backend.
-- Runs as part of docker-entrypoint-initdb.d (Postgres container startup).
-- The POSTGRES_USER from docker-compose automatically owns this database.

SELECT 'CREATE DATABASE openbao'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'openbao')\gexec
