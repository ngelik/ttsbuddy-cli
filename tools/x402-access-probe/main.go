// x402-access-probe is a fail-closed Base Sepolia compatibility probe.
// Its default mode reports readiness only. Payment creation requires --submit
// plus the complete frozen public tuple in environment variables.
package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/ngelik/ttsbuddy-cli/internal/wallet"
)

func main() {
	os.Exit(runMain(os.Args[1:]))
}

func runMain(args []string) int {
	encoder := json.NewEncoder(os.Stdout)
	if len(args) == 0 {
		_ = encoder.Encode(readinessRecord())
		return 0
	}
	if len(args) != 1 || args[0] != "--submit" {
		_ = encoder.Encode(map[string]string{"outcome": outcomeRejected})
		return 2
	}

	config, err := loadSubmitConfig(os.LookupEnv)
	if err != nil {
		_ = encoder.Encode(receiptFor(submitConfig{}, outcomeRejected))
		return 2
	}
	receipt, err := executeSubmission(context.Background(), config, defaultDependencies())
	_ = encoder.Encode(receipt)
	if err != nil {
		return 1
	}
	return 0
}

func readinessRecord() map[string]interface{} {
	result := map[string]interface{}{
		"x402_version":             frozenVersion,
		"scheme":                   frozenScheme,
		"network":                  frozenNetwork,
		"payment_flow":             frozenPaymentFlow,
		"asset":                    frozenAsset,
		"amount":                   frozenAmount,
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
	return result
}

func configured(names ...string) bool {
	for _, name := range names {
		if os.Getenv(name) == "" {
			return false
		}
	}
	return true
}
