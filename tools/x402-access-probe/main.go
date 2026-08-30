// x402-access-probe reports whether the explicit live-proof inputs are present.
// It intentionally never creates, funds, imports, or exports a wallet, and it never
// submits a payment without a separately reviewed live-proof implementation.
package main

import (
	"encoding/json"
	"os"

	"github.com/ngelik/ttsbuddy-cli/internal/wallet"
)

func main() {
	result := map[string]interface{}{
		"x402_version":             2,
		"network":                  "eip155:84532",
		"local_signer_configured":  os.Getenv("TTSBUDDY_EVM_PRIVATE_KEY") != "",
		"cdp_signer_configured":    configured("CDP_API_KEY_ID", "CDP_API_KEY_SECRET", "CDP_WALLET_SECRET", "TTSBUDDY_CDP_EVM_ACCOUNT_ADDRESS"),
		"live_payment_submitted":   false,
		"wallet_created_or_funded": false,
	}
	if _, err := wallet.NewLocalFromEnvironment(); err != nil {
		result["local_signer_ready"] = false
	} else {
		result["local_signer_ready"] = true
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

func configured(names ...string) bool {
	for _, name := range names {
		if os.Getenv(name) == "" {
			return false
		}
	}
	return true
}
