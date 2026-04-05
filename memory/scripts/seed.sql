-- ==========================================
-- SEED DATA FOR MEMORY SERVICE
-- ==========================================

-- 1. identity_schema.users
INSERT INTO identity_schema.users (id, email, created_at)
VALUES 
    ('11111111-1111-1111-1111-111111111111', 'alice@example.com', NOW()),
    ('22222222-2222-2222-2222-222222222222', 'bob@example.com', NOW())
ON CONFLICT (id) DO NOTHING;

-- 2. chat_schema.messages (Session_A for user 1)
INSERT INTO chat_schema.messages (id, session_id, user_id, role, content, token_count, idempotency_key, created_at)
VALUES 
    (gen_random_uuid(), 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', '11111111-1111-1111-1111-111111111111', 'user', 'Hi, can you help me write a Python script for data analysis?', 12, gen_random_uuid(), NOW() - INTERVAL '10 minutes'),
    (gen_random_uuid(), 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', '11111111-1111-1111-1111-111111111111', 'assistant', 'Of course! I can help with that. What kind of data are you analyzing?', 15, gen_random_uuid(), NOW() - INTERVAL '9 minutes'),
    (gen_random_uuid(), 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', '11111111-1111-1111-1111-111111111111', 'user', 'I am working with some CSV files containing sales data. I prefer using pandas.', 16, gen_random_uuid(), NOW() - INTERVAL '8 minutes')
ON CONFLICT (id) DO NOTHING;

-- 3. personal_info_schema.user_facts (for user 1 and 2)
INSERT INTO personal_info_schema.user_facts (id, user_id, category, fact_key, fact_value, confidence, version, updated_at)
VALUES 
    (gen_random_uuid(), '11111111-1111-1111-1111-111111111111', 'preferences', 'programming_language', '"Python"', 0.95, 1, NOW()),
    (gen_random_uuid(), '11111111-1111-1111-1111-111111111111', 'preferences', 'library', '"pandas"', 0.9, 1, NOW()),
    (gen_random_uuid(), '22222222-2222-2222-2222-222222222222', 'location', 'timezone', '"America/New_York"', 0.99, 1, NOW())
ON CONFLICT (id) DO NOTHING;

-- 4. vault_schema.secrets (for user 1)
INSERT INTO vault_schema.secrets (id, user_id, key_name, encrypted_value, nonce, created_at)
VALUES 
    (gen_random_uuid(), '11111111-1111-1111-1111-111111111111', 'OPENAI_API_KEY', '\xdeadbeef'::bytea, '\x000000000000000000000000'::bytea, NOW()),
    (gen_random_uuid(), '11111111-1111-1111-1111-111111111111', 'GITHUB_TOKEN', '\xcafebabe'::bytea, '\x111111111111111111111111'::bytea, NOW())
ON CONFLICT (id) DO NOTHING;
