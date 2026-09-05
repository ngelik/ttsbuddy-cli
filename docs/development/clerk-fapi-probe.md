# Clerk FAPI probe

Run `make clerk-auth-probe` to exercise the browserless Clerk Frontend API login scaffold against a development Clerk instance without printing raw responses, tokens, OTPs, session identifiers, or email addresses. Pass `go run ./tools/clerk-fapi-probe --signup` (or add an equivalent Make target invocation) to exercise the new-account path.

Required inputs:

- `TTSBUDDY_CLERK_FRONTEND_API_URL` set to the public development Frontend API origin
- an already-existing development account that can receive Clerk email OTP messages for the default login probe, or a genuinely new development identity for `--signup`; do not replace or delete a user for either probe
- an interactive terminal for entering the email address and hidden OTP

If `TTSBUDDY_CLERK_FRONTEND_API_URL` is absent, the probe exits with a development-only hard gate report. That record keeps the live gate closed and prints only sanitized booleans plus the missing-variable reason. Protocol failures are reported as the generic `probe failed` outcome so an unknown email cannot be distinguished from an eligible one by CLI output.

Signup-specific behavior:

- `--signup` sends exactly one `email_address` signup request followed by one `email_code` verification preparation request.
- It never supplies `legal_accepted`, CAPTCHA, password, or other inferred fields. If the instance requires them, the probe reports a generic failure and the terminal flow must fall back to browser authentication.
- The signup-created session is checked through the same active-session and session-token gates as ordinary email login before the sanitized proof record is emitted.

What the probe records:

- redacted stage names and pass/fail status
- the pinned Frontend API version, native query flag, and request-header names
- the flow stage (`start_email_signup`/`verify_email_signup` for `--signup`, or the login equivalents)
- whether a session id was issued
- decoded JWT claim keys and JSON value types only
- UTC timestamps and Clerk request ids when the server supplies them
- a fixed internal protocol-stage label on failure (`attempt_first_factor`, `validate_sign_in`, `attempt_verification`, `validate_signup`, `get_session`, `validate_session`, or `create_session_token`)

What the probe does not record:

- the email address
- the OTP
- the native client token
- the Clerk JWT value
- the raw Clerk `sid`
- raw Clerk responses

Interruption behavior:

- `Ctrl-C` cancels the in-flight request context
- the probe then attempts best-effort cleanup through `Client.Cleanup`
- on any post-client failure, cleanup is attempted and a redacted cleanup pass/fail stage is printed

Hard gate:

- the development gate passed on 2026-08-30 using the existing account, Strict enumeration protection, `_is_native=true`, and FAPI `2026-05-12`
- the initial native client response returned a raw token in the `Authorization` response header; requests sent it as Bearer authentication, and later responses were allowed to omit rotation while retaining the current token
- the successful existing-account flow proved email-code preparation, verification, active-session retrieval, JWT minting, and session cleanup without creating or deleting a Clerk user
- a successful `--signup` run proves signup creation, email-code preparation, verification, active-session retrieval, JWT minting, and session cleanup; retain only its sanitized JSON receipt and verify the resulting Clerk user/subscription through the approved development checks
- failure-branch behavior remains covered by the mock-backed protocol tests; the live receipt stores only fixed state/claim metadata and pass/fail status
- if the required development Frontend API environment variable is absent, the probe reports `development_only=true` and `live_gate_open=false` instead of attempting any network activity
- if the published Frontend API schema is insufficient to prove the native client token rotation contract or the exact cleanup behavior in that environment, stop and write a new CLI-specific PKCE design instead of guessing
