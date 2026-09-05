# Local development CLI email-auth receipt

This is a sanitized, local-only receipt. It contains no mailbox address,
verification code, Clerk or Supabase identifier, credential component, token,
hash, or raw response.

## Candidate and timestamps

- CLI implementation candidate: `16e22b3dd02b05f33624ee7b9de76f7d2aa63a73`
  (the preceding signup attempt used `e93d41abcecb30c931cada34b154970bd6985d43`).
- Backend source pin used for the local function: `feef6e7a`.
- The initial fresh-signup run verified the email code, then its local
  `cli-auth` exchange returned HTTP 401 before the local Clerk JWT issuer/JWKS
  verifier was configured. No CLI credential was written; the created
  development identity was retained for ordinary-login recovery. This is not
  a completed signup acceptance proof.
- After the scoped local verifier configuration, ordinary email login and
  backend exchange succeeded at `2026-09-05T06:22:09Z`; the issued session had
  a seven-day expiry of `2026-09-12T06:22:09Z`.

## Local account initialization

Read-only service-role queries, filtered privately by the session's public
component, returned before logout:

- `user_subscriptions`: exactly 1 row, `status=active`, `email=NULL`.
- Its tier was `free` with `api_access=true`.
- `user_settings`: exactly 1 row.
- `api_keys`: exactly 1 active `cli_session` row and 0 permanent-key rows.

## Logout and revocation

- `auth logout --json` exited 0 and returned `status=revoked`.
- The local CLI session file was removed.
- Post-logout read-only aggregates were unchanged for subscription, tier,
  email-null, and settings rows; active CLI-session rows were 0 and permanent
  key rows were 0.
- The `/cli-auth` status endpoint returned HTTP 200 with
  `credential.status=revoked`, which is the current intentional contract for
  status inspection. Resource-access rejection using the revoked credential
  against the TTS endpoint was not tested in this lifecycle because the
  retained credential was deleted after logout; it remains a required check
  in the next fresh lifecycle. Do not treat this receipt as proof of that
  rejection or of fresh-signup acceptance.

All temporary credential material and the one-use code handoff file were
removed. No production deployment, publication, tag, or release was performed.
