# x402 access probe

Run `make x402-access-probe` to emit a redacted, environment-only readiness record for the pinned x402 v2.24.0 compatibility work. It reports booleans and fixed protocol metadata only; it never prints environment values, private keys, CDP secrets, payment headers, or pass credentials.

The probe never creates, imports, exports, or funds a wallet, and it never submits a payment. A successful local readiness result is not live Base Sepolia or CDP proof. Before a beta can proceed, a separately authorized run must use already-existing disposable wallets and record only the version, CAIP-2 network, asset, amount, payee, transaction hash, facilitator request IDs, and pass/fail.

This command is intentionally a readiness probe, not a payment executor: it cannot provide live proof by itself because doing so would submit an authorization and settlement. The live proof path must be a separately reviewed command using `TTSBUDDY_EVM_PRIVATE_KEY` or the existing CDP account credentials, with an already-funded disposable account; this repository deliberately provides no wallet provisioning or funding path. Until that command is implemented and authorized, the hard gate remains closed.

The local suite validates the x402 Deno adapter against a deterministic fake facilitator transport and validates both signer request contracts with deterministic keys and `httptest`. It does not establish the required live facilitator settlement or CDP remote-signature acceptance.
