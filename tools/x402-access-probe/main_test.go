package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"github.com/ngelik/ttsbuddy-cli/internal/wallet"
	x402 "github.com/x402-foundation/x402/go/v2"
	"github.com/x402-foundation/x402/go/v2/mechanisms/evm"
	evmsigners "github.com/x402-foundation/x402/go/v2/signers/evm"
)

const (
	testPayee           = "0x1111111111111111111111111111111111111111"
	testKeyA            = "0x59c6995e998f97a5a0044976f0945389dc9e86dae88c7a841dd9ec886c1b6b90"
	testKeyB            = "0x8b3a350cf5c34c9194ca3a545d7c8bd42a4a56c7c2fdcdd188c7c2ab436d5b22"
	testTransactionHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestReadinessRecordAlwaysReportsNoSubmissionOrWalletMutation(t *testing.T) {
	t.Setenv("TTSBUDDY_EVM_PRIVATE_KEY", "")
	result := readinessRecord()
	if result["live_payment_submitted"] != false || result["wallet_created_or_funded"] != false {
		t.Fatalf("readiness mode claimed a side effect: %#v", result)
	}
	if result["x402_version"] != frozenVersion || result["network"] != frozenNetwork {
		t.Fatalf("readiness mode drifted from frozen tuple: %#v", result)
	}
}

func TestCommitFromBuildSettingsRequiresCleanRevision(t *testing.T) {
	tests := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{name: "clean revision", settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "false"}, {Key: "vcs.revision", Value: "abc123"}}, want: "abc123"},
		{name: "modified", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.modified", Value: "true"}}, want: ""},
		{name: "missing revision", settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "false"}}, want: ""},
		{name: "missing modified proof", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commitFromBuildSettings(tt.settings); got != tt.want {
				t.Fatalf("commitFromBuildSettings() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadSubmitConfigRequiresFrozenTupleAndLocalResource(t *testing.T) {
	valid := map[string]string{
		"X402_RESOURCE_URL":       "http://127.0.0.1:54321/functions/v1/x402-compatibility-probe",
		"X402_SIGNER_BACKEND":     "local",
		"X402_ASSET_ADDRESS":      frozenAsset,
		"X402_PAYEE_ADDRESS":      testPayee,
		"X402_PAYMENT_AMOUNT":     frozenAmount,
		"X402_MAX_PAYMENT_AMOUNT": frozenAmount,
	}

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing resource", key: "X402_RESOURCE_URL", value: ""},
		{name: "remote resource", key: "X402_RESOURCE_URL", value: "https://example.com/probe"},
		{name: "wrong path", key: "X402_RESOURCE_URL", value: "http://127.0.0.1:54321/functions/v1/other"},
		{name: "wrong port", key: "X402_RESOURCE_URL", value: "http://127.0.0.1:54320/functions/v1/x402-compatibility-probe"},
		{name: "query", key: "X402_RESOURCE_URL", value: "http://127.0.0.1:54321/functions/v1/x402-compatibility-probe?pay=1"},
		{name: "fragment", key: "X402_RESOURCE_URL", value: "http://127.0.0.1:54321/functions/v1/x402-compatibility-probe#pay"},
		{name: "credentials", key: "X402_RESOURCE_URL", value: "http://user:pass@127.0.0.1:54321/functions/v1/x402-compatibility-probe"},
		{name: "wrong signer", key: "X402_SIGNER_BACKEND", value: "browser"},
		{name: "wrong asset", key: "X402_ASSET_ADDRESS", value: testPayee},
		{name: "invalid payee", key: "X402_PAYEE_ADDRESS", value: "not-an-address"},
		{name: "wrong amount", key: "X402_PAYMENT_AMOUNT", value: "1001"},
		{name: "wrong maximum", key: "X402_MAX_PAYMENT_AMOUNT", value: "999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := cloneMap(valid)
			env[tt.key] = tt.value
			_, err := loadSubmitConfig(mapLookup(env))
			if err == nil {
				t.Fatal("expected frozen tuple validation to fail")
			}
		})
	}

	if _, err := loadSubmitConfig(mapLookup(valid)); err != nil {
		t.Fatalf("valid frozen tuple rejected: %v", err)
	}
}

func TestSubmissionRejectsChallengeMismatchBeforeSigner(t *testing.T) {
	base := frozenPaymentRequired(testPayee)
	tests := []struct {
		name   string
		mutate func(*x402.PaymentRequired)
	}{
		{name: "v1", mutate: func(r *x402.PaymentRequired) { r.X402Version = 1 }},
		{name: "scheme", mutate: func(r *x402.PaymentRequired) { r.Accepts[0].Scheme = "upto" }},
		{name: "network", mutate: func(r *x402.PaymentRequired) { r.Accepts[0].Network = "eip155:8453" }},
		{name: "asset", mutate: func(r *x402.PaymentRequired) { r.Accepts[0].Asset = testPayee }},
		{name: "amount", mutate: func(r *x402.PaymentRequired) { r.Accepts[0].Amount = "1001" }},
		{name: "payee", mutate: func(r *x402.PaymentRequired) { r.Accepts[0].PayTo = "0x2222222222222222222222222222222222222222" }},
		{name: "payment flow", mutate: func(r *x402.PaymentRequired) { r.Accepts[0].Extra["paymentFlow"] = "authorization" }},
		{name: "wrong timeout", mutate: func(r *x402.PaymentRequired) { r.Accepts[0].MaxTimeoutSeconds = 14 }},
		{name: "multiple accepts", mutate: func(r *x402.PaymentRequired) { r.Accepts = append(r.Accepts, r.Accepts[0]) }},
		{name: "wrong transfer method", mutate: func(r *x402.PaymentRequired) { r.Accepts[0].Extra["assetTransferMethod"] = "permit2" }},
		{name: "missing resource", mutate: func(r *x402.PaymentRequired) { r.Resource = nil }},
		{name: "resource mismatch", mutate: func(r *x402.PaymentRequired) { r.Resource.URL = "http://127.0.0.1:54321/functions/v1/other" }},
		{name: "domain name", mutate: func(r *x402.PaymentRequired) { r.Accepts[0].Extra["name"] = "USD Coin" }},
		{name: "domain version", mutate: func(r *x402.PaymentRequired) { r.Accepts[0].Extra["version"] = "1" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			required := base
			required.Accepts = append([]x402.PaymentRequirements(nil), base.Accepts...)
			required.Accepts[0].Extra = cloneAnyMap(base.Accepts[0].Extra)
			tt.mutate(&required)
			server := challengeServer(t, required, nil)
			defer server.Close()

			calls := 0
			deps := testDependencies(server.Client(), func(string) (wallet.Signer, error) {
				calls++
				return signerFromKey(t, testKeyA), nil
			})
			receipt, err := executeSubmission(context.Background(), testConfig(server.URL), deps)
			if err == nil || receipt.Outcome != outcomeRejected {
				t.Fatalf("expected rejected mismatch, receipt=%+v err=%v", receipt, err)
			}
			if calls != 0 {
				t.Fatalf("signer factory called %d times before tuple validation", calls)
			}
		})
	}
}

func TestSubmissionRejectsRedirectAndOversizedBodyBeforeSigner(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeChallenge(t, w, frozenPaymentRequired(testPayee))
		}))
		defer target.Close()
		redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
		defer redirect.Close()

		calls := 0
		deps := testDependencies(redirect.Client(), func(string) (wallet.Signer, error) {
			calls++
			return signerFromKey(t, testKeyA), nil
		})
		receipt, err := executeSubmission(context.Background(), testConfig(redirect.URL), deps)
		if err == nil || receipt.Outcome != outcomeRejected || calls != 0 {
			t.Fatalf("redirect did not fail closed: receipt=%+v err=%v signer_calls=%d", receipt, err, calls)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeChallenge(t, w, frozenPaymentRequired(testPayee))
			_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBody+1))
		}))
		defer server.Close()
		calls := 0
		deps := testDependencies(server.Client(), func(string) (wallet.Signer, error) {
			calls++
			return signerFromKey(t, testKeyA), nil
		})
		receipt, err := executeSubmission(context.Background(), testConfig(server.URL), deps)
		if err == nil || receipt.Outcome != outcomeRejected || calls != 0 {
			t.Fatalf("oversized response did not fail closed: receipt=%+v err=%v signer_calls=%d", receipt, err, calls)
		}
	})
}

func TestSubmissionRejectsInvalidAndWrongAccountSignatures(t *testing.T) {
	tests := []struct {
		name   string
		signer wallet.Signer
	}{
		{name: "invalid signature", signer: &invalidSigner{address: signerFromKey(t, testKeyA).Address()}},
		{name: "wrong account signature", signer: &wrongAccountSigner{address: signerFromKey(t, testKeyA).Address(), delegate: signerFromKey(t, testKeyB)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paidCalls := 0
			server := challengeServer(t, frozenPaymentRequired(testPayee), func(_ http.ResponseWriter, _ *http.Request) { paidCalls++ })
			defer server.Close()
			deps := testDependencies(server.Client(), func(string) (wallet.Signer, error) { return tt.signer, nil })
			receipt, err := executeSubmission(context.Background(), testConfig(server.URL), deps)
			if err == nil || receipt.Outcome != outcomeRejected {
				t.Fatalf("signature was accepted: receipt=%+v err=%v", receipt, err)
			}
			if paidCalls != 0 {
				t.Fatalf("invalid authorization reached resource %d times", paidCalls)
			}
		})
	}
}

