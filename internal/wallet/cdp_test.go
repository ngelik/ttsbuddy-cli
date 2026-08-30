package wallet

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/x402-foundation/x402/go/v2/mechanisms/evm"
	evmsigners "github.com/x402-foundation/x402/go/v2/signers/evm"
)

type testCredentials map[string]string

func (c testCredentials) LookupEnv(name string) (string, bool) {
	value, ok := c[name]
	return value, ok
}

func TestCDPSignerSendsExactTypedDataAndNormalizesSignature(t *testing.T) {
	responseSigner, err := evmsigners.NewClientSignerFromPrivateKey(localTestPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	domain, types, message := cdpTypedDataFixture(responseSigner.Address())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/evm/accounts/"+responseSigner.Address()+"/sign/typed-data" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || r.Header.Get("X-Wallet-Auth") == "" || r.Header.Get("X-Idempotency-Key") == "" {
			t.Fatal("expected CDP auth and idempotency headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["primaryType"] != "TransferWithAuthorization" || body["domain"].(map[string]any)["chainId"] != float64(84532) {
			t.Fatalf("typed data was not encoded losslessly: %#v", body)
		}
		signature, err := responseSigner.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message)
		if err != nil {
			t.Fatal(err)
		}
		// CDP may return 0/1 recovery values; the x402 signer contract needs 27/28.
		signature[64] -= 27
		_ = json.NewEncoder(w).Encode(map[string]string{"signature": "0x" + hex.EncodeToString(signature)})
	}))
	defer server.Close()

	signer, err := newCDPSigner(testCDPCredentials(t), server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatalf("newCDPSigner() error = %v", err)
	}
	signature, err := signer.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message)
	if err != nil {
		t.Fatalf("SignTypedData() error = %v", err)
	}
	if len(signature) != 65 || (signature[64] != 27 && signature[64] != 28) {
		t.Fatalf("expected x402-compatible 65-byte signature, got %x", signature)
	}
}

func TestCDPSignerRedactsCredentialsAndRejectsRedirects(t *testing.T) {
	credentials := testCDPCredentials(t)
	credentials["CDP_API_KEY_SECRET"] = "api-secret-value"
	credentials["CDP_WALLET_SECRET"] = "wallet-secret-value"
	_, err := newCDPSigner(credentials, "http://example.invalid", nil)
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("expected redacted credential validation error, got %v", err)
	}

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, nil, "http://example.invalid", http.StatusFound)
	}))
	defer redirect.Close()
	responseSigner, _ := evmsigners.NewClientSignerFromPrivateKey(localTestPrivateKey)
	domain, types, message := cdpTypedDataFixture(responseSigner.Address())
	signer, err := newCDPSigner(testCDPCredentials(t), redirect.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = signer.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("expected redacted redirect rejection, got %v", err)
	}
}

func cdpTypedDataFixture(address string) (evm.TypedDataDomain, map[string][]evm.TypedDataField, map[string]interface{}) {
	return evm.TypedDataDomain{Name: "USD Coin", Version: "2", ChainID: big.NewInt(84532), VerifyingContract: "0x036CbD53842c5426634e7929541eC2318f3dCF7e"}, map[string][]evm.TypedDataField{
		"TransferWithAuthorization": {{Name: "from", Type: "address"}, {Name: "to", Type: "address"}, {Name: "value", Type: "uint256"}},
	}, map[string]interface{}{"from": address, "to": "0x1111111111111111111111111111111111111111", "value": big.NewInt(10000)}
}

func testCDPCredentials(t *testing.T) testCredentials {
	t.Helper()
	_, apiSecret, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	walletKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	walletDER, err := x509.MarshalPKCS8PrivateKey(walletKey)
	if err != nil {
		t.Fatal(err)
	}
	return testCredentials{
		"CDP_API_KEY_ID":                   "api-key-id",
		"CDP_API_KEY_SECRET":               base64.StdEncoding.EncodeToString(apiSecret),
		"CDP_WALLET_SECRET":                base64.StdEncoding.EncodeToString(walletDER),
		"TTSBUDDY_CDP_EVM_ACCOUNT_ADDRESS": "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
	}
}
