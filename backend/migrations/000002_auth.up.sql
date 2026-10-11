-- Auth: organisers and admins sign in with email + password, plus a TOTP code if they have
-- turned it on; fans and gate staff with a phone OTP (docs/ARCHITECTURE.md §11). 000001 assumed every user has a phone.

ALTER TABLE users
    ALTER COLUMN phone_e164 DROP NOT NULL,
    ADD COLUMN password_hash TEXT,          -- argon2id PHC string, never the password
    ADD COLUMN totp_secret   TEXT,          -- base32 shared secret
    ADD COLUMN totp_enabled  BOOLEAN NOT NULL DEFAULT FALSE,
    -- An account nobody can sign in to is a bug, not a state.
    ADD CONSTRAINT users_contact_check CHECK (phone_e164 IS NOT NULL OR email IS NOT NULL),
    -- The phone is the M-Pesa identity; a malformed one cannot receive an OTP or an STK push.
    ADD CONSTRAINT users_phone_e164_check CHECK (phone_e164 ~ '^\+[1-9][0-9]{7,14}$'),
    ADD CONSTRAINT users_password_needs_email_check CHECK (password_hash IS NULL OR email IS NOT NULL),
    ADD CONSTRAINT users_totp_check CHECK (NOT totp_enabled OR totp_secret IS NOT NULL);

-- "Ann@x.co" and "ann@x.co" are the same login.
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

CREATE TABLE otp_codes (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    phone_e164  TEXT NOT NULL CHECK (phone_e164 ~ '^\+[1-9][0-9]{7,14}$'),
    -- Six digits are trivially brute-forced from a leaked table, so only a keyed hash is stored.
    code_hash   BYTEA NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    consumed_at TIMESTAMPTZ,
    request_ip  INET,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Latest code for a phone, and "how many codes has this phone asked for lately".
CREATE INDEX otp_codes_phone_created_idx ON otp_codes (phone_e164, created_at DESC);
-- SMS costs money: the per-IP rate limit counts recent requests from one address.
CREATE INDEX otp_codes_ip_created_idx ON otp_codes (request_ip, created_at);

CREATE TABLE refresh_tokens (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Every token descended from one login shares a family. Presenting an already-used token
    -- means it was stolen, and the whole family is revoked.
    family_id   UUID NOT NULL,
    token_hash  BYTEA NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_expires_idx ON refresh_tokens (expires_at);
