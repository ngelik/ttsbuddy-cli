package wallet

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
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
		if r.Method != http.MethodPost || r.URL.Path != "/platform/v2/evm/accounts/"+responseSigner.Address()+"/sign/typed-data" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || r.Header.Get("X-Wallet-Auth") == "" || r.Header.Get("X-Idempotency-Key") == "" {
			t.Fatal("expected CDP auth and idempotency headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		wantBody := map[string]any{
			"domain": map[string]any{
				"name":              "USD Coin",
				"version":           "2",
				"chainId":           float64(84532),
				"verifyingContract": "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
			},
			"types": map[string]any{
				"TransferWithAuthorization": []any{
					map[string]any{"name": "from", "type": "address"},
					map[string]any{"name": "to", "type": "address"},
					map[string]any{"name": "value", "type": "uint256"},
				},
			},
			"primaryType": "TransferWithAuthorization",
			"message": map[string]any{
				"from":  responseSigner.Address(),
				"to":    "0x1111111111111111111111111111111111111111",
				"value": "10000",
			},
		}
		if !reflect.DeepEqual(body, wantBody) {
			t.Fatalf("typed data was not encoded losslessly:\n got: %#v\nwant: %#v", body, wantBody)
		}
		assertWalletJWT(t, r.Header.Get("X-Wallet-Auth"), r.Method+" "+r.Host+r.URL.Path, body)
		signature, err := responseSigner.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message)
		if err != nil {
			t.Fatal(err)
		}
		// CDP may return 0/1 recovery values; the x402 signer contract needs 27/28.
		signature[64] -= 27
		_ = json.NewEncoder(w).Encode(map[string]string{"signature": "0x" + hex.EncodeToString(signature)})
	}))
	defer server.Close()

	signer, err := newCDPSigner(testCDPCredentials(t), server.URL+"/platform", &http.Client{Timeout: time.Second})
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

func assertWalletJWT(t *testing.T, token, wantURI string, body map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("wallet auth must be a JWT")
	}
	var claims struct {
		URIs        []string `json:"uris"`
		RequestHash string   `json:"reqHash"`
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if len(claims.URIs) != 1 || claims.URIs[0] != wantURI {
		t.Fatalf("wrong wallet JWT uri: %#v", claims.URIs)
	}
	bodyBytes, _ := json.Marshal(body)
	hash := sha256.Sum256(bodyBytes)
	if claims.RequestHash != hex.EncodeToString(hash[:]) {
		t.Fatal("wallet JWT body hash mismatch")
	}
	// The exact JWT input is signed as SHA-256(input), per ES256/JWS.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatal("invalid wallet JWT signature")
	}
}

func TestWalletJWTSignsSHA256Digest(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	token, err := walletJWT(key, http.MethodPost, "api.example.test", "/platform/v2/evm/accounts/0x1/sign/typed-data", []byte(`{"message":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("expected JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatal("invalid raw ES256 signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
		t.Fatal("wallet JWT did not sign SHA-256 JWT input")
	}
}

func TestCDPSignerFailsClosedForTransportAndResponseFailures(t *testing.T) {
	responseSigner, err := evmsigners.NewClientSignerFromPrivateKey(localTestPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	domain, types, message := cdpTypedDataFixture(responseSigner.Address())
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			signer, err := newCDPSigner(testCDPCredentials(t), server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = signer.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
	for _, response := range []string{`{"signature":"0x00"}`, `not-json`} {
		t.Run("malformed response", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			signer, err := newCDPSigner(testCDPCredentials(t), server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = signer.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message); err == nil {
				t.Fatal("expected malformed response failure")
			}
		})
	}
}

func TestCDPSignerTimesOutAndBoundsResponses(t *testing.T) {
	responseSigner, err := evmsigners.NewClientSignerFromPrivateKey(localTestPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	domain, types, message := cdpTypedDataFixture(responseSigner.Address())

	t.Run("HTTP timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		signer, err := newCDPSigner(testCDPCredentials(t), server.URL, &http.Client{Timeout: 25 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		_, err = signer.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message)
		if err == nil {
			t.Fatal("expected HTTP timeout to fail closed")
		}
		if time.Since(started) > time.Second {
			t.Fatal("HTTP timeout did not bound the signing request")
		}
	})

	t.Run("response larger than 64 KiB", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", cdpResponseMax+1)))
		}))
		defer server.Close()
		signer, err := newCDPSigner(testCDPCredentials(t), server.URL, &http.Client{Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = signer.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message); err == nil {
			t.Fatal("expected oversized CDP response to fail closed")
		}
	})
}

func TestCDPSignerRejectsNonP256ECDSACredentials(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		curve elliptic.Curve
	}{
		{name: "P-384", curve: elliptic.P384()},
		{name: "P-521", curve: elliptic.P521()},
	} {
		t.Run(testCase.name+" API key", func(t *testing.T) {
			key, err := ecdsa.GenerateKey(testCase.curve, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			der, err := x509.MarshalECPrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			credentials := testCDPCredentials(t)
			credentials["CDP_API_KEY_SECRET"] = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
			if _, err = newCDPSigner(credentials, "https://api.example.test", nil); err == nil {
				t.Fatalf("expected %s API key rejection", testCase.name)
			}
		})

		t.Run(testCase.name+" wallet key", func(t *testing.T) {
			key, err := ecdsa.GenerateKey(testCase.curve, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			credentials := testCDPCredentials(t)
			credentials["CDP_WALLET_SECRET"] = base64.StdEncoding.EncodeToString(der)
			if _, err = newCDPSigner(credentials, "https://api.example.test", nil); err == nil {
				t.Fatalf("expected %s wallet key rejection", testCase.name)
			}
		})
	}
}

func TestParseCDPAPICredentialAcceptsP256ECKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	_, parsed, err := parseCDPAPICredential(string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})))
	if err != nil || parsed == nil || !isP256PrivateKey(parsed) {
		t.Fatalf("expected P-256 API credential acceptance, got key=%v err=%v", parsed != nil, err)
	}
}

func TestCDPSignerRejectsWrongSignerAndEntropyFailure(t *testing.T) {
	configured, _ := evmsigners.NewClientSignerFromPrivateKey(localTestPrivateKey)
	wrong, _ := evmsigners.NewClientSignerFromPrivateKey("0x59c6995e998f97a5a0044966f094538eD1A0dDeA3dA9d19eE4f4eFf9b0fF1b5a")
	domain, types, message := cdpTypedDataFixture(configured.Address())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		signature, _ := wrong.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message)
		_ = json.NewEncoder(w).Encode(map[string]string{"signature": "0x" + hex.EncodeToString(signature)})
	}))
	defer server.Close()
	signer, err := newCDPSigner(testCDPCredentials(t), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = signer.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message); err == nil {
		t.Fatal("expected wrong signer rejection")
	}

	randomReader = failingReader{}
	defer func() { randomReader = rand.Reader }()
	if _, err = signer.SignTypedData(context.Background(), domain, types, "TransferWithAuthorization", message); err == nil {
		t.Fatal("expected entropy failure")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

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
