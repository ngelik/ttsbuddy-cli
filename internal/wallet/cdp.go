package wallet

import (
	"bytes"
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
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/x402-foundation/x402/go/v2/mechanisms/evm"
)

const (
	cdpAPIURL       = "https://api.cdp.coinbase.com/platform"
	cdpResponseMax  = 64 << 10
	cdpRequestLimit = 10 * time.Second
)

// randomReader is replaceable only by package tests; production always uses
// crypto/rand.Reader and fails closed if entropy is unavailable.
var randomReader io.Reader = rand.Reader

// CredentialSource keeps CDP credentials out of callers' constructor arguments.
type CredentialSource interface {
	LookupEnv(string) (string, bool)
}

type environmentCredentials struct{}

func (environmentCredentials) LookupEnv(name string) (string, bool) { return os.LookupEnv(name) }

// NewCDPFromEnvironment creates a signer for an already-existing CDP EVM account.
func NewCDPFromEnvironment() (Signer, error) {
	return newCDPSigner(environmentCredentials{}, cdpAPIURL, nil)
}

type cdpSigner struct {
	address    string
	apiKeyID   string
	apiPrivate ed25519.PrivateKey
	apiEC      *ecdsa.PrivateKey
	walletKey  *ecdsa.PrivateKey
	baseURL    *url.URL
	httpClient *http.Client
}

func newCDPSigner(source CredentialSource, baseURL string, client *http.Client) (Signer, error) {
	if source == nil {
		return nil, errors.New("CDP credential source is required")
	}
	apiID, okID := source.LookupEnv("CDP_API_KEY_ID")
	apiSecret, okSecret := source.LookupEnv("CDP_API_KEY_SECRET")
	walletSecret, okWallet := source.LookupEnv("CDP_WALLET_SECRET")
	address, okAddress := source.LookupEnv("TTSBUDDY_CDP_EVM_ACCOUNT_ADDRESS")
	if !okID || apiID == "" || !okSecret || apiSecret == "" || !okWallet || walletSecret == "" || !okAddress || !isAddress(address) {
		return nil, errors.New("CDP signer requires CDP_API_KEY_ID, CDP_API_KEY_SECRET, CDP_WALLET_SECRET, and TTSBUDDY_CDP_EVM_ACCOUNT_ADDRESS")
	}
	apiPrivate, apiEC, err := parseCDPAPICredential(apiSecret)
	if err != nil {
		return nil, errors.New("invalid CDP API credential")
	}
	walletBytes, err := base64.StdEncoding.DecodeString(walletSecret)
	if err != nil {
		return nil, errors.New("invalid CDP wallet credential")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(walletBytes)
	walletKey, ok := parsed.(*ecdsa.PrivateKey)
	if err != nil || !ok || !isP256PrivateKey(walletKey) {
		return nil, errors.New("invalid CDP wallet credential")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
		return nil, errors.New("invalid CDP endpoint")
	}
	if client == nil {
		client = &http.Client{}
	}
	// Authenticated requests must never follow a redirect to another origin.
	// Clone rather than mutate a caller-owned client used by a test or another command.
	configuredClient := *client
	if configuredClient.Timeout == 0 {
		configuredClient.Timeout = cdpRequestLimit
	}
	configuredClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	client = &configuredClient
	return &cdpSigner{address: address, apiKeyID: apiID, apiPrivate: apiPrivate, apiEC: apiEC, walletKey: walletKey, baseURL: u, httpClient: client}, nil
}

func (s *cdpSigner) Address() string { return s.address }

func (s *cdpSigner) SignTypedData(ctx context.Context, domain evm.TypedDataDomain, types map[string][]evm.TypedDataField, primaryType string, message map[string]interface{}) ([]byte, error) {
	if domain.ChainID == nil || !domain.ChainID.IsInt64() || primaryType == "" {
		return nil, errors.New("invalid CDP typed data")
	}
	body := map[string]interface{}{"domain": map[string]interface{}{"name": domain.Name, "version": domain.Version, "chainId": domain.ChainID.Int64(), "verifyingContract": domain.VerifyingContract}, "types": types, "primaryType": primaryType, "message": normalizeTypedData(message)}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("invalid CDP typed data")
	}
	endpointPath := "/v2/evm/accounts/" + s.address + "/sign/typed-data"
	u := *s.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + endpointPath
	path := u.EscapedPath()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, errors.New("unable to create CDP signing request")
	}
	apiJWT, err := s.apiJWT(req.Method, u.Host, path)
	if err != nil {
		return nil, errors.New("unable to authenticate CDP signing request")
	}
	walletJWT, err := walletJWT(s.walletKey, req.Method, u.Host, path, bodyBytes)
	if err != nil {
		return nil, errors.New("unable to authenticate CDP signing request")
	}
	idempotencyKey, err := randomID()
	if err != nil {
		return nil, errors.New("unable to authenticate CDP signing request")
	}
	req.Header.Set("Authorization", "Bearer "+apiJWT)
	req.Header.Set("X-Wallet-Auth", walletJWT)
	req.Header.Set("X-Idempotency-Key", idempotencyKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, errors.New("CDP signing request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, cdpResponseMax+1))
	if err != nil || len(data) > cdpResponseMax || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errors.New("CDP signing request failed")
	}
	var decoded struct {
		Signature string `json:"signature"`
	}
	if json.Unmarshal(data, &decoded) != nil {
		return nil, errors.New("invalid CDP signing response")
	}
	signature, err := hex.DecodeString(strings.TrimPrefix(decoded.Signature, "0x"))
	if err != nil || len(signature) != 65 {
		return nil, errors.New("invalid CDP signature")
	}
	if signature[64] == 0 || signature[64] == 1 {
		signature[64] += 27
	}
	if signature[64] != 27 && signature[64] != 28 {
		return nil, errors.New("invalid CDP signature")
	}
	valid, err := evm.VerifyEOATypedData(s.address, domain, types, primaryType, message, signature)
	if err != nil || !valid {
		return nil, errors.New("CDP signature did not match configured EVM account")
	}
	return signature, nil
}

