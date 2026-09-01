package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ngelik/ttsbuddy-cli/internal/wallet"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"
	"github.com/x402-foundation/x402/go/v2/mechanisms/evm"
	exactclient "github.com/x402-foundation/x402/go/v2/mechanisms/evm/exact/client"
)

const (
	frozenVersion        = 2
	frozenScheme         = "exact"
	frozenNetwork        = "eip155:84532"
	frozenPaymentFlow    = "upfront"
	frozenFacilitatorURL = "https://x402.org/facilitator"
	frozenAsset          = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	frozenAmount         = "1000"
	frozenTimeoutSeconds = 15
	maxResponseBody      = 64 << 10
	localSupabasePort    = "54321"
	localProbePath       = "/functions/v1/x402-compatibility-probe"

	paymentRequiredHeader  = "PAYMENT-REQUIRED"
	paymentSignatureHeader = "PAYMENT-SIGNATURE"
	paymentResponseHeader  = "PAYMENT-RESPONSE"
	serverCommitHeader     = "X-TTSBuddy-Server-Commit"

	outcomeSettled      = "settled"
	outcomeRejected     = "rejected"
	outcomeInconclusive = "inconclusive"
)

type submitConfig struct {
	ResourceURL   string
	SignerBackend string
	Asset         string
	Payee         string
	Amount        string
	MaxAmount     string
}

type receipt struct {
	X402Version           int      `json:"x402_version"`
	Scheme                string   `json:"scheme"`
	Network               string   `json:"network"`
	PaymentFlow           string   `json:"payment_flow"`
	Asset                 string   `json:"asset"`
	Amount                string   `json:"amount"`
	Payee                 string   `json:"payee"`
	SignerBackend         string   `json:"signer_backend"`
	Outcome               string   `json:"outcome"`
	TransactionHash       string   `json:"transaction_hash"`
	FacilitatorRequestIDs []string `json:"facilitator_request_ids"`
	CLICommit             string   `json:"cli_commit"`
	ServerCommit          string   `json:"server_commit"`
}

type dependencies struct {
	httpClient              *http.Client
	newSigner               func(string) (wallet.Signer, error)
	checkFacilitatorSupport func(context.Context, *http.Client) error
	cliCommit               func() string
}

func defaultDependencies() dependencies {
	return dependencies{
		httpClient: newBoundedHTTPClient(),
		newSigner: func(backend string) (wallet.Signer, error) {
			switch backend {
			case "local":
				return wallet.NewLocalFromEnvironment()
			case "cdp":
				return wallet.NewCDPFromEnvironment()
			default:
				return nil, errors.New("unsupported signer backend")
			}
		},
		checkFacilitatorSupport: checkOfficialFacilitatorSupport,
		cliCommit:               currentCLICommit,
	}
}

func newBoundedHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: frozenTimeoutSeconds * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	transport.TLSHandshakeTimeout = frozenTimeoutSeconds * time.Second
	transport.ResponseHeaderTimeout = frozenTimeoutSeconds * time.Second
	transport.MaxResponseHeaderBytes = maxResponseBody
	return &http.Client{
		Transport: transport,
		Timeout:   frozenTimeoutSeconds * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("redirect rejected")
		},
	}
}

func hardenHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return newBoundedHTTPClient()
	}
	copy := *client
	if copy.Timeout == 0 || copy.Timeout > frozenTimeoutSeconds*time.Second {
		copy.Timeout = frozenTimeoutSeconds * time.Second
	}
	copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return errors.New("redirect rejected") }
	return &copy
}

