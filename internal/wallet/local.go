package wallet

import (
	"errors"
	"os"

	evmsigners "github.com/x402-foundation/x402/go/v2/signers/evm"
)

const localPrivateKeyEnvironment = "TTSBUDDY_EVM_PRIVATE_KEY"

// NewLocalFromEnvironment constructs an x402 signer from the process environment.
// The key is never accepted from command arguments or retained by this package.
func NewLocalFromEnvironment() (Signer, error) {
	privateKey := os.Getenv(localPrivateKeyEnvironment)
	if privateKey == "" {
		return nil, errors.New("local EVM signer requires TTSBUDDY_EVM_PRIVATE_KEY")
	}

	signer, err := evmsigners.NewClientSignerFromPrivateKey(privateKey)
	if err != nil {
		return nil, errors.New("invalid local EVM private key")
	}

	return signer, nil
}
