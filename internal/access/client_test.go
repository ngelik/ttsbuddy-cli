package access

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/wallet"
	x402 "github.com/x402-foundation/x402/go/v2"
	"github.com/x402-foundation/x402/go/v2/mechanisms/evm"
)

const (
	testAgentURL = "https://www.ttsbuddy.com/v1/agent-tts"
	testAsset    = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	testPayTo    = "0x52908400098527886E0F7030069857D2E4169EE7"
	testPayer    = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
)

func TestEndpointsFromAgentURLBuildsRoutesFromTrustedOrigin(t *testing.T) {
	endpoints, err := EndpointsFromAgentURL(testAgentURL)
	if err != nil {
		t.Fatalf("EndpointsFromAgentURL() error = %v", err)
	}
	if endpoints.AgentTTS.String() != testAgentURL {
		t.Fatalf("agent URL = %q", endpoints.AgentTTS.String())
	}
	if endpoints.Plans.String() != "https://www.ttsbuddy.com/v1/access-pass-plans" {
		t.Fatalf("plans URL = %q", endpoints.Plans.String())
	}
	if endpoints.Purchases.String() != "https://www.ttsbuddy.com/v1/access-passes" {
		t.Fatalf("purchases URL = %q", endpoints.Purchases.String())
	}
	if endpoints.Current.String() != "https://www.ttsbuddy.com/v1/access-passes/current" {
		t.Fatalf("current URL = %q", endpoints.Current.String())
	}
}

func TestEndpointsFromAgentURLSupportsLocalSupabaseFunctionOrigin(t *testing.T) {
	endpoints, err := EndpointsFromAgentURL("http://127.0.0.1:54321/functions/v1/agent-tts")
	if err != nil {
		t.Fatalf("local endpoints error = %v", err)
	}
	if got, want := endpoints.Plans.String(), "http://127.0.0.1:54321/functions/v1/access-pass-plans"; got != want {
		t.Fatalf("plans URL = %q, want %q", got, want)
	}
}

func TestEndpointsFromAgentURLRejectsInsecureNonLoopbackAndCustomWithoutOptIn(t *testing.T) {
	cases := []string{
		"http://api.example.com/v1/agent-tts",
		"https://api.example.com/v1/agent-tts",
		"https://user:pass@www.ttsbuddy.com/v1/agent-tts",
	}
	for _, raw := range cases {
		if _, err := EndpointsFromAgentURL(raw); err == nil {
			t.Fatalf("EndpointsFromAgentURL(%q) succeeded", raw)
		}
	}

	if _, err := EndpointsFromAgentURLWithOptions("https://api.example.com/v1/agent-tts", EndpointOptions{AllowCustomAPIURL: true}); err != nil {
		t.Fatalf("custom opt-in should allow HTTPS custom host: %v", err)
	}
}

func TestPlansIsPublicAndDoesNotSendAuthorization(t *testing.T) {
	var gotAuth string
	client, cleanup := newAccessClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/access-pass-plans" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"plans": []any{starterPlanBody()}})
	})
	defer cleanup()

	plans, err := client.Plans(context.Background())
	if err != nil {
		t.Fatalf("Plans() error = %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Plans sent Authorization: %q", gotAuth)
	}
	if len(plans.Plans) != 1 || plans.Plans[0].Price.Atomic != "5000000" || plans.Plans[0].AllowanceUnits != 500_000 {
		t.Fatalf("unexpected plans response: %#v", plans)
	}
}

func TestStatusUsesOnlyPassBearerAndParsesReceipt(t *testing.T) {
	pass := fixtureCredential("ttsp", 'a', 'b')
	var gotAuth string
	client, cleanup := newAccessClientWithBearer(t, pass, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/access-passes/current" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, statusBody(pass, "0xabc123"))
	})
	defer cleanup()

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if gotAuth != "Bearer "+pass {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if status.Receipt.Transaction != "0xabc123" || status.RemainingUnits != 499_900 {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestStatusRejectsSubscriptionBearerBeforeNetwork(t *testing.T) {
	client, cleanup := newAccessClientWithBearer(t, fixtureCredential("ttsb", 'a', 'b'), func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("network should not be called for subscription bearer")
	})
	defer cleanup()

	_, err := client.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "access pass") {
		t.Fatalf("expected pass-only error, got %v", err)
	}
}

