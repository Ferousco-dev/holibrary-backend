-- 0008_audit_fixes.sql
-- Corrections from an adversarial security audit. See .ilana/defects.md
-- DEF-014..DEF-019.

-- DEF-015: sessions survived a password change under a race.
--
-- Revoking every refresh token is not atomic with the password update, so a
-- concurrent /auth/refresh could consume an old token and mint a new one in the
-- gap -- leaving the attacker a live session after the victim had changed their
-- password precisely to stop that.
--
-- A version stamp closes the window. It is written in the same transaction as
-- the new password hash, and every token issued before it is invalid by
-- comparison rather than by having been individually revoked. There is no gap
-- to race, because nothing has to be revoked.
ALTER TABLE users
    ADD COLUMN tokens_invalid_before timestamptz NOT NULL DEFAULT now();

-- DEF-016: a pending reservation aged out of the queue.
--
-- expires_at was set when the member joined the queue, and ExpireStale treated
-- 'pending' and 'ready' alike -- so a member waiting for a popular title lost
-- their place after three days even though no copy had come back and they had
-- never been offered anything.
--
-- The hold period is a collection deadline, not a limit on how long one may
-- wait in line. A pending reservation now has no expiry; PromoteNext sets one
-- when a copy is actually held.
ALTER TABLE reservations ALTER COLUMN expires_at DROP NOT NULL;
UPDATE reservations SET expires_at = NULL WHERE status = 'pending';

-- Only a held copy can go uncollected.
CREATE INDEX reservations_ready_expiry_idx ON reservations (expires_at)
    WHERE status = 'ready';