func TestSubmissionCreatesOneAuthorizationAndReusesIdenticalBytesOnceAfterAmbiguousError(t *testing.T) {
	signer := &countingSigner{delegate: signerFromKey(t, testKeyA)}
	resourceURL := "http://127.0.0.1:54321/functions/v1/x402-compatibility-probe"
	required := frozenPaymentRequired(testPayee)
	required.Resource.URL = resourceURL
	rt := &ambiguousRoundTripper{required: required}
	client := &http.Client{Transport: rt}
	deps := testDependencies(client, func(string) (wallet.Signer, error) { return signer, nil })

	receipt, err := executeSubmission(context.Background(), testConfig(resourceURL), deps)
	if err != nil {
		t.Fatalf("ambiguous retry failed: %v", err)
	}
	if receipt.Outcome != outcomeSettled || receipt.TransactionHash != testTransactionHash {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if signer.calls != 1 {
		t.Fatalf("signed %d authorizations, want exactly one", signer.calls)
	}
	if len(rt.paymentHeaders) != 2 || rt.paymentHeaders[0] == "" || rt.paymentHeaders[0] != rt.paymentHeaders[1] {
		t.Fatalf("paid attempts did not reuse exact authorization bytes: %#v", rt.paymentHeaders)
	}
}

func TestEveryNonStrictPaidResponseIsInconclusiveAndRetainsRequestIDs(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		settlement x402.SettleResponse
		malformed  bool
		commit     string
		oversized  bool
	}{
		{name: "facilitator failure", status: http.StatusBadGateway},
		{name: "reported failure", status: http.StatusPaymentRequired, settlement: x402.SettleResponse{Success: false, Network: frozenNetwork}},
		{name: "missing transaction", status: http.StatusOK, settlement: x402.SettleResponse{Success: true, Network: frozenNetwork, Amount: frozenAmount}},
		{name: "missing receipt", status: http.StatusOK},
		{name: "malformed receipt", status: http.StatusOK, malformed: true},
		{name: "changed commit", status: http.StatusOK, commit: "different-commit", settlement: validSettlement()},
		{name: "wrong network", status: http.StatusOK, settlement: x402.SettleResponse{Success: true, Transaction: testTransactionHash, Network: "eip155:8453", Amount: frozenAmount}},
		{name: "wrong amount", status: http.StatusOK, settlement: x402.SettleResponse{Success: true, Transaction: testTransactionHash, Network: frozenNetwork, Amount: "1001"}},
		{name: "wrong hash", status: http.StatusOK, settlement: x402.SettleResponse{Success: true, Transaction: "0xabc123", Network: frozenNetwork, Amount: frozenAmount}},
		{name: "oversized response", status: http.StatusOK, settlement: validSettlement(), oversized: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := challengeServer(t, frozenPaymentRequired(testPayee), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-Request-ID", "failure-req-123")
				if tt.commit != "" {
					w.Header().Set(serverCommitHeader, tt.commit)
				}
				if tt.malformed {
					w.Header().Set(paymentResponseHeader, "not-base64")
				}
				if tt.settlement.Network != "" {
					writeSettlement(t, w, tt.settlement)
				}
				w.WriteHeader(tt.status)
				if tt.oversized {
					_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBody+1))
				}
			})
			defer server.Close()
			deps := testDependencies(server.Client(), func(string) (wallet.Signer, error) { return signerFromKey(t, testKeyA), nil })
			receipt, err := executeSubmission(context.Background(), testConfig(server.URL), deps)
			if err == nil || receipt.Outcome != outcomeInconclusive {
				t.Fatalf("failure accepted: receipt=%+v err=%v", receipt, err)
			}
			if len(receipt.FacilitatorRequestIDs) != 1 || receipt.FacilitatorRequestIDs[0] != "failure-req-123" {
				t.Fatalf("failure request id not retained: %+v", receipt)
			}
		})
	}
}

