package wallet

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/x402-foundation/x402/go/v2/mechanisms/evm"
)

const localTestPrivateKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

func TestNewLocalFromEnvironmentRejectsMissingKey(t *testing.T) {
	t.Setenv("TTSBUDDY_EVM_PRIVATE_KEY", "")

	_, err := NewLocalFromEnvironment()
	if err == nil || strings.Contains(err.Error(), "TTSBUDDY_EVM_PRIVATE_KEY=") {
		t.Fatalf("expected a redacted missing-key error, got %v", err)
	}
}

func TestNewLocalFromEnvironmentRedactsMalformedKey(t *testing.T) {
	t.Setenv("TTSBUDDY_EVM_PRIVATE_KEY", "not-a-private-key")

	_, err := NewLocalFromEnvironment()
	if err == nil {
		t.Fatal("expected malformed key to be rejected")
	}
	if strings.Contains(err.Error(), "not-a-private-key") {
		t.Fatalf("private key leaked through error: %v", err)
	}
}

func TestLocalSignerDerivesAddressAndProducesEIP712Signature(t *testing.T) {
	t.Setenv("TTSBUDDY_EVM_PRIVATE_KEY", localTestPrivateKey)

	signer, err := NewLocalFromEnvironment()
	if err != nil {
		t.Fatalf("NewLocalFromEnvironment() error = %v", err)
	}
	if got, want := signer.Address(), "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"; got != want {
		t.Fatalf("Address() = %q, want %q", got, want)
	}

	signature, err := signer.SignTypedData(
		context.Background(),
		evm.TypedDataDomain{
			Name:              "USD Coin",
			Version:           "2",
			ChainID:           big.NewInt(84532),
			VerifyingContract: "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
		},
		map[string][]evm.TypedDataField{
			"TransferWithAuthorization": {
				{Name: "from", Type: "address"},
				{Name: "to", Type: "address"},
				{Name: "value", Type: "uint256"},
			},
		},
		"TransferWithAuthorization",
		map[string]interface{}{
			"from":  signer.Address(),
			"to":    "0x1111111111111111111111111111111111111111",
			"value": big.NewInt(10000),
		},
	)
	if err != nil {
		t.Fatalf("SignTypedData() error = %v", err)
	}
	if len(signature) != 65 || (signature[64] != 27 && signature[64] != 28) {
		t.Fatalf("expected a 65-byte Ethereum signature with v 27 or 28, got %x", signature)
	}
}
