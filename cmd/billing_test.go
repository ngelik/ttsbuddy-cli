package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
)

const cmdBillingID = "11111111-2222-4333-8444-555555555555"

func TestBillingOwnerErrorRetainsURLAndHumanHandoff(t *testing.T) {
	url := "https://www.ttsbuddy.com/agent/billing?registration=" + cmdBillingID
	got := classifyBillingCLIError(&api.BillingHTTPError{StatusCode: 403, Reason: "BILLING_AUTHORIZATION_REQUIRED", Action: &api.BillingAction{Type: "owner_billing_authorization", URL: url}}, "", false).(*exitError)
	payload := structuredErrorPayload(got)
	if got.retryable || !payload.Error.HumanActionRequired || got.action == nil || got.action.URL != url {
		t.Fatalf("payload=%#v", payload.Error)
	}
}
func TestBillingExecuteTransportAndProtocolRecovery(t *testing.T) {
	for _, err := range []error{&api.BillingTransportError{}, &api.BillingValidationError{Kind: "INVALID_BILLING_RESPONSE"}, &api.BillingHTTPError{StatusCode: 503, Reason: "BILLING_UNAVAILABLE"}} {
		got := classifyBillingCLIError(err, cmdBillingID, true).(*exitError)
		if got.retryable || got.action == nil || strings.Join(got.action.Argv, " ") == "" || strings.Contains(strings.Join(got.action.Argv, " "), "upgrade") {
			t.Fatalf("unsafe recovery: %#v", got)
		}
	}
	got := classifyBillingCLIError(&api.BillingTransportError{}, cmdBillingID, false).(*exitError)
	if got.reason == "EXECUTION_OUTCOME_UNKNOWN" || !got.retryable {
		t.Fatalf("read-only status misclassified: %#v", got)
	}
}
func TestBillingUpgradeTerminalAndHumanStates(t *testing.T) {
	oldCfg, oldJSON := resolvedCfg, flagJSON
	defer func() { resolvedCfg = oldCfg; flagJSON = oldJSON }()
	flagJSON = true
	for _, tc := range []struct {
		state, reason string
		human, retry  bool
	}{{"failed", "PAYMENT_FAILED", false, false}, {"expired", "QUOTE_EXPIRED", false, false}, {"requires_action", "PAYMENT_ACTION_REQUIRED", true, false}, {"syncing", "ENTITLEMENT_SYNC_PENDING", false, true}} {
		t.Run(tc.state, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v1/agent-billing/operations/"+cmdBillingID+"/execute" {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": cmdBillingID, "state": tc.state, "reason": tc.reason, "target_plan": "pro", "entitlement_ready": false})
			}))
			defer server.Close()
			resolvedCfg = &config.ResolvedConfig{APIURL: server.URL + "/v1/agent-tts", APIKey: "ttsa_test"}
			err := runBillingUpgrade(context.Background(), cmdBillingID)
			got, ok := err.(*exitError)
			if !ok {
				t.Fatalf("err %v", err)
			}
			payload, ok := got.jsonPayload.(api.CLIError)
			if !ok {
				t.Fatalf("payload %T", got.jsonPayload)
			}
			if calls != 1 || got.retryable != tc.retry || payload.Error.HumanActionRequired != tc.human {
				t.Fatalf("calls=%d payload=%#v", calls, payload.Error)
			}
		})
	}
}
func TestBillingTextPrintsAmountsAndUnknown(t *testing.T) {
	oldJSON := flagJSON
	flagJSON = false
	defer func() { flagJSON = oldJSON }()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = original }()
	tax, total := 60, 1260
	err = emitBilling(&api.BillingOperationView{OperationID: cmdBillingID, State: "quoted", TargetPlan: "pro", Quote: &api.BillingQuote{Currency: "usd", ImmediateSubtotalMinor: 1200, ImmediateTaxMinor: &tax, ImmediateTotalMinor: &total, RecurringBaseMinor: 1200, Interval: "month"}})
	_ = w.Close()
	os.Stdout = original
	data, _ := io.ReadAll(r)
	_ = r.Close()
	if err != nil || (!strings.Contains(string(data), "tax 60, total 1260") || !strings.Contains(string(data), "current entitlement ready: false")) || strings.Contains(string(data), "0x") {
		t.Fatalf("output %s err %v", data, err)
	}
	if billingMinorAmount(nil) != "unknown" {
		t.Fatal("nil amount misrepresented")
	}
}
func TestBillingURLActionHasNoConfigArgv(t *testing.T) {
	old := flagConfigDir
	flagConfigDir = "/tmp/billing-config"
	defer func() { flagConfigDir = old }()
	got := billingActionFromAPI(&api.BillingAction{Type: "owner_billing_authorization", URL: "https://www.ttsbuddy.com/agent/billing?registration=" + cmdBillingID})
	if got == nil || len(got.Argv) != 0 {
		t.Fatalf("action %#v", got)
	}
}

func TestBillingSuccessfulNestedActionKeepsConfigContext(t *testing.T) {
	oldDir, oldJSON := flagConfigDir, flagJSON
	flagConfigDir = "/tmp/billing-context"
	flagJSON = true
	defer func() { flagConfigDir = oldDir; flagJSON = oldJSON }()
	result := &api.BillingStatusResponse{Operations: []api.BillingOperationView{{OperationID: cmdBillingID, State: "syncing", TargetPlan: "pro", Action: &api.BillingAction{Type: "billing_status", Argv: []string{"ttsbuddy", "billing", "status", "--operation", cmdBillingID}}}}}
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = original }()
	err = emitBilling(result)
	_ = w.Close()
	os.Stdout = original
	data, _ := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	var decoded api.BillingStatusResponse
	if json.Unmarshal(data, &decoded) != nil {
		t.Fatalf("bad json %s", data)
	}
	argv := strings.Join(decoded.Operations[0].Action.Argv, " ")
	if !strings.Contains(argv, "--config-dir /tmp/billing-context") || !strings.Contains(argv, "--json") || strings.Contains(argv, "upgrade") {
		t.Fatalf("argv %s", argv)
	}
}

func TestBillingHistoricalSuccessUnavailableDoesNotPollOrResume(t *testing.T) {
	oldCfg, oldJSON := resolvedCfg, flagJSON
	defer func() { resolvedCfg = oldCfg; flagJSON = oldJSON }()
	flagJSON = true
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": cmdBillingID, "state": "succeeded", "target_plan": "pro", "entitlement_ready": false})
	}))
	defer server.Close()
	resolvedCfg = &config.ResolvedConfig{APIURL: server.URL + "/v1/agent-tts", APIKey: "ttsa_test"}
	err := runBillingUpgrade(context.Background(), cmdBillingID)
	got, ok := err.(*exitError)
	if !ok {
		t.Fatalf("historical success must not return success: %v", err)
	}
	if calls != 1 || got.code != 1 || got.retryable || got.reason != "CURRENT_ENTITLEMENT_UNAVAILABLE" || got.action == nil {
		t.Fatalf("unsafe historical success: %#v", got)
	}
	argv := strings.Join(got.action.Argv, " ")
	if !strings.Contains(argv, "ttsbuddy billing status") || strings.Contains(argv, "--operation") || strings.Contains(argv, "speak") || strings.Contains(argv, "upgrade") {
		t.Fatalf("unsafe recovery: %s", argv)
	}
	payload, ok := got.jsonPayload.(api.CLIError)
	if !ok || payload.Error.Reason != "CURRENT_ENTITLEMENT_UNAVAILABLE" || payload.Error.Retryable {
		t.Fatalf("payload %#v", got.jsonPayload)
	}
}

func TestBillingTextReturnsOutputWriteError(t *testing.T) {
	oldJSON, oldStdout := flagJSON, os.Stdout
	defer func() { flagJSON = oldJSON; os.Stdout = oldStdout }()
	flagJSON = false
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	_ = w.Close()
	os.Stdout = w
	if err = emitBilling(&api.BillingOperationView{OperationID: cmdBillingID, State: "succeeded", TargetPlan: "pro", EntitlementReady: true}); err == nil {
		t.Fatal("output write failure was discarded")
	}
}

