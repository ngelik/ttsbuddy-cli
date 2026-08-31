package access

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	x402 "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"
	exactclient "github.com/x402-foundation/x402/go/v2/mechanisms/evm/exact/client"
)

const (
	maxResponseBytes       = 64 << 10
	accessClientTimeout    = 15 * time.Second
	paymentSignatureHeader = "PAYMENT-SIGNATURE"
)

type ClientOptions struct {
	AgentURL          string
	Bearer            string
	Version           string
	AllowCustomAPIURL bool
	HTTPClient        *http.Client
	NewIdempotencyKey func() string
	Now               func() time.Time
}

type Client struct {
	httpClient        *http.Client
	endpoints         Endpoints
	bearer            string
	version           string
	newIdempotencyKey func() string
	now               func() time.Time
}

func EndpointsFromAgentURL(raw string) (Endpoints, error) {
	return EndpointsFromAgentURLWithOptions(raw, EndpointOptions{})
}

func EndpointsFromAgentURLWithOptions(raw string, opts EndpointOptions) (Endpoints, error) {
	agent, err := parseTrustedAgentURL(raw, opts.AllowCustomAPIURL)
	if err != nil {
		return Endpoints{}, err
	}
	basePath := strings.TrimSuffix(agent.EscapedPath(), "/agent-tts")
	if basePath == agent.EscapedPath() {
		return Endpoints{}, errors.New("agent API URL must end with /agent-tts")
	}
	if basePath == "" {
		basePath = "/v1"
	}
	mk := func(path string) *url.URL {
		u := *agent
		u.RawQuery = ""
		u.Fragment = ""
		u.Path = basePath + path
		u.RawPath = ""
		return &u
	}
	return Endpoints{
		AgentTTS:  cloneURL(agent),
		Plans:     mk("/access-pass-plans"),
		Purchases: mk("/access-passes"),
		Current:   mk("/access-passes/current"),
	}, nil
}

