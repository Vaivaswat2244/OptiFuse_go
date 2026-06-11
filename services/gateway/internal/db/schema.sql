-- OptiFuse Gateway Schema
-- Mirrors the Django models exactly:
-- core/models.py: User, Profile
-- rest_framework.authtoken: Token

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE IF NOT EXISTS users (
    id         BIGSERIAL PRIMARY KEY,
    username   VARCHAR(150) NOT NULL UNIQUE,
    email      VARCHAR(254),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS profiles (
    id                   BIGSERIAL PRIMARY KEY,
    user_id              BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    github_access_token  VARCHAR(255) NOT NULL DEFAULT '',
    aws_role_arn         VARCHAR(255),
    aws_external_id      UUID NOT NULL DEFAULT uuid_generate_v4(),
    subscription         VARCHAR(10) NOT NULL DEFAULT 'FREE',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- DRF-style auth tokens: one token per user, random 40-char hex string
CREATE TABLE IF NOT EXISTS tokens (
    key        CHAR(40) PRIMARY KEY,
    user_id    BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tokens_user_id ON tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_profiles_user_id ON profiles(user_id);