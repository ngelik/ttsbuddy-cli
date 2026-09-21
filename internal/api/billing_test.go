package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

const billingTestID = "11111111-2222-4333-8444-555555555555"
const billingTestBase = "https://www.ttsbuddy.com/v1/agent-tts"

func billingTestOperation() map[string]any {
	return map[string]any{"operation_id": billingTestID, "state": "quoted", "target_plan": "pro", "entitlement_ready": false, "quote": map[string]any{"expires_at": "2026-09-21T23:00:00Z", "currency": "usd", "interval": "month", "tax_behavior": "exclusive", "immediate_subtotal_minor": 1200, "immediate_tax_minor": 60, "immediate_total_minor": 1260, "recurring_base_minor": 1200, "amount_is_estimate": true}}
}
func TestBillingNestedResponseSanitization(t *testing.T) {
	op := billingTestOperation()
	op["reason"] = "provider_secret_ttsa_private"
	op["action"] = map[string]any{"type": "billing_status", "argv": []string{"ttsbuddy", "billing", "upgrade", "--quote", billingTestID, "--key", "secret"}}
	payload := map[string]any{"registration_id": billingTestID, "source": map[string]any{"tier": "free", "status": "active"}, "usage": map[string]any{"month": "2026-09"}, "authorization": map[string]any{"state": "awaiting_setup", "owner_action_url": "https://www.ttsbuddy.com/agent/billing?registration=" + billingTestID + "&secret=ttsa_private"}, "operations": []any{op}, "secret": "not-allowed"}
	data, _ := json.Marshal(payload)
	var result BillingStatusResponse
	if err := decodeBilling(data, &result, billingTestBase); err != nil {
		t.Fatal(err)
	}
	rendered, _ := json.Marshal(result)
	if strings.Contains(string(rendered), "secret") || strings.Contains(string(rendered), "ttsa_") || strings.Contains(string(rendered), "upgrade") {
		t.Fatalf("unsafe output: %s", rendered)
	}
	if result.Operations[0].Action != nil || result.Operations[0].Reason != "BILLING_UNAVAILABLE" || result.Authorization.OwnerActionURL != "" {
		t.Fatalf("unsafe nested result: %#v", result)
	}
}
func TestBillingActionsExactCommandsAndOwnerURLs(t *testing.T) {
	owner := "https://www.ttsbuddy.com/agent/billing?registration=" + billingTestID
	good := &BillingAction{Type: "billing_status", Argv: []string{"ttsbuddy", "billing", "status", "--operation", billingTestID}}
	if got := SanitizeBillingAction(good, billingTestBase); got == nil || len(got.Argv) != 5 {
		t.Fatal("status rejected")
	}
	if got := SanitizeBillingAction(&BillingAction{Type: "owner_billing_authorization", URL: owner}, billingTestBase); got == nil || got.URL != owner {
		t.Fatal("owner rejected")
	}
	for _, raw := range []string{"https://www.ttsbuddy.com/agent/billing.evil?registration=" + billingTestID, "http://localhost:80@evil.example/agent/billing?registration=" + billingTestID, "https://www.ttsbuddy.com/agent/billing?registration=" + billingTestID + "&token=secret", "https://www.ttsbuddy.com/agent/billing?registration=secret", "https://evil.example/agent/billing?registration=" + billingTestID} {
		if SanitizeBillingAction(&BillingAction{Type: "payment_action_required", URL: raw}, billingTestBase) != nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, argv := range [][]string{{"ttsbuddy", "billing", "upgrade", "--quote", billingTestID}, {"ttsbuddy", "billing", "status", "--operation", billingTestID, "--key", "secret"}, {"ttsbuddy", "billing", "status", "--operation", "../../../secret"}} {
		if SanitizeBillingAction(&BillingAction{Type: "billing_status", Argv: argv}, billingTestBase) != nil {
			t.Fatalf("accepted argv %#v", argv)
		}
	}
	local := "http://localhost:54321/agent/billing?registration=" + billingTestID
	if SanitizeBillingAction(&BillingAction{Type: "owner_billing_authorization", URL: local}, "http://localhost:54321/functions/v1/agent-tts") == nil {
		t.Fatal("same-origin local URL rejected")
	}
}
func TestBillingMalformedProtocolIsSafe(t *testing.T) {
	for _, data := range []string{"null", "{}", `{"operation_id":"secret","state":"succeeded","entitlement_ready":true}`, `{"reason":"provider_secret"`} {
		var out BillingOperationView
		err := decodeBilling([]byte(data), &out, billingTestBase)
		var invalid *BillingValidationError
		if !errors.As(err, &invalid) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("wrong error: %v", err)
		}
	}
	op := billingTestOperation()
	op["state"] = "provider_secret"
	data, _ := json.Marshal(op)
	var out BillingOperationView
	if decodeBilling(data, &out, billingTestBase) == nil {
		t.Fatal("unknown state accepted")
	}
}