func TestPaidRedirectIsInconclusiveAndNotFollowed(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	defer target.Close()
	server := challengeServer(t, frozenPaymentRequired(testPayee), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "redirect-req")
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	defer server.Close()
	receipt, err := executeSubmission(context.Background(), testConfig(server.URL), testDependencies(server.Client(), func(string) (wallet.Signer, error) { return signerFromKey(t, testKeyA), nil }))
	if err == nil || receipt.Outcome != outcomeInconclusive || targetCalls != 0 {
		t.Fatalf("paid redirect did not stay inconclusive: receipt=%+v err=%v target_calls=%d", receipt, err, targetCalls)
	}
	if len(receipt.FacilitatorRequestIDs) != 1 || receipt.FacilitatorRequestIDs[0] != "redirect-req" {
		t.Fatalf("redirect request id not retained: %+v", receipt)
	}
}

func TestDoubleTransportErrorStopsAfterTwoIdenticalPaidAttempts(t *testing.T) {
	resourceURL := "http://127.0.0.1:54321/functions/v1/x402-compatibility-probe"
	required := frozenPaymentRequired(testPayee)
	required.Resource.URL = resourceURL
	rt := &alwaysAmbiguousRoundTripper{required: required}
	signer := &countingSigner{delegate: signerFromKey(t, testKeyA)}
	receipt, err := executeSubmission(context.Background(), testConfig(resourceURL), testDependencies(&http.Client{Transport: rt}, func(string) (wallet.Signer, error) { return signer, nil }))
	if err == nil || receipt.Outcome != outcomeInconclusive {
		t.Fatalf("double transport error did not stay inconclusive: receipt=%+v err=%v", receipt, err)
	}
	if signer.calls != 1 || len(rt.paymentHeaders) != 2 || rt.paymentHeaders[0] == "" || rt.paymentHeaders[0] != rt.paymentHeaders[1] {
		t.Fatalf("retry bounds violated: signer_calls=%d headers=%#v", signer.calls, rt.paymentHeaders)
	}
}