func TestBuyMakesFirst402ThenOnePaidRetryWithSameBodyAndIdempotency(t *testing.T) {
	signer := &recordingSigner{}
	var attempts []capturedPurchaseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveStarterPlans(t, w, r) {
			return
		}
		if r.URL.Path != "/v1/access-passes" {
			http.NotFound(w, r)
			return
		}
		capture := capturePurchase(t, r)
		attempts = append(attempts, capture)
		if len(attempts) == 1 {
			writePaymentRequired(t, w, challengeForServer(serverURL(r), "5000000", "eip155:84532", testAsset, testPayTo))
			return
		}
		if capture.PaymentSignature == "" {
			t.Fatal("paid retry omitted PAYMENT-SIGNATURE")
		}
		w.Header().Set("PAYMENT-RESPONSE", encodeSettlement(t, x402.SettleResponse{Success: true, Transaction: "0xabc123", Network: x402.Network("eip155:84532"), Amount: "5000000", Payer: testPayer}))
		writeJSON(t, w, http.StatusOK, successPurchaseBody("0xabc123"))
	}))
	defer server.Close()

	client := newServerAccessClient(t, server.URL, fixtureCredential("ttsb", 'e', 'f'))
	result, err := client.Buy(context.Background(), BuyRequest{SKU: "starter", MaxPrice: "5.00", Signer: signer})
	if err != nil {
		t.Fatalf("Buy() error = %v", err)
	}
	if result.Pass == "" || result.Receipt.Transaction != "0xabc123" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if signer.Calls() != 1 {
		t.Fatalf("signer calls = %d, want 1", signer.Calls())
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if attempts[0].Body != `{"sku":"starter"}` || attempts[1].Body != attempts[0].Body {
		t.Fatalf("body changed across attempts: %#v", attempts)
	}
	if attempts[0].Authorization != "" || attempts[1].Authorization != "" {
		t.Fatalf("Buy sent stored bearer Authorization: %#v", attempts)
	}
	if attempts[0].IdempotencyKey == "" || attempts[1].IdempotencyKey != attempts[0].IdempotencyKey {
		t.Fatalf("idempotency changed across attempts: %#v", attempts)
	}
	if attempts[1].PaymentSignature == "" {
		t.Fatal("missing paid signature")
	}
}

func TestBuyReusesExactPaidRequestOnceAfterAmbiguousDrop(t *testing.T) {
	signer := &recordingSigner{}
	var paidSignatures []string
	var paidBodies []string
	var paidIdempotency []string
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveStarterPlans(t, w, r) {
			return
		}
		hit := atomic.AddInt32(&hits, 1)
		captured := capturePurchase(t, r)
		if hit == 1 {
			writePaymentRequired(t, w, challengeForServer(serverURL(r), "5000000", "eip155:84532", testAsset, testPayTo))
			return
		}
		paidSignatures = append(paidSignatures, captured.PaymentSignature)
		paidBodies = append(paidBodies, captured.Body)
		paidIdempotency = append(paidIdempotency, captured.IdempotencyKey)
		if hit == 2 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("PAYMENT-RESPONSE", encodeSettlement(t, x402.SettleResponse{Success: true, Transaction: "0xdef456", Network: x402.Network("eip155:84532"), Amount: "5000000", Payer: testPayer}))
		writeJSON(t, w, http.StatusOK, successPurchaseBody("0xdef456"))
	}))
	defer server.Close()

	client := newServerAccessClient(t, server.URL, "")
	_, err := client.Buy(context.Background(), BuyRequest{SKU: "starter", MaxPrice: "5.00", Signer: signer})
	if err != nil {
		t.Fatalf("Buy() error = %v", err)
	}
	if signer.Calls() != 1 {
		t.Fatalf("signer calls = %d, want one authorization", signer.Calls())
	}
	if len(paidSignatures) != 2 || paidSignatures[0] == "" || paidSignatures[1] != paidSignatures[0] {
		t.Fatalf("paid signature not reused exactly: %#v", paidSignatures)
	}
	if paidBodies[1] != paidBodies[0] || paidIdempotency[1] != paidIdempotency[0] {
		t.Fatalf("paid body/idempotency changed: bodies=%#v idempotency=%#v", paidBodies, paidIdempotency)
	}
}