func NewClient(opts ClientOptions) (*Client, error) {
	endpoints, err := EndpointsFromAgentURLWithOptions(opts.AgentURL, EndpointOptions{AllowCustomAPIURL: opts.AllowCustomAPIURL})
	if err != nil {
		return nil, err
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = newBoundedHTTPClient(endpoints.AgentTTS)
	} else {
		copy := *httpClient
		if copy.Timeout == 0 || copy.Timeout > accessClientTimeout {
			copy.Timeout = accessClientTimeout
		}
		copy.CheckRedirect = sameOriginRedirectPolicy(endpoints.AgentTTS)
		httpClient = &copy
	}
	newID := opts.NewIdempotencyKey
	if newID == nil {
		newID = newRandomID
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	return &Client{
		httpClient:        httpClient,
		endpoints:         endpoints,
		bearer:            opts.Bearer,
		version:           version,
		newIdempotencyKey: newID,
		now:               now,
	}, nil
}

func (c *Client) Plans(ctx context.Context) (*PlansResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoints.Plans.String(), nil)
	if err != nil {
		return nil, err
	}
	c.addUserAgent(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, redactError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readBounded(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, statusError("plans", resp.StatusCode, data)
	}
	var plans PlansResponse
	if err := decodeStrict(data, &plans); err != nil {
		return nil, fmt.Errorf("parsing access plans: %w", err)
	}
	return &plans, nil
}

func (c *Client) Status(ctx context.Context) (*StatusResult, error) {
	if !isAccessPassCredential(c.bearer) {
		return nil, errors.New("access status requires a ttsp_ access pass credential")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoints.Current.String(), nil)
	if err != nil {
		return nil, err
	}
	c.addUserAgent(req)
	req.Header.Set("Authorization", "Bearer "+c.bearer)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, redactError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readBounded(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, statusError("status", resp.StatusCode, data)
	}
	var status StatusResult
	if err := decodeStrict(data, &status); err != nil {
		return nil, fmt.Errorf("parsing access status: %w", err)
	}
	return &status, nil
}

func (c *Client) Buy(ctx context.Context, buy BuyRequest) (*PurchaseResult, error) {
	signer := buy.Signer
	if signer == nil {
		return nil, errors.New("access buy requires a wallet signer")
	}
	sku := strings.TrimSpace(buy.SKU)
	if sku == "" {
		return nil, errors.New("access buy requires a sku")
	}
	plans, err := c.Plans(ctx)
	if err != nil {
		return nil, err
	}
	plan, ok := findPlan(plans.Plans, sku)
	if !ok {
		return nil, fmt.Errorf("access pass plan %q unavailable", sku)
	}
	maxAtomic, err := parseMaxPriceAtomic(buy.MaxPrice, plan.Price.AssetDecimals)
	if err != nil {
		return nil, err
	}
	if compareAtomic(plan.Price.Atomic, maxAtomic) > 0 {
		return nil, errors.New("access pass price exceeds max price")
	}

	body, err := json.Marshal(struct {
		SKU string `json:"sku"`
	}{SKU: sku})
	if err != nil {
		return nil, err
	}
	idempotencyKey := c.newIdempotencyKey()
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, errors.New("idempotency key unavailable")
	}

	initial := c.sendPurchase(ctx, body, idempotencyKey, "")
	if initial.err != nil {
		return nil, initial.err
	}
	defer func() { _ = initial.response.Body.Close() }()
	if initial.response.StatusCode != http.StatusPaymentRequired {
		return nil, statusError("purchase challenge", initial.response.StatusCode, initial.body)
	}
	required, err := x402http.Newx402HTTPClient(nil).GetPaymentRequiredResponse(headersMap(initial.response.Header), initial.body)
	if err != nil {
		return nil, fmt.Errorf("parsing payment challenge: %w", err)
	}
	if err := validatePaymentChallenge(required, plan, maxAtomic, c.endpoints.Purchases.String()); err != nil {
		return nil, err
	}

	core := x402.Newx402Client().SetSpendControls(x402.SpendControls{
		AllowedAssets: []x402.SpendControlAsset{{
			Network:             x402.Network(plan.Price.Network),
			Asset:               plan.Price.Asset,
			MaxAmountPerPayment: plan.Price.Atomic,
		}},
	}).Register(x402.Network(plan.Price.Network), exactclient.NewExactEvmScheme(signer, nil))
	http402 := x402http.Newx402HTTPClient(core)
	selected, err := core.SelectPaymentRequirements(required.Accepts)
	if err != nil {
		return nil, fmt.Errorf("selecting payment requirements: %w", err)
	}
	payload, err := core.CreatePaymentPayload(ctx, selected, required.Resource, required.Extensions)
	if err != nil {
		return nil, fmt.Errorf("creating payment payload: %w", err)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encoding payment payload: %w", err)
	}
	paymentHeaders, err := http402.EncodePaymentSignatureHeader(payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("encoding payment signature: %w", err)
	}
	paymentSignature := paymentHeaders[paymentSignatureHeader]
	if paymentSignature == "" {
		return nil, errors.New("payment signature header missing")
	}

	paid := c.sendPurchase(ctx, body, idempotencyKey, paymentSignature)
	if paid.err != nil {
		if !paid.ambiguous {
			return nil, paid.err
		}
		paid = c.sendPurchase(ctx, body, idempotencyKey, paymentSignature)
		if paid.err != nil {
			if !paid.ambiguous {
				return nil, paid.err
			}
			return nil, fmt.Errorf("paid purchase outcome ambiguous: %w", redactError(paid.err))
		}
	}
	defer func() { _ = paid.response.Body.Close() }()
	if paid.response.StatusCode < 200 || paid.response.StatusCode > 299 {
		return nil, statusError("paid purchase", paid.response.StatusCode, paid.body)
	}
	settlement, err := x402http.Newx402HTTPClient(nil).GetPaymentSettleResponse(headersMap(paid.response.Header))
	if err != nil {
		return nil, fmt.Errorf("payment response receipt missing: %w", err)
	}
	if !settlement.Success || string(settlement.Network) != plan.Price.Network || settlement.Amount != plan.Price.Atomic || settlement.Transaction == "" {
		return nil, fmt.Errorf("%w: settlement header mismatch", ErrInvalidAccessPassReceipt)
	}
	if strings.TrimSpace(settlement.Payer) == "" {
		return nil, fmt.Errorf("%w: payer missing", ErrInvalidAccessPassReceipt)
	}
	var result PurchaseResult
	if err := decodeStrict(paid.body, &purchaseResultForDecode{PurchaseResult: &result, plan: plan, network: string(settlement.Network), transaction: settlement.Transaction, amount: settlement.Amount, payer: settlement.Payer, now: c.now()}); err != nil {
		return nil, fmt.Errorf("parsing access pass purchase: %w", err)
	}
	return &result, nil
}

