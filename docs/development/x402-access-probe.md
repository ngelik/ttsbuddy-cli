# x402 access probe

Run `make x402-access-probe` to emit a redacted, environment-only readiness record for the pinned x402 v2.24.0 compatibility work. It reports booleans and fixed protocol metadata only; it never prints environment values, private keys, CDP secrets, payment headers, or pass credentials.

The probe never creates, imports, exports, or funds a wallet, and it never submits a payment. A successful local readiness result is not live Base Sepolia or CDP proof. Before a beta can proceed, a separately authorized run must use already-existing disposable wallets and record only the version, CAIP-2 network, asset, amount, payee, transaction hash, facilitator request IDs, and pass/fail.

The local suite validates the x402 Deno adapter against a localhost facilitator and validates both signer request contracts with deterministic keys and `httptest`. It does not establish the required live facilitator settlement or CDP remote-signature acceptance.