func loadSubmitConfig(lookup func(string) (string, bool)) (submitConfig, error) {
	read := func(name string) (string, error) {
		value, ok := lookup(name)
		if !ok || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("missing %s", name)
		}
		return value, nil
	}
	resourceURL, err := read("X402_RESOURCE_URL")
	if err != nil {
		return submitConfig{}, err
	}
	backend, err := read("X402_SIGNER_BACKEND")
	if err != nil {
		return submitConfig{}, err
	}
	asset, err := read("X402_ASSET_ADDRESS")
	if err != nil {
		return submitConfig{}, err
	}
	payee, err := read("X402_PAYEE_ADDRESS")
	if err != nil {
		return submitConfig{}, err
	}
	amount, err := read("X402_PAYMENT_AMOUNT")
	if err != nil {
		return submitConfig{}, err
	}
	maxAmount, err := read("X402_MAX_PAYMENT_AMOUNT")
	if err != nil {
		return submitConfig{}, err
	}

	canonicalResourceURL, err := canonicalLocalResourceURL(resourceURL)
	if err != nil {
		return submitConfig{}, err
	}
	if backend != "local" && backend != "cdp" {
		return submitConfig{}, errors.New("signer backend must be local or cdp")
	}
	if !strings.EqualFold(asset, frozenAsset) {
		return submitConfig{}, errors.New("asset does not match frozen tuple")
	}
	if !common.IsHexAddress(payee) || common.HexToAddress(payee) == (common.Address{}) {
		return submitConfig{}, errors.New("payee must be a nonzero EVM address")
	}
	if amount != frozenAmount || maxAmount != frozenAmount || amount != maxAmount {
		return submitConfig{}, errors.New("amount does not match frozen cap")
	}
	return submitConfig{ResourceURL: canonicalResourceURL, SignerBackend: backend, Asset: frozenAsset, Payee: common.HexToAddress(payee).Hex(), Amount: amount, MaxAmount: maxAmount}, nil
}

func canonicalLocalResourceURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path != localProbePath || parsed.Port() != localSupabasePort || !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("resource URL must be the explicit local Supabase compatibility endpoint")
	}
	host := net.JoinHostPort(strings.ToLower(parsed.Hostname()), localSupabasePort)
	return strings.ToLower(parsed.Scheme) + "://" + host + localProbePath, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func executeSubmission(ctx context.Context, config submitConfig, deps dependencies) (receipt, error) {
	result := receiptFor(config, outcomeRejected)
	client := hardenHTTPClient(deps.httpClient)
	if deps.checkFacilitatorSupport == nil || deps.newSigner == nil || deps.cliCommit == nil {
		return result, errors.New("probe dependencies unavailable")
	}
	result.CLICommit = deps.cliCommit()
	if result.CLICommit == "" {
		return result, errors.New("CLI commit unavailable")
	}
	if err := deps.checkFacilitatorSupport(ctx, client); err != nil {
		return result, errors.New("facilitator support check failed")
	}

	unpaid, err := sendProbeRequest(ctx, client, config.ResourceURL, "")
	if err != nil {
		return result, errors.New("unpaid probe request failed")
	}
	defer func() { _ = unpaid.Body.Close() }()
	body, err := readBounded(unpaid.Body)
	if err != nil || unpaid.StatusCode != http.StatusPaymentRequired {
		return result, errors.New("resource did not return a bounded 402 challenge")
	}
	result.ServerCommit = sanitizeIdentifier(unpaid.Header.Get(serverCommitHeader))
	if result.ServerCommit == "" {
		return result, errors.New("server commit unavailable")
	}
	required, err := parsePaymentRequired(unpaid.Header, body)
	if err != nil || validateFrozenChallenge(required, config) != nil {
		return result, errors.New("resource challenge rejected")
	}

	signer, err := deps.newSigner(config.SignerBackend)
	if err != nil {
		return result, errors.New("signer unavailable")
	}
	payloadBytes, err := createAndValidateAuthorization(ctx, signer, required)
	if err != nil {
		return result, errors.New("authorization rejected")
	}
	headerMap, err := x402http.Newx402HTTPClient(nil).EncodePaymentSignatureHeader(payloadBytes)
	if err != nil || headerMap[paymentSignatureHeader] == "" {
		return result, errors.New("authorization encoding failed")
	}
	paymentHeader := headerMap[paymentSignatureHeader]

	// From this point onward an authorization may have reached the server. Only a
	// strict settlement receipt may upgrade the result to settled.
	result.Outcome = outcomeInconclusive
	paid, err := sendProbeRequest(ctx, client, config.ResourceURL, paymentHeader)
	if err != nil {
		if paid != nil {
			result.FacilitatorRequestIDs = mergeRequestIDs(result.FacilitatorRequestIDs, collectRequestIDs(paid.Header))
			_ = paid.Body.Close()
			return result, errors.New("paid request rejected")
		}
		paid, err = sendProbeRequest(ctx, client, config.ResourceURL, paymentHeader)
		if err != nil {
			if paid != nil {
				result.FacilitatorRequestIDs = mergeRequestIDs(result.FacilitatorRequestIDs, collectRequestIDs(paid.Header))
				_ = paid.Body.Close()
			}
			return result, errors.New("paid request outcome is ambiguous")
		}
	}
	defer func() { _ = paid.Body.Close() }()
	result.FacilitatorRequestIDs = mergeRequestIDs(result.FacilitatorRequestIDs, collectRequestIDs(paid.Header))
	if _, err := readBounded(paid.Body); err != nil {
		return result, errors.New("paid response exceeded bound")
	}
	if paid.StatusCode < 200 || paid.StatusCode > 299 {
		return result, errors.New("payment settlement failed")
	}
	paidCommit := sanitizeIdentifier(paid.Header.Get(serverCommitHeader))
	if paidCommit == "" || paidCommit != result.ServerCommit {
		return result, errors.New("server commit changed during payment")
	}
	settlement, err := parseSettlement(paid.Header)
	if err != nil || !settlement.Success || !isTransactionHash(settlement.Transaction) || settlement.Network != x402.Network(frozenNetwork) || settlement.Amount != frozenAmount {
		return result, errors.New("strict settlement receipt missing")
	}
	result.Outcome = outcomeSettled
	result.TransactionHash = settlement.Transaction
	return result, nil
}

func isTransactionHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func receiptFor(config submitConfig, outcome string) receipt {
	return receipt{
		X402Version: frozenVersion, Scheme: frozenScheme, Network: frozenNetwork,
		PaymentFlow: frozenPaymentFlow, Asset: frozenAsset, Amount: frozenAmount,
		Payee: config.Payee, SignerBackend: config.SignerBackend, Outcome: outcome,
		FacilitatorRequestIDs: []string{},
	}
}

func sendProbeRequest(ctx context.Context, client *http.Client, resourceURL, paymentHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resourceURL, nil)
	if err != nil {
		return nil, err
	}
	if paymentHeader != "" {
		req.Header.Set(paymentSignatureHeader, paymentHeader)
	}
	return client.Do(req)
}

func readBounded(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBody {
		return nil, errors.New("response body too large")
	}
	return data, nil
}

func headersMap(header http.Header) map[string]string {
	result := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) > 0 {
			result[key] = values[0]
		}
	}
	return result
}

func parsePaymentRequired(header http.Header, body []byte) (x402.PaymentRequired, error) {
	return x402http.Newx402HTTPClient(nil).GetPaymentRequiredResponse(headersMap(header), body)
}

func validateFrozenChallenge(required x402.PaymentRequired, config submitConfig) error {
	if required.X402Version != frozenVersion || len(required.Accepts) != 1 || required.Resource == nil || !sameCanonicalResourceURL(required.Resource.URL, config.ResourceURL) {
		return errors.New("unsupported x402 challenge")
	}
	req := required.Accepts[0]
	flow, ok := req.Extra["paymentFlow"].(string)
	if req.Scheme != frozenScheme || req.Network != frozenNetwork || !strings.EqualFold(req.Asset, config.Asset) || req.Amount != config.Amount || !strings.EqualFold(req.PayTo, config.Payee) || req.MaxTimeoutSeconds != frozenTimeoutSeconds || !ok || flow != frozenPaymentFlow {
		return errors.New("challenge does not match frozen tuple")
	}
	if transferMethod, present := req.Extra["assetTransferMethod"]; present && transferMethod != string(evm.AssetTransferMethodEIP3009) {
		return errors.New("unsupported asset transfer method")
	}
	if name, present := req.Extra["name"]; present && name != "USDC" {
		return errors.New("unsupported signing domain name")
	}
	if version, present := req.Extra["version"]; present && version != "2" {
		return errors.New("unsupported signing domain version")
	}
	return nil
}

func sameCanonicalResourceURL(left, right string) bool {
	canonical := func(raw string) (string, bool) {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
			return "", false
		}
		host := parsed.Hostname()
		if parsed.Port() != "" {
			host = net.JoinHostPort(strings.ToLower(host), parsed.Port())
		} else {
			host = strings.ToLower(host)
		}
		return strings.ToLower(parsed.Scheme) + "://" + host + parsed.EscapedPath(), true
	}
	leftCanonical, leftOK := canonical(left)
	rightCanonical, rightOK := canonical(right)
	return leftOK && rightOK && leftCanonical == rightCanonical
}

