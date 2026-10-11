DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS otp_codes;

DROP INDEX IF EXISTS users_email_lower_key;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_totp_check,
    DROP CONSTRAINT IF EXISTS users_password_needs_email_check,
    DROP CONSTRAINT IF EXISTS users_phone_e164_check,
    DROP CONSTRAINT IF EXISTS users_contact_check,
    DROP COLUMN IF EXISTS totp_enabled,
    DROP COLUMN IF EXISTS totp_secret,
    DROP COLUMN IF EXISTS password_hash;

-- 000001 requires a phone. Email-only staff get a placeholder that is not a dialable number
-- rather than being deleted, because bookings and scan logs may point at them.
UPDATE users SET phone_e164 = 'nophone:' || id WHERE phone_e164 IS NULL;
ALTER TABLE users ALTER COLUMN phone_e164 SET NOT NULL;