func TestBillingManualReviewRequiresHumanWithoutRepurchase(t *testing.T) {
	oldCfg, oldJSON := resolvedCfg, flagJSON
	defer func() { resolvedCfg = oldCfg; flagJSON = oldJSON }()
	flagJSON = true
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": cmdBillingID, "state": "failed", "target_plan": "pro", "entitlement_ready": false, "reason": "BILLING_MANUAL_REVIEW_REQUIRED", "action": api.BillingManualReviewAction(cmdBillingID)})
	}))
	defer server.Close()
	resolvedCfg = &config.ResolvedConfig{APIURL: server.URL + "/v1/agent-tts", APIKey: "ttsa_test"}
	got, ok := runBillingUpgrade(context.Background(), cmdBillingID).(*exitError)
	if !ok {
		t.Fatal("manual review returned success")
	}
	payload, ok := got.jsonPayload.(api.CLIError)
	if !ok || calls != 1 || got.retryable || !payload.Error.HumanActionRequired || payload.Error.Details["operation_id"] != cmdBillingID || got.action == nil || got.action.Type != "contact_support" || len(got.action.Argv) != 0 {
		t.Fatalf("unsafe manual result %#v calls=%d", got, calls)
	}
	if got.action.URL != "mailto:support@ttsbuddy.com?subject=Billing%20operation%20"+cmdBillingID || strings.Contains(got.nextAction, "new quote") {
		t.Fatalf("bad handoff %#v", got)
	}
	httpErr := &api.BillingHTTPError{StatusCode: 409, Reason: "BILLING_MANUAL_REVIEW_REQUIRED", Action: api.BillingManualReviewAction(cmdBillingID)}
	mapped := classifyBillingCLIError(httpErr, cmdBillingID, true).(*exitError)
	if mapped.retryable || !structuredErrorPayload(mapped).Error.HumanActionRequired || mapped.action == nil || mapped.action.Type != "contact_support" {
		t.Fatalf("bad HTTP manual handoff %#v", mapped)
	}
}

func TestBillingAgentQuotaSuggestsReadOnlyStatusWithoutPurchaseOrRetry(t *testing.T) {
	for _, tc := range []struct{ name, credential, action string }{{"agent", "ttsa_" + strings.Repeat("a", 8) + "_" + strings.Repeat("b", 48), "billing_status"}, {"permanent", testSubscriptionCredential(), "account"}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls, unexpected atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v1/agent-tts" {
					unexpected.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]any{"code": "USAGE_LIMIT_EXCEEDED", "message": "quota exhausted"}})
			}))
			defer server.Close()
			result := runCLI(t, envForTest(t.TempDir(), server.URL+"/v1/agent-tts", tc.credential), "speak", "Preserve this original input.", "--json", "--no-download", "--idempotency-key", "quota-original")
			assertExitCode(t, result, 1)
			var payload api.CLIError
			if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
				t.Fatalf("json %s err=%v", result.Stdout, err)
			}
			if calls.Load() != 1 || unexpected.Load() != 0 {
				t.Fatalf("quota triggered extra/billing request: calls=%d unexpected=%d", calls.Load(), unexpected.Load())
			}
			if payload.Error.Reason != "QUOTA_EXCEEDED" || payload.Error.Retryable || payload.Error.Action == nil || payload.Error.Action.Type != tc.action {
				t.Fatalf("recovery %#v", payload.Error)
			}
			argv := strings.Join(payload.Error.Action.Argv, " ")
			if strings.Contains(argv, "upgrade") || strings.Contains(argv, "quote") || strings.Contains(argv, "speak") {
				t.Fatalf("mutating recovery %s", argv)
			}
			if tc.name == "agent" && !strings.Contains(argv, "ttsbuddy billing status") {
				t.Fatalf("missing status recovery %s", argv)
			}
			if tc.name == "permanent" && payload.Error.Action.URL != "https://ttsbuddy.com/billing" {
				t.Fatalf("lost browser fallback %#v", payload.Error.Action)
			}
		})
	}
}
