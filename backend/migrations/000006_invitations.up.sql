-- Staff accounts are created by invitation: an admin invites an email address as ORGANISER or
-- ADMIN, and the invitee sets a password through a one-time link.

CREATE TABLE invitations (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Stored lower-cased so "one live invitation per email" cannot be dodged with capitals.
    email        TEXT NOT NULL CHECK (email = lower(email) AND email <> ''),
    role         TEXT NOT NULL CHECK (role IN ('ORGANISER', 'ADMIN')),
    -- The link carries the token; only its hash is kept, so a leaked table invites nobody.
    token_hash   BYTEA NOT NULL UNIQUE,
    invited_by   BIGINT NOT NULL REFERENCES users (id),
    expires_at   TIMESTAMPTZ NOT NULL,
    accepted_at  TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (accepted_at IS NULL OR revoked_at IS NULL)
);
-- At most one invitation per address that can still be accepted. Expiry is not part of the
-- rule (an index cannot depend on the clock): an expired invitation is revoked before a new
-- one is sent.
CREATE UNIQUE INDEX invitations_live_email_key ON invitations (email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX invitations_created_idx ON invitations (created_at DESC, id DESC);
CREATE INDEX invitations_invited_by_idx ON invitations (invited_by);