func TestBuyDoesNotRetryWithFreshSignatureAfterPaidHTTP402(t *testing.T) {
	signer := &recordingSigner{}
	var paidHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveStarterPlans(t, w, r) {
			return
		}
		captured := capturePurchase(t, r)
		if captured.PaymentSignature == "" {
			writePaymentRequired(t, w, challengeForServer(serverURL(r), "5000000", "eip155:84532", testAsset, testPayTo))
			return
		}
		paidHits++
		writePaymentRequired(t, w, challengeForServer(serverURL(r), "5000000", "eip155:84532", testAsset, testPayTo))
	}))
	defer server.Close()

	client := newServerAccessClient(t, server.URL, "")
	_, err := client.Buy(context.Background(), BuyRequest{SKU: "starter", MaxPrice: "5.00", Signer: signer})
	if err == nil {
		t.Fatal("expected paid 402 to fail")
	}
	if signer.Calls() != 1 || paidHits != 1 {
		t.Fatalf("unexpected retry/sign behavior: signer=%d paidHits=%d", signer.Calls(), paidHits)
	}
}

func TestBuyRejectsTupleMismatchesBeforeSigning(t *testing.T) {
	cases := []struct {
		name   string
		change func(x402.PaymentRequired) x402.PaymentRequired
	}{
		{"version", func(r x402.PaymentRequired) x402.PaymentRequired { r.X402Version = 1; return r }},
		{"scheme", func(r x402.PaymentRequired) x402.PaymentRequired { r.Accepts[0].Scheme = "upto"; return r }},
		{"network", func(r x402.PaymentRequired) x402.PaymentRequired { r.Accepts[0].Network = "eip155:8453"; return r }},
		{"asset", func(r x402.PaymentRequired) x402.PaymentRequired { r.Accepts[0].Asset = testPayTo; return r }},
		{"payee", func(r x402.PaymentRequired) x402.PaymentRequired { r.Accepts[0].PayTo = testAsset; return r }},
		{"amount", func(r x402.PaymentRequired) x402.PaymentRequired { r.Accepts[0].Amount = "5000001"; return r }},
		{"payment flow", func(r x402.PaymentRequired) x402.PaymentRequired {
			r.Accepts[0].Extra["paymentFlow"] = "authorization"
			return r
		}},
		{"missing resource", func(r x402.PaymentRequired) x402.PaymentRequired { r.Resource = nil; return r }},
		{"resource mismatch", func(r x402.PaymentRequired) x402.PaymentRequired {
			r.Resource.URL = "http://127.0.0.1:54321/v1/access-passes"
			return r
		}},
		{"multiple accepts", func(r x402.PaymentRequired) x402.PaymentRequired {
			r.Accepts = append(r.Accepts, r.Accepts[0])
			return r
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signer := &recordingSigner{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveStarterPlans(t, w, r) {
					return
				}
				required := challengeForServer(serverURL(r), "5000000", "eip155:84532", testAsset, testPayTo)
				writePaymentRequired(t, w, tc.change(required))
			}))
			defer server.Close()
			client := newServerAccessClient(t, server.URL, "")
			_, err := client.Buy(context.Background(), BuyRequest{SKU: "starter", MaxPrice: "5.00", Signer: signer})
			if err == nil {
				t.Fatal("expected tuple rejection")
			}
			if signer.Calls() != 0 {
				t.Fatalf("signer was called before tuple rejection")
			}
		})
	}
}

func TestBuyRejectsMaxPriceBoundaryAndMalformedMoneyBeforeSigning(t *testing.T) {
	cases := []struct {
		name     string
		maxPrice string
	}{
		{"below price", "4.99"},
		{"too much precision", "5.0000011"},
		{"scientific", "5e0"},
		{"negative", "-5.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signer := &recordingSigner{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveStarterPlans(t, w, r) {
					return
				}
				writePaymentRequired(t, w, challengeForServer(serverURL(r), "5000000", "eip155:84532", testAsset, testPayTo))
			}))
			defer server.Close()
			client := newServerAccessClient(t, server.URL, "")
			_, err := client.Buy(context.Background(), BuyRequest{SKU: "starter", MaxPrice: tc.maxPrice, Signer: signer})
			if err == nil {
				t.Fatal("expected max price/money rejection")
			}
			if signer.Calls() != 0 {
				t.Fatalf("signer was called for invalid max price")
			}
		})
	}
}