func createAndValidateAuthorization(ctx context.Context, signer wallet.Signer, required x402.PaymentRequired) ([]byte, error) {
	requirement := required.Accepts[0]
	client := x402.Newx402Client().SetSpendControls(x402.SpendControls{
		AllowedAssets: []x402.SpendControlAsset{{Network: x402.Network(frozenNetwork), Asset: frozenAsset, MaxAmountPerPayment: frozenAmount}},
	}).Register(x402.Network(frozenNetwork), exactclient.NewExactEvmScheme(signer, nil))
	payload, err := client.CreatePaymentPayload(ctx, requirement, required.Resource, required.Extensions)
	if err != nil {
		return nil, err
	}
	if payload.X402Version != frozenVersion || payload.Accepted.Scheme != frozenScheme || payload.Accepted.Network != frozenNetwork || !strings.EqualFold(payload.Accepted.Asset, frozenAsset) || payload.Accepted.Amount != frozenAmount || !strings.EqualFold(payload.Accepted.PayTo, requirement.PayTo) {
		return nil, errors.New("created payload changed frozen tuple")
	}
	evmPayload, err := evm.PayloadFromMap(payload.Payload)
	if err != nil || !strings.EqualFold(evmPayload.Authorization.From, signer.Address()) || !strings.EqualFold(evmPayload.Authorization.To, requirement.PayTo) || evmPayload.Authorization.Value != frozenAmount {
		return nil, errors.New("authorization fields do not match signer and tuple")
	}
	if err := verifyEOAAuthorization(evmPayload, signer.Address()); err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

func verifyEOAAuthorization(payload *evm.ExactEIP3009Payload, signerAddress string) error {
	signature, err := evm.HexToBytes(payload.Signature)
	if err != nil || len(signature) != 65 {
		return errors.New("invalid authorization signature")
	}
	sig := append([]byte(nil), signature...)
	if sig[64] == 27 || sig[64] == 28 {
		sig[64] -= 27
	}
	if sig[64] != 0 && sig[64] != 1 {
		return errors.New("invalid authorization recovery id")
	}
	digest, err := evm.HashEIP3009Authorization(payload.Authorization, big.NewInt(84532), frozenAsset, "USDC", "2")
	if err != nil {
		return errors.New("invalid authorization data")
	}
	publicKey, err := crypto.SigToPub(digest, sig)
	if err != nil || !strings.EqualFold(crypto.PubkeyToAddress(*publicKey).Hex(), signerAddress) {
		return errors.New("authorization signature does not match configured account")
	}
	return nil
}

func parseSettlement(header http.Header) (*x402.SettleResponse, error) {
	return x402http.Newx402HTTPClient(nil).GetPaymentSettleResponse(headersMap(header))
}

func checkOfficialFacilitatorSupport(ctx context.Context, client *http.Client) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, frozenFacilitatorURL+"/supported", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readBounded(resp.Body)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errors.New("facilitator support unavailable")
	}
	var supported x402.SupportedResponse
	if json.Unmarshal(data, &supported) != nil {
		return errors.New("invalid facilitator support response")
	}
	for _, kind := range supported.Kinds {
		if kind.X402Version == frozenVersion && kind.Scheme == frozenScheme && kind.Network == frozenNetwork {
			return nil
		}
	}
	return errors.New("frozen payment kind unsupported")
}

func collectRequestIDs(header http.Header) []string {
	seen := map[string]bool{}
	for _, name := range []string{"X-Facilitator-Request-ID", "X-Request-ID"} {
		if value := sanitizeIdentifier(header.Get(name)); value != "" {
			seen[value] = true
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func mergeRequestIDs(existing, additions []string) []string {
	seen := make(map[string]bool, len(existing)+len(additions))
	for _, value := range existing {
		seen[value] = true
	}
	for _, value := range additions {
		seen[value] = true
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sanitizeIdentifier(value string) string {
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && !strings.ContainsRune("._:-", char) {
			return ""
		}
	}
	return value
}

func currentCLICommit() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return commitFromBuildSettings(info.Settings)
	}
	return ""
}

func commitFromBuildSettings(settings []debug.BuildSetting) string {
	var revision string
	clean := false
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			clean = setting.Value == "false"
		}
	}
	if revision == "" || !clean {
		return ""
	}
	return revision
}