type billingRoundTrip func(*http.Request) (*http.Response, error)

func (f billingRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBillingTransportNeverRetriesOrLeaks(t *testing.T) {
	client := NewClient(billingTestBase, "ttsa_private", "test")
	calls := 0
	client.httpClient = &http.Client{Transport: billingRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("provider secret ttsa_private")
	})}
	_, _, err := client.BillingUpgrade(context.Background(), billingTestID)
	var transport *BillingTransportError
	if !errors.As(err, &transport) || calls != 1 || strings.Contains(err.Error(), "private") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
func TestBillingHTTPErrorRetainsOnlySafeActionAndReason(t *testing.T) {
	client := NewClient(billingTestBase, "ttsa_private", "test")
	body := `{"reason":"provider_secret","secret":"private","action":{"type":"owner_billing_authorization","url":"https://www.ttsbuddy.com/agent/billing?registration=` + billingTestID + `"}}`
	client.httpClient = &http.Client{Transport: billingRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	_, _, err := client.BillingQuote(context.Background(), "pro")
	var apiErr *BillingHTTPError
	if !errors.As(err, &apiErr) || apiErr.Reason != "BILLING_UNAVAILABLE" || apiErr.Action == nil {
		t.Fatalf("err=%#v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", apiErr), "private") || strings.Contains(err.Error(), "secret") {
		t.Fatal("error leaks provider text")
	}
}
func TestBillingURLAndOperationInput(t *testing.T) {
	for _, base := range []string{"https://www.ttsbuddy.com/v1/agent-tts", "http://localhost:54321/functions/v1/agent-tts"} {
		c := NewClient(base, "key", "test")
		got, err := c.billingBaseURL()
		want := strings.Replace(base, "agent-tts", "agent-billing", 1)
		if err != nil || got != want {
			t.Fatalf("got %s err %v", got, err)
		}
	}
	c := NewClient("https://www.ttsbuddy.com/other", "key", "test")
	_, _, err := c.BillingPlans(context.Background())
	var invalid *BillingValidationError
	if !errors.As(err, &invalid) || invalid.Kind != "INVALID_CONFIGURATION" {
		t.Fatalf("err=%v", err)
	}
	c = NewClient(billingTestBase, "key", "test")
	_, _, err = c.BillingUpgrade(context.Background(), strings.Repeat("-", 36))
	if !errors.As(err, &invalid) || invalid.Kind != "INVALID_QUOTE" {
		t.Fatalf("err=%v", err)
	}
}

func TestBillingWireContractAndOperationBinding(t *testing.T) {
	c := NewClient(billingTestBase, "ttsa_test", "test-version")
	calls := 0
	c.httpClient = &http.Client{Transport: billingRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/agent-billing/quotes" || r.Header.Get("Authorization") != "Bearer ttsa_test" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["plan"] != "pro" || len(body) != 1 {
			t.Errorf("body %#v err %v", body, err)
		}
		data, _ := json.Marshal(billingTestOperation())
		return &http.Response{StatusCode: 201, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})}
	got, _, err := c.BillingQuote(context.Background(), "pro")
	if err != nil || got.OperationID != billingTestID || calls != 1 {
		t.Fatalf("result=%#v calls=%d err=%v", got, calls, err)
	}
	c.httpClient = &http.Client{Transport: billingRoundTrip(func(r *http.Request) (*http.Response, error) {
		op := billingTestOperation()
		op["operation_id"] = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
		data, _ := json.Marshal(op)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})}
	_, _, err = c.BillingUpgrade(context.Background(), billingTestID)
	var invalid *BillingValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("mismatched operation accepted: %v", err)
	}
	_, _, _, err = c.BillingStatus(context.Background(), billingTestID)
	if !errors.As(err, &invalid) {
		t.Fatalf("mismatched status accepted: %v", err)
	}
}

func TestBillingHostedInvoiceHandoffIsPaymentOnly(t *testing.T) {
	for _, raw := range []string{"https://invoice.stripe.com/i/acct_example/test_invoice", "https://invoice.stripe.com/i/acct_example/test_invoice?s=ap"} {
		if SanitizeBillingAction(&BillingAction{Type: "payment_action_required", URL: raw}, billingTestBase) == nil {
			t.Fatalf("hosted invoice rejected: %s", raw)
		}
		if SanitizeBillingAction(&BillingAction{Type: "owner_billing_authorization", URL: raw}, billingTestBase) != nil {
			t.Fatal("invoice accepted for owner authorization")
		}
	}
	for _, raw := range []string{"http://invoice.stripe.com/i/invoice", "https://invoice.stripe.com.evil.example/i/invoice", "https://invoice.stripe.com@evil.example/i/invoice", "https://secret@invoice.stripe.com/i/invoice", "https://invoice.stripe.com:444/i/invoice", "https://invoice.stripe.com/i/invoice#secret", "https://invoice.stripe.com/i/invoice?api_key=secret", "https://invoice.stripe.com/elsewhere"} {
		if SanitizeBillingAction(&BillingAction{Type: "payment_action_required", URL: raw}, billingTestBase) != nil {
			t.Fatalf("unsafe invoice accepted: %s", raw)
		}
	}
}