func TestBuyRejectsMissingSettlementHeaderOrInvalidSuccessShape(t *testing.T) {
	cases := []struct {
		name    string
		header  bool
		mutate  func(map[string]any)
		wantErr string
	}{
		{"missing settlement", false, nil, "payment response"},
		{"wrong network", true, nil, "settlement"},
		{"missing credential", true, func(body map[string]any) { delete(body, "pass") }, "pass"},
		{"missing receipt", true, func(body map[string]any) { delete(body, "receipt") }, "purchase_id"},
		{"wrong allowance", true, func(body map[string]any) { body["allowance_units"] = float64(499999) }, "allowance"},
		{"reserved nonzero", true, func(body map[string]any) { body["reserved_units"] = float64(1) }, "reserved"},
		{"expired pass", true, func(body map[string]any) { body["expires_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) }, "expires_at"},
		{"unknown success field", true, func(body map[string]any) { body["secret_debug"] = "must not be accepted" }, "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signer := &recordingSigner{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveStarterPlans(t, w, r) {
					return
				}
				captured := capturePurchase(t, r)
				if captured.PaymentSignature == "" {
					writePaymentRequired(t, w, challengeForServer(serverURL(r), "5000000", "eip155:84532", testAsset, testPayTo))
					return
				}
				if tc.header {
					network := x402.Network("eip155:84532")
					if tc.name == "wrong network" {
						network = x402.Network("eip155:8453")
					}
					w.Header().Set("PAYMENT-RESPONSE", encodeSettlement(t, x402.SettleResponse{Success: true, Transaction: "0xabc123", Network: network, Amount: "5000000", Payer: testPayer}))
				}
				body := successPurchaseBody("0xabc123")
				if tc.mutate != nil {
					tc.mutate(body)
				}
				writeJSON(t, w, http.StatusOK, body)
			}))
			defer server.Close()
			client := newServerAccessClient(t, server.URL, "")
			_, err := client.Buy(context.Background(), BuyRequest{SKU: "starter", MaxPrice: "5.00", Signer: signer})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestClientRejectsCrossOriginRedirectWithoutLeakingPaymentOrBearer(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		if r.Header.Get("Authorization") != "" || r.Header.Get("PAYMENT-SIGNATURE") != "" {
			t.Fatalf("redirect target received sensitive headers")
		}
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/access-passes", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client := newServerAccessClient(t, redirector.URL, "")
	_, err := client.Plans(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("expected cross-origin redirect error, got %v", err)
	}
	if targetHits != 0 {
		t.Fatalf("redirect target reached")
	}
}

func TestClientRejectsOversizedBodyAndTimeout(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		client, cleanup := newAccessClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
		})
		defer cleanup()
		_, err := client.Plans(context.Background())
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("expected oversize error, got %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
		}))
		defer srv.Close()
		client := newServerAccessClient(t, srv.URL, "")
		client.httpClient.Timeout = 10 * time.Millisecond
		_, err := client.Plans(context.Background())
		if err == nil {
			t.Fatal("expected timeout error")
		}
	})
}

func TestErrorRedactionDoesNotLeakCredentialOrPaymentSignature(t *testing.T) {
	secretPass := fixtureCredential("ttsp", 'a', 'b')
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, secretPass+" PAYMENT-SIGNATURE=secret-value", http.StatusInternalServerError)
	}))
	defer server.Close()
	client := newServerAccessClient(t, server.URL, secretPass)

	_, err := client.Status(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, secretPass) || strings.Contains(msg, "secret-value") || strings.Contains(msg, "PAYMENT-SIGNATURE") {
		t.Fatalf("sensitive value leaked through error: %v", err)
	}
}

type recordingSigner struct {
	calls int32
}

var _ wallet.Signer = (*recordingSigner)(nil)

func (s *recordingSigner) Address() string {
	return testPayer
}

func (s *recordingSigner) SignTypedData(context.Context, evm.TypedDataDomain, map[string][]evm.TypedDataField, string, map[string]interface{}) ([]byte, error) {
	atomic.AddInt32(&s.calls, 1)
	signature := make([]byte, 65)
	signature[64] = 27
	return signature, nil
}

func (s *recordingSigner) Calls() int {
	return int(atomic.LoadInt32(&s.calls))
}

type capturedPurchaseRequest struct {
	Body             string
	Authorization    string
	IdempotencyKey   string
	PaymentSignature string
}

