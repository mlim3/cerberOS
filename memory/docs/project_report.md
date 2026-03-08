# cerberOS Distributed System Design Report

This document outlines the core distributed systems principles and architectural choices made during the development of the cerberOS memory service.

## 1. UUIDv7 for Time-Ordered Clustering
We chose UUIDv7 for primary keys across all major tables (`chat_messages`, `personal_info_facts`, `vault_secrets`). 
Unlike UUIDv4, which is completely random and leads to B-tree index fragmentation and page splits in PostgreSQL, UUIDv7 is time-ordered. 
This ensures that new records are appended sequentially, optimizing index locality, reducing disk I/O, and enabling highly efficient time-based range queries (e.g., retrieving recent chat history).

## 2. Logical Sharding Keys for Future Scaling
To future-proof the database for horizontal scalability, we designed the schema with logical sharding in mind. 
Tables such as `chat_messages`, `personal_info_facts`, and `vault_secrets` prominently include `user_id` or `session_id` as logical sharding keys. 
This data modeling choice ensures that if the service outgrows a single PostgreSQL instance, we can seamlessly partition the data across multiple database nodes based on these keys, maintaining data locality for specific users or sessions.

## 3. Optimistic Concurrency Control (OCC)
For the `personal_info_facts` table, concurrent updates from multiple agents are a significant risk. We implemented Optimistic Concurrency Control (OCC) using a `version` field.
Whenever an agent updates a fact, the `version` is incremented. The update operation explicitly checks that the `version` matches the one initially read. If another agent has modified the fact in the interim, the update fails with a `Conflict` (HTTP 409), preventing lost updates without the heavy performance penalty of pessimistic row locking.

## 4. Application-Level AES-256-GCM Encryption
To protect highly sensitive data (the Vault), we implemented application-level AES-256-GCM encryption. 
Data is encrypted *before* it reaches the database. The database only stores the ciphertext and the nonce. The `VAULT_MASTER_KEY` is injected via environment variables and is never persisted in the database. 
This separation of concerns ensures that even in the event of a full database compromise, the secrets remain completely inaccessible without the application's runtime configuration.