func TestBillingHistoricalSuccessDoesNotProveCurrentEntitlement(t *testing.T) {
	for _, tc := range []struct {
		state        string
		ready, valid bool
	}{{"succeeded", false, true}, {"succeeded", true, true}, {"syncing", true, false}, {"failed", true, false}} {
		t.Run(fmt.Sprintf("%s-ready-%t", tc.state, tc.ready), func(t *testing.T) {
			op := billingTestOperation()
			op["state"] = tc.state
			op["entitlement_ready"] = tc.ready
			data, _ := json.Marshal(op)
			var result BillingOperationView
			err := decodeBilling(data, &result, billingTestBase)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
			if tc.state == "succeeded" && !tc.ready && (result.EntitlementReady || result.Reason != "CURRENT_ENTITLEMENT_UNAVAILABLE") {
				t.Fatalf("historical success treated as ready: %#v", result)
			}
		})
	}
}

func TestBillingManualReviewSupportLinkIsBounded(t *testing.T) {
	canonical := "mailto:support@ttsbuddy.com?subject=Billing%20operation%20" + billingTestID
	for _, raw := range []string{canonical, "mailto:support@ttsbuddy.com?subject=Billing+operation+" + billingTestID} {
		action := &BillingAction{Type: "contact_support", URL: raw}
		got := SanitizeBillingAction(action, billingTestBase, "BILLING_MANUAL_REVIEW_REQUIRED")
		if got == nil || got.URL != canonical || len(got.Argv) != 0 {
			t.Fatalf("support action %#v", got)
		}
		if SanitizeBillingAction(action, billingTestBase) != nil || SanitizeBillingAction(action, billingTestBase, "PAYMENT_FAILED") != nil {
			t.Fatal("support link allowed outside manual review")
		}
	}
	for _, raw := range []string{
		"mailto:other@ttsbuddy.com?subject=Billing%20operation%20" + billingTestID,
		"mailto:support@ttsbuddy.com,other@example.com?subject=Billing%20operation%20" + billingTestID,
		canonical + "&body=secret", canonical + "&cc=other@example.com", canonical + "&bcc=other@example.com", canonical + "&subject=duplicate", canonical + "#fragment",
		"mailto:support@ttsbuddy.com?subject=Billing%20operation%20invalid",
		"mailto:support@ttsbuddy.com?subject=Billing%20operation%20" + billingTestID + "%0Abcc:other@example.com",
		"https://support@ttsbuddy.com?subject=Billing%20operation%20" + billingTestID,
	} {
		if SanitizeBillingAction(&BillingAction{Type: "contact_support", URL: raw}, billingTestBase, "BILLING_MANUAL_REVIEW_REQUIRED") != nil {
			t.Fatalf("unsafe support URL accepted %q", raw)
		}
	}
	if SanitizeBillingAction(&BillingAction{Type: "contact_support", URL: canonical, Argv: []string{"mail", "support@ttsbuddy.com"}}, billingTestBase, "BILLING_MANUAL_REVIEW_REQUIRED") != nil {
		t.Fatal("automatic email command accepted")
	}
}

func TestBillingManualReviewOperationPreservesReasonAndBindsSupportID(t *testing.T) {
	for _, state := range []string{"unknown", "failed", "succeeded"} {
		op := billingTestOperation()
		op["state"] = state
		op["reason"] = "BILLING_MANUAL_REVIEW_REQUIRED"
		op["action"] = BillingManualReviewAction(billingTestID)
		data, _ := json.Marshal(op)
		var result BillingOperationView
		if err := decodeBilling(data, &result, billingTestBase); err != nil || result.Reason != "BILLING_MANUAL_REVIEW_REQUIRED" || result.Action == nil {
			t.Fatalf("manual result=%#v err=%v", result, err)
		}
	}
	op := billingTestOperation()
	op["reason"] = "BILLING_MANUAL_REVIEW_REQUIRED"
	op["action"] = BillingManualReviewAction("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	data, _ := json.Marshal(op)
	var result BillingOperationView
	if err := decodeBilling(data, &result, billingTestBase); err != nil || result.Action != nil {
		t.Fatalf("cross-operation support action %#v err=%v", result, err)
	}
	op["state"] = "succeeded"
	op["entitlement_ready"] = true
	data, _ = json.Marshal(op)
	if decodeBilling(data, &result, billingTestBase) == nil {
		t.Fatal("manual review permitted ready entitlement")
	}
}