func capturePurchase(t *testing.T, r *http.Request) capturedPurchaseRequest {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return capturedPurchaseRequest{
		Body:             string(body),
		Authorization:    r.Header.Get("Authorization"),
		IdempotencyKey:   r.Header.Get("Idempotency-Key"),
		PaymentSignature: r.Header.Get("PAYMENT-SIGNATURE"),
	}
}

func newAccessClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	return newAccessClientWithBearer(t, "", handler)
}

func newAccessClientWithBearer(t *testing.T, bearer string, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	client := newServerAccessClient(t, srv.URL, bearer)
	return client, srv.Close
}

func newServerAccessClient(t *testing.T, serverURL, bearer string) *Client {
	t.Helper()
	client, err := NewClient(ClientOptions{
		AgentURL:          serverURL + "/v1/agent-tts",
		Bearer:            bearer,
		Version:           "test",
		AllowCustomAPIURL: true,
		NewIdempotencyKey: func() string { return "fixed-idempotency-key" },
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func starterPlanBody() map[string]any {
	return map[string]any{
		"sku":     "starter",
		"version": float64(1),
		"price": map[string]any{
			"display":        "5.00",
			"atomic":         "5000000",
			"asset":          testAsset,
			"asset_decimals": float64(6),
			"network":        "eip155:84532",
			"pay_to":         testPayTo,
		},
		"allowance_units":     float64(500_000),
		"request_limit_units": float64(100_000),
		"valid_for_seconds":   float64(2_592_000),
		"voice_policy":        "all_approved_public_voices",
	}
}

func statusBody(pass, transaction string) map[string]any {
	body := successPurchaseBody(transaction)
	delete(body, "pass")
	body["status"] = "active"
	body["remaining_units"] = float64(499_900)
	body["consumed_units"] = float64(100)
	body["plan"] = starterPlanBody()
	body["receipt"] = map[string]any{
		"purchase_id": "purchase-1",
		"network":     "eip155:84532",
		"transaction": transaction,
		"asset":       testAsset,
		"amount":      "5000000",
		"payer":       testPayer,
	}
	_ = pass
	return body
}

func serveStarterPlans(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	if r.URL.Path != "/v1/access-pass-plans" {
		return false
	}
	if r.Header.Get("Authorization") != "" {
		t.Fatalf("plans request sent Authorization: %q", r.Header.Get("Authorization"))
	}
	writeJSON(t, w, http.StatusOK, map[string]any{"plans": []any{starterPlanBody()}})
	return true
}

func successPurchaseBody(transaction string) map[string]any {
	return map[string]any{
		"pass":                fixtureCredential("ttsp", 'c', 'd'),
		"status":              "active",
		"allowance_units":     float64(500_000),
		"reserved_units":      float64(0),
		"consumed_units":      float64(0),
		"remaining_units":     float64(500_000),
		"request_limit_units": float64(100_000),
		"expires_at":          time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"receipt": map[string]any{
			"purchase_id": "purchase-1",
			"network":     "eip155:84532",
			"transaction": transaction,
			"asset":       testAsset,
			"amount":      "5000000",
			"payer":       testPayer,
		},
	}
}

func challengeForServer(resourceURL, amount, network, asset, payTo string) x402.PaymentRequired {
	return x402.PaymentRequired{
		X402Version: 2,
		Resource:    &x402.ResourceInfo{URL: resourceURL},
		Accepts: []x402.PaymentRequirements{{
			Scheme:            "exact",
			Network:           network,
			Asset:             asset,
			Amount:            amount,
			PayTo:             payTo,
			MaxTimeoutSeconds: 15,
			Extra:             map[string]interface{}{"paymentFlow": "upfront"},
		}},
	}
}

func writePaymentRequired(t *testing.T, w http.ResponseWriter, required x402.PaymentRequired) {
	t.Helper()
	raw, err := json.Marshal(required)
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString(raw))
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = w.Write([]byte(`{"error":"payment required"}`))
}

func encodeSettlement(t *testing.T, settlement x402.SettleResponse) string {
	t.Helper()
	raw, err := json.Marshal(settlement)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatal(err)
	}
}

func serverURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + r.URL.Path
}

func fixtureCredential(prefix string, public, secret byte) string {
	return prefix + "_" + strings.Repeat(string(public), 8) + "_" + strings.Repeat(string(secret), 48)
}