func TestSuccessfulReceiptIsFixedAndRedacted(t *testing.T) {
	required := frozenPaymentRequired(testPayee)
	required.Accepts[0].Extra["name"] = "USDC"
	required.Accepts[0].Extra["version"] = "2"
	server := challengeServer(t, required, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Facilitator-Request-ID", "fac-123")
		w.Header().Set("X-Request-ID", "req-456")
		w.Header().Set(serverCommitHeader, "server-commit")
		writeSettlement(t, w, x402.SettleResponse{Success: true, Transaction: testTransactionHash, Network: frozenNetwork, Amount: frozenAmount})
		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()
	deps := testDependencies(server.Client(), func(string) (wallet.Signer, error) { return signerFromKey(t, testKeyA), nil })
	deps.cliCommit = func() string { return "cli-commit" }
	receipt, err := executeSubmission(context.Background(), testConfig(server.URL), deps)
	if err != nil {
		t.Fatalf("submission failed: %v", err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"x402_version", "scheme", "network", "payment_flow", "asset", "amount", "payee", "signer_backend", "outcome", "transaction_hash", "facilitator_request_ids", "cli_commit", "server_commit"}
	var object map[string]interface{}
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != len(wantKeys) {
		t.Fatalf("receipt fields = %v, want only %v", object, wantKeys)
	}
	for _, key := range wantKeys {
		if _, ok := object[key]; !ok {
			t.Fatalf("receipt missing %q: %s", key, encoded)
		}
	}
	for _, forbidden := range []string{"signature", "authorization", testKeyA, "PAYMENT-SIGNATURE"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("receipt leaked %q: %s", forbidden, encoded)
		}
	}
}

func testConfig(resourceURL string) submitConfig {
	return submitConfig{ResourceURL: resourceURL, SignerBackend: "local", Asset: frozenAsset, Payee: testPayee, Amount: frozenAmount, MaxAmount: frozenAmount}
}

func testDependencies(client *http.Client, factory func(string) (wallet.Signer, error)) dependencies {
	return dependencies{httpClient: client, newSigner: factory, checkFacilitatorSupport: func(context.Context, *http.Client) error { return nil }, cliCommit: func() string { return "test-cli" }}
}

func frozenPaymentRequired(payee string) x402.PaymentRequired {
	return x402.PaymentRequired{X402Version: frozenVersion, Resource: &x402.ResourceInfo{URL: "http://127.0.0.1/probe"}, Accepts: []x402.PaymentRequirements{frozenRequirement(payee)}}
}

func validSettlement() x402.SettleResponse {
	return x402.SettleResponse{Success: true, Transaction: testTransactionHash, Network: frozenNetwork, Amount: frozenAmount}
}

func frozenRequirement(payee string) x402.PaymentRequirements {
	return x402.PaymentRequirements{Scheme: frozenScheme, Network: frozenNetwork, Asset: frozenAsset, Amount: frozenAmount, PayTo: payee, MaxTimeoutSeconds: frozenTimeoutSeconds, Extra: map[string]interface{}{"paymentFlow": frozenPaymentFlow}}
}

func challengeServer(t *testing.T, required x402.PaymentRequired, paid func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(paymentSignatureHeader) == "" {
			challenge := required
			if challenge.Resource != nil && challenge.Resource.URL == "http://127.0.0.1/probe" {
				resource := *challenge.Resource
				resource.URL = server.URL
				challenge.Resource = &resource
			}
			writeChallenge(t, w, challenge)
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		if paid != nil {
			w.Header().Set(serverCommitHeader, "server-commit")
			paid(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	return server
}

func writeChallenge(t *testing.T, w http.ResponseWriter, required x402.PaymentRequired) {
	t.Helper()
	data, err := json.Marshal(required)
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set(paymentRequiredHeader, base64.StdEncoding.EncodeToString(data))
	w.Header().Set(serverCommitHeader, "server-commit")
}

func writeSettlement(t *testing.T, w http.ResponseWriter, settlement x402.SettleResponse) {
	t.Helper()
	data, err := json.Marshal(settlement)
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set(paymentResponseHeader, base64.StdEncoding.EncodeToString(data))
}

func signerFromKey(t *testing.T, key string) wallet.Signer {
	t.Helper()
	signer, err := evmsigners.NewClientSignerFromPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

type invalidSigner struct{ address string }

func (s *invalidSigner) Address() string { return s.address }
func (s *invalidSigner) SignTypedData(context.Context, evm.TypedDataDomain, map[string][]evm.TypedDataField, string, map[string]interface{}) ([]byte, error) {
	return make([]byte, 65), nil
}

type wrongAccountSigner struct {
	address  string
	delegate wallet.Signer
}

func (s *wrongAccountSigner) Address() string { return s.address }
func (s *wrongAccountSigner) SignTypedData(ctx context.Context, domain evm.TypedDataDomain, types map[string][]evm.TypedDataField, primary string, message map[string]interface{}) ([]byte, error) {
	return s.delegate.SignTypedData(ctx, domain, types, primary, message)
}

type countingSigner struct {
	delegate wallet.Signer
	calls    int
}

func (s *countingSigner) Address() string { return s.delegate.Address() }
func (s *countingSigner) SignTypedData(ctx context.Context, domain evm.TypedDataDomain, types map[string][]evm.TypedDataField, primary string, message map[string]interface{}) ([]byte, error) {
	s.calls++
	return s.delegate.SignTypedData(ctx, domain, types, primary, message)
}

type ambiguousRoundTripper struct {
	mu             sync.Mutex
	required       x402.PaymentRequired
	paymentHeaders []string
}

type alwaysAmbiguousRoundTripper struct {
	required       x402.PaymentRequired
	paymentHeaders []string
}

func (rt *alwaysAmbiguousRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	sig := req.Header.Get(paymentSignatureHeader)
	if sig == "" {
		data, _ := json.Marshal(rt.required)
		header := make(http.Header)
		header.Set(paymentRequiredHeader, base64.StdEncoding.EncodeToString(data))
		header.Set(serverCommitHeader, "server-commit")
		return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: io.NopCloser(strings.NewReader("challenge")), Request: req}, nil
	}
	rt.paymentHeaders = append(rt.paymentHeaders, sig)
	return nil, errors.New("ambiguous transport timeout")
}

func (rt *ambiguousRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	sig := req.Header.Get(paymentSignatureHeader)
	if sig == "" {
		data, _ := json.Marshal(rt.required)
		header := make(http.Header)
		header.Set(paymentRequiredHeader, base64.StdEncoding.EncodeToString(data))
		header.Set(serverCommitHeader, "server-commit")
		return &http.Response{StatusCode: http.StatusPaymentRequired, Header: header, Body: io.NopCloser(strings.NewReader("challenge")), Request: req}, nil
	}
	rt.paymentHeaders = append(rt.paymentHeaders, sig)
	if len(rt.paymentHeaders) == 1 {
		return nil, errors.New("ambiguous transport timeout")
	}
	settle, _ := json.Marshal(x402.SettleResponse{Success: true, Transaction: testTransactionHash, Network: frozenNetwork, Amount: frozenAmount})
	header := make(http.Header)
	header.Set(paymentResponseHeader, base64.StdEncoding.EncodeToString(settle))
	header.Set(serverCommitHeader, "server-commit")
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader("ok")), Request: req}, nil
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}
func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
func cloneAnyMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
