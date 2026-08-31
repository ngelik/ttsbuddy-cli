# x402 access probe

The probe has two deliberately separate modes. Both are pinned to x402 v2.24.0, Exact EVM, Base Sepolia (`eip155:84532`), upfront settlement, Base Sepolia USDC `0x036CbD53842c5426634e7929541eC2318f3dCF7e`, and `1000` atomic units.

## Readiness only

```bash
make x402-access-probe
```

This default mode reads signer environment presence and emits booleans plus the public frozen tuple. It does not contact a resource or facilitator, create an authorization, submit a payment, create a wallet, or fund a wallet. It never prints environment values, keys, CDP credentials, or signatures.

## Explicit Base Sepolia submission

Submission is available only through the literal `--submit` argument:

```bash
export X402_RESOURCE_URL=http://127.0.0.1:54321/functions/v1/x402-compatibility-probe
export X402_SIGNER_BACKEND=local
export X402_ASSET_ADDRESS=0x036CbD53842c5426634e7929541eC2318f3dCF7e
export X402_PAYEE_ADDRESS=0xCONTROLLED_BASE_SEPOLIA_PAYEE
export X402_PAYMENT_AMOUNT=1000
export X402_MAX_PAYMENT_AMOUNT=1000
export TTSBUDDY_EVM_PRIVATE_KEY=0xEXISTING_DISPOSABLE_FUNDED_KEY

go build -buildvcs=true -o /private/tmp/ttsbuddy-x402-access-probe ./tools/x402-access-probe
go version -m /private/tmp/ttsbuddy-x402-access-probe
/private/tmp/ttsbuddy-x402-access-probe --submit
```

Before executing the binary, confirm that `go version -m` reports both the intended `vcs.revision` and `vcs.modified=false`. The probe rejects a missing revision, missing clean-state proof, or `vcs.modified=true`; a dirty checkout cannot produce live acceptance evidence. The current uncommitted implementation must therefore be reviewed and committed before a live proof can run. `go run ./tools/x402-access-probe --submit` is not accepted as live acceptance evidence because its build metadata may omit the VCS revision; the probe correctly fails closed when that revision is absent.

For an already-existing CDP account, set `X402_SIGNER_BACKEND=cdp` and supply `CDP_API_KEY_ID`, `CDP_API_KEY_SECRET`, `CDP_WALLET_SECRET`, and `TTSBUDDY_CDP_EVM_ACCOUNT_ADDRESS` in the environment. Credentials and signatures are never accepted as command arguments. The probe has no wallet creation, import, export, faucet, or funding operation.

Before signing, submit mode requires:

- the exact local Supabase compatibility URL shape: loopback host, port `54321`, path `/functions/v1/x402-compatibility-probe`, and no credentials, query, or fragment;
- the exact frozen asset, amount, and identical maximum amount;
- a valid nonzero controlled payee;
- `local` or `cdp` as the signer backend;
- official facilitator `/supported` confirmation for x402 v2 Exact EVM on Base Sepolia;
- one bounded `402` response containing a non-null resource whose canonical URL exactly matches the configured URL and exactly one matching requirement, including `extra.paymentFlow=upfront` and a 15-second timeout;
- no EIP-712 domain overrides, or the exact `name=USDC` and `version=2` overrides used by Base Sepolia USDC;
- an `X-TTSBuddy-Server-Commit` identifier on both the unpaid and paid responses.

All HTTP requests have 15-second timeouts, response headers and bodies are bounded, and redirects are rejected. The probe creates at most one EIP-3009 authorization using the official x402 Go v2.24.0 client. It locally checks that the signature recovers to the configured signer. Immediately before the first paid request, the outcome becomes `inconclusive`; only a strict settlement receipt can change it to `settled`. A paid redirect, response-plus-error, 5xx, oversized response, changed server commit, malformed or mismatched settlement, or second transport error therefore remains `inconclusive`. If the first paid request ends without any HTTP response, the probe retries once with the identical encoded authorization; it never signs again.

A successful result requires a 2xx response, an unchanged server commit, and a `PAYMENT-RESPONSE` settlement receipt with `success=true`, the exact Base Sepolia network and amount, and a 32-byte transaction hash. Output is a fixed redacted JSON object containing only the approved tuple, signer backend name, outcome, transaction hash, sanitized facilitator request identifiers, and CLI/server commit identifiers. Sanitized request identifiers from any paid HTTP response are retained even when the outcome is inconclusive. The receipt never includes the payer address, authorization, signature, request headers, response bodies, keys, or credentials.

Unset transient tuple and signer variables after the acceptance run. A readiness result, deterministic test, or unpaid `402` is not live settlement proof.