func (s *cdpSigner) apiJWT(method, host, path string) (string, error) {
	nonce, err := randomID()
	if err != nil {
		return "", err
	}
	header := map[string]interface{}{"kid": s.apiKeyID, "nonce": nonce, "typ": "JWT"}
	if s.apiEC != nil {
		header["alg"] = "ES256"
		return signJWT(header, cdpClaims(s.apiKeyID, method, host, path), func(data []byte) ([]byte, error) { return signECDSADigest(s.apiEC, data) })
	}
	header["alg"] = "EdDSA"
	return signJWT(header, cdpClaims(s.apiKeyID, method, host, path), func(data []byte) ([]byte, error) { return ed25519.Sign(s.apiPrivate, data), nil })
}

func walletJWT(key *ecdsa.PrivateKey, method, host, path string, body []byte) (string, error) {
	if !isP256PrivateKey(key) {
		return "", errors.New("wallet JWT requires a P-256 key")
	}
	hash := sha256.Sum256(body)
	jti, err := randomID()
	if err != nil {
		return "", err
	}
	return signJWT(map[string]interface{}{"alg": "ES256", "typ": "JWT"}, map[string]interface{}{"uris": []string{method + " " + host + path}, "iat": time.Now().Unix(), "nbf": time.Now().Unix(), "jti": jti, "reqHash": hex.EncodeToString(hash[:])}, func(data []byte) ([]byte, error) { return signECDSADigest(key, data) })
}

func cdpClaims(apiKeyID, method, host, path string) map[string]interface{} {
	now := time.Now()
	return map[string]interface{}{"sub": apiKeyID, "iss": "cdp", "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(120 * time.Second).Unix(), "uris": []string{method + " " + host + path}}
}

func signECDSADigest(key *ecdsa.PrivateKey, data []byte) ([]byte, error) {
	if !isP256PrivateKey(key) {
		return nil, errors.New("ES256 requires a P-256 key")
	}
	digest := sha256.Sum256(data)
	r, s, err := ecdsa.Sign(randomReader, key, digest[:])
	if err != nil {
		return nil, err
	}
	size := (key.Curve.Params().BitSize + 7) / 8
	out := make([]byte, size*2)
	r.FillBytes(out[:size])
	s.FillBytes(out[size:])
	return out, nil
}

func signJWT(header, claims map[string]interface{}, sign func([]byte) ([]byte, error)) (string, error) {
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sig, err := sign([]byte(input))
	if err != nil {
		return "", err
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(randomReader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func parseCDPAPICredential(value string) (ed25519.PrivateKey, *ecdsa.PrivateKey, error) {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(decoded), nil, nil
	}
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, nil, errors.New("unsupported CDP API credential")
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		if !isP256PrivateKey(key) {
			return nil, nil, errors.New("unsupported CDP API credential")
		}
		return nil, key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, errors.New("unsupported CDP API credential")
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("unsupported CDP API credential")
	}
	if !isP256PrivateKey(ec) {
		return nil, nil, errors.New("unsupported CDP API credential")
	}
	return nil, ec, nil
}

func isP256PrivateKey(key *ecdsa.PrivateKey) bool {
	return key != nil && key.Curve == elliptic.P256()
}
func normalizeTypedData(value interface{}) interface{} {
	switch v := value.(type) {
	case *big.Int:
		return v.String()
	case []byte:
		return "0x" + hex.EncodeToString(v)
	case [32]byte:
		return "0x" + hex.EncodeToString(v[:])
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, x := range v {
			out[k] = normalizeTypedData(x)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, x := range v {
			out[i] = normalizeTypedData(x)
		}
		return out
	default:
		return value
	}
}
func isAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}