type purchaseResultForDecode struct {
	*PurchaseResult
	plan        Plan
	network     string
	transaction string
	amount      string
	payer       string
	now         time.Time
}

func (p *purchaseResultForDecode) validate() error {
	return p.PurchaseResult.validateSuccess(p.plan, p.network, p.transaction, p.amount, p.payer, p.now)
}

type purchaseSendResult struct {
	response  *http.Response
	body      []byte
	err       error
	ambiguous bool
}

func (c *Client) sendPurchase(ctx context.Context, body []byte, idempotencyKey, paymentSignature string) purchaseSendResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoints.Purchases.String(), bytes.NewReader(body))
	if err != nil {
		return purchaseSendResult{err: err}
	}
	c.addUserAgent(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	if paymentSignature != "" {
		req.Header.Set(paymentSignatureHeader, paymentSignature)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return purchaseSendResult{response: resp, err: redactError(err), ambiguous: resp == nil}
	}
	data, readErr := readBounded(resp.Body)
	if readErr != nil {
		_ = resp.Body.Close()
		return purchaseSendResult{response: resp, err: redactError(readErr)}
	}
	return purchaseSendResult{response: resp, body: data}
}

func (c *Client) addUserAgent(req *http.Request) {
	req.Header.Set("User-Agent", "ttsbuddy-cli/"+c.version)
}

func findPlan(plans []Plan, sku string) (Plan, bool) {
	for _, plan := range plans {
		if plan.SKU == sku {
			return plan, true
		}
	}
	return Plan{}, false
}

func validatePaymentChallenge(required x402.PaymentRequired, plan Plan, maxAtomic, purchaseURL string) error {
	if required.X402Version != 2 {
		return errors.New("x402 challenge version must be 2")
	}
	if required.Resource == nil || !sameCanonicalURL(required.Resource.URL, purchaseURL) {
		return errors.New("x402 resource does not match purchase endpoint")
	}
	if len(required.Accepts) != 1 {
		return errors.New("x402 challenge must advertise exactly one payment option")
	}
	req := required.Accepts[0]
	switch {
	case req.Scheme != "exact":
		return errors.New("x402 challenge scheme mismatch")
	case req.Network != plan.Price.Network:
		return errors.New("x402 challenge network mismatch")
	case !strings.EqualFold(req.Asset, plan.Price.Asset):
		return errors.New("x402 challenge asset mismatch")
	case !strings.EqualFold(req.PayTo, plan.Price.PayTo):
		return errors.New("x402 challenge payee mismatch")
	case req.Amount != plan.Price.Atomic:
		return errors.New("x402 challenge amount mismatch")
	case compareAtomic(req.Amount, maxAtomic) > 0:
		return errors.New("x402 challenge exceeds max price")
	}
	flow, ok := req.Extra["paymentFlow"].(string)
	if !ok || flow != "upfront" {
		return errors.New("x402 challenge payment flow mismatch")
	}
	method, ok := req.Extra["assetTransferMethod"].(string)
	if !ok || method != "eip3009" {
		return errors.New("x402 challenge asset transfer method mismatch")
	}
	return nil
}

func parseTrustedAgentURL(raw string, allowCustom bool) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid Agent API URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, errors.New("Agent API URL must not include credentials, query, or fragment")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return nil, errors.New("Agent API URL host is required")
	}
	switch parsed.Scheme {
	case "http":
		if !isLoopbackHost(host) {
			return nil, fmt.Errorf("refusing insecure Agent API URL host %q", host)
		}
	case "https":
		if !isLoopbackHost(host) && !isOfficialHost(host) && !allowCustom {
			return nil, fmt.Errorf("refusing custom Agent API URL host %q without opt-in", host)
		}
	default:
		return nil, fmt.Errorf("unsupported Agent API URL scheme %q", parsed.Scheme)
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
	}
	return parsed, nil
}

