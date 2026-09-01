// Package wallet contains the narrow signing contract required by x402 clients.
package wallet

import (
	"context"

	"github.com/x402-foundation/x402/go/v2/mechanisms/evm"
)

// Signer is intentionally the same narrow typed-data contract required by x402.
// It does not expose key material, wallet creation, or generic custody operations.
type Signer interface {
	Address() string
	SignTypedData(
		context.Context,
		evm.TypedDataDomain,
		map[string][]evm.TypedDataField,
		string,
		map[string]interface{},
	) ([]byte, error)
}