func newBoundedHTTPClient(origin *url.URL) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = accessClientTimeout
	transport.TLSHandshakeTimeout = accessClientTimeout
	transport.MaxResponseHeaderBytes = maxResponseBytes
	return &http.Client{
		Transport:     transport,
		Timeout:       accessClientTimeout,
		CheckRedirect: sameOriginRedirectPolicy(origin),
	}
}

func sameOriginRedirectPolicy(origin *url.URL) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many access API redirects")
		}
		if !sameOrigin(req.URL, origin) {
			return fmt.Errorf("refusing cross-origin access API redirect to %s", req.URL.Redacted())
		}
		return nil
	}
}

func sameOrigin(candidate, origin *url.URL) bool {
	return candidate != nil && origin != nil &&
		strings.EqualFold(candidate.Scheme, origin.Scheme) &&
		strings.EqualFold(candidate.Host, origin.Host)
}

func sameCanonicalURL(left, right string) bool {
	canonical := func(raw string) (string, bool) {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
			return "", false
		}
		host := strings.ToLower(u.Hostname())
		if u.Port() != "" {
			host = net.JoinHostPort(host, u.Port())
		}
		return strings.ToLower(u.Scheme) + "://" + host + u.EscapedPath(), true
	}
	l, okL := canonical(left)
	r, okR := canonical(right)
	return okL && okR && l == r
}

func readBounded(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("response too large (>%d bytes)", maxResponseBytes)
	}
	return data, nil
}

func statusError(operation string, status int, body []byte) error {
	var payload struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.Error != "" {
		return fmt.Errorf("%s failed with HTTP %d: %s", operation, status, sanitizeToken(payload.Error))
	}
	return fmt.Errorf("%s failed with HTTP %d", operation, status)
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

func parseMaxPriceAtomic(maxPrice string, decimals int) (string, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(maxPrice, "$"))
	if trimmed == "" || strings.ContainsAny(trimmed, "eE") || strings.HasPrefix(trimmed, "-") {
		return "", errors.New("max price must be a non-negative decimal string")
	}
	intPart, decPart, hasDot := strings.Cut(trimmed, ".")
	if intPart == "" {
		intPart = "0"
	}
	if intPart == "" || !allDigits(intPart) || hasDot && !allDigits(decPart) {
		return "", errors.New("max price must be a plain decimal string")
	}
	if len(decPart) > decimals {
		return "", errors.New("max price has more precision than the payment asset supports")
	}
	atomic := strings.TrimLeft(intPart+decPart+strings.Repeat("0", decimals-len(decPart)), "0")
	if atomic == "" {
		atomic = "0"
	}
	return atomic, nil
}

func compareAtomic(left, right string) int {
	l, okL := new(big.Int).SetString(left, 10)
	r, okR := new(big.Int).SetString(right, 10)
	if !okL || !okR {
		return 1
	}
	return l.Cmp(r)
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isOfficialHost(host string) bool {
	return host == "ttsbuddy.com" || host == "www.ttsbuddy.com"
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	v := *u
	return &v
}

func newRandomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func redactError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(sanitizeToken(err.Error()))
}

func sanitizeToken(value string) string {
	value = redactCredentialPrefix(value, "ttsp")
	value = redactCredentialPrefix(value, "ttsb")
	value = strings.ReplaceAll(value, paymentSignatureHeader, "payment header")
	return strings.ReplaceAll(value, "PAYMENT-SIGNATURE", "payment header")
}

func redactCredentialPrefix(value, prefix string) string {
	for {
		start := strings.Index(value, prefix+"_")
		if start < 0 {
			return value
		}
		end := start
		for end < len(value) && !strings.ContainsRune(" \t\r\n\"'<>", rune(value[end])) {
			end++
		}
		value = value[:start] + prefix + "_..._" + value[end:]
	}
}
