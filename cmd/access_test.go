package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/access"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/ngelik/ttsbuddy-cli/internal/wallet"
	"github.com/spf13/cobra"
	"github.com/x402-foundation/x402/go/v2/mechanisms/evm"
)

const (
	accessTestPrivateKey = "local-private-key-secret-value"
	accessTestCDPSecret  = "cdp-secret-value"
	accessTestSignature  = "payment-signature-secret-value"
)

func TestAccessPlansPublicPlainAndJSON(t *testing.T) {
	var calls atomic.Int32
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/access-pass-plans" {
			t.Fatalf("request = %s %s, want GET /v1/access-pass-plans", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Fatalf("plans sent Authorization: %q", auth)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"plans": []any{accessStarterPlanBody()}})
	}))

	home := t.TempDir()
	writeAccessTestConfig(t, home, map[string]any{
		"api_key":     testSubscriptionCredential(),
		"access_pass": storedAccessPassMap(testAccessPassCredential(), time.Now().Add(time.Hour)),
	})

	r := runCLI(t, envForTest(home, accessAgentURL(apiSrv), ""), "access", "plans")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "starter", "stdout")
	assertContains(t, r.Stdout, "500,000", "stdout")
	assertAccessOutputDoesNotLeak(t, r, false)

	r = runCLI(t, envForTest(home, accessAgentURL(apiSrv), ""), "access", "plans", "--json")
	assertExitCode(t, r, 0)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stdout, "\"plans\"", "stdout")
	assertAccessOutputDoesNotLeak(t, r, false)

	if calls.Load() != 2 {
		t.Fatalf("plans calls = %d, want 2", calls.Load())
	}
}

func TestAccessPlansRejectsArgsAndWalletFlagBeforeNetwork(t *testing.T) {
	var called atomic.Bool
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	home := t.TempDir()
	r := runCLI(t, envForTest(home, accessAgentURL(apiSrv), ""), "access", "plans", "starter")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "Usage:", "stderr")

	r = runCLI(t, envForTest(home, accessAgentURL(apiSrv), ""), "access", "plans", "--wallet", "local")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "Usage:", "stderr")

	if called.Load() {
		t.Fatal("plans validation failure called the network")
	}
}

func TestAccessBuyRejectsGlobalKeyAndDoesNotReadStoredCredentialsBeforePurchase(t *testing.T) {
	var called atomic.Bool
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	home := t.TempDir()
	storedSecret := "stored-config-secret-should-not-appear"
	writeRawAccessTestConfig(t, home, []byte(`{"api_key":"`+storedSecret+`",`))

	r := runCLI(t, envForTest(home, accessAgentURL(apiSrv), ""), "access", "buy", "starter", "--wallet", "local", "--max-price", "5.00", "--key", testSubscriptionCredential())
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "--key is not supported", "stderr")
	assertNotContains(t, r.Stderr, storedSecret, "stderr")
	assertNotContains(t, r.Stdout, storedSecret, "stdout")

	r = runCLI(t, envForTest(home, accessAgentURL(apiSrv), ""), "access", "buy", "starter", "--wallet", "local", "--max-price", "5.00")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "TTSBUDDY_EVM_PRIVATE_KEY", "stderr")
	assertNotContains(t, r.Stderr, "cannot parse config", "stderr")
	assertNotContains(t, r.Stderr, storedSecret, "stderr")
	assertNotContains(t, r.Stdout, storedSecret, "stdout")

	if called.Load() {
		t.Fatal("access buy called the network before validating local wallet environment")
	}
}

func TestAccessBuyValidatesSKUWalletAndMaxPriceBeforeSignerOrNetwork(t *testing.T) {
	var signerConstructed atomic.Bool
	var clientConstructed atomic.Bool
	restore := replaceAccessDeps(t)
	restore.newLocalSigner = func() (wallet.Signer, error) {
		signerConstructed.Store(true)
		return &accessTestSigner{}, nil
	}
	restore.newClient = func(string, string, bool) (accessCLIClient, error) {
		clientConstructed.Store(true)
		return &fakeAccessClient{}, nil
	}

	cases := []struct {
		name string
		args []string
	}{
		{"unknown sku", []string{"plus", "--wallet", "local", "--max-price", "5.00"}},
		{"missing wallet", []string{"starter", "--max-price", "5.00"}},
		{"unknown wallet", []string{"starter", "--wallet", "browser", "--max-price", "5.00"}},
		{"missing max price", []string{"starter", "--wallet", "local"}},
		{"zero max price", []string{"starter", "--wallet", "local", "--max-price", "0"}},
		{"negative max price", []string{"starter", "--wallet", "local", "--max-price", "-1.00"}},
		{"scientific max price", []string{"starter", "--wallet", "local", "--max-price", "5e0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signerConstructed.Store(false)
			clientConstructed.Store(false)
			cmd := accessBuyCommandForTest(t, tc.args...)
			err := runAccessBuy(cmd, []string{tc.args[0]})
			if err == nil {
				t.Fatal("expected validation error")
			}
			if signerConstructed.Load() || clientConstructed.Load() {
				t.Fatalf("validation constructed signer=%t client=%t", signerConstructed.Load(), clientConstructed.Load())
			}
		})
	}
}

func TestAccessBuyConstructsOnlySelectedWalletAndSavesRedactedPlainSummary(t *testing.T) {
	for _, walletName := range []string{"local", "cdp"} {
		t.Run(walletName, func(t *testing.T) {
			stdout, stderr := captureAccessWriters(t)
			restore := replaceAccessDeps(t)
			var localConstructs atomic.Int32
			var cdpConstructs atomic.Int32
			restore.newLocalSigner = func() (wallet.Signer, error) {
				localConstructs.Add(1)
				return &accessTestSigner{}, nil
			}
			restore.newCDPSigner = func() (wallet.Signer, error) {
				cdpConstructs.Add(1)
				return &accessTestSigner{}, nil
			}
			restore.client = &fakeAccessClient{
				buyFn: func(_ context.Context, buy access.BuyRequest) (*access.PurchaseResult, error) {
					if buy.SKU != "starter" || buy.MaxPrice != "5.00" || buy.Signer == nil {
						t.Fatalf("bad buy request: %#v", buy)
					}
					return accessPurchaseFixture(), nil
				},
			}
			var saved config.StoredAccessPass
			restore.saveAccessPass = func(pass config.StoredAccessPass) error {
				saved = pass
				return nil
			}
			setWalletEnv(t)

			cmd := accessBuyCommandForTest(t, "starter", "--wallet", walletName, "--max-price", "5.00")
			if err := runAccessBuy(cmd, []string{"starter"}); err != nil {
				t.Fatalf("runAccessBuy() error = %v", err)
			}

			if walletName == "local" && (localConstructs.Load() != 1 || cdpConstructs.Load() != 0) {
				t.Fatalf("constructors local=%d cdp=%d", localConstructs.Load(), cdpConstructs.Load())
			}
			if walletName == "cdp" && (localConstructs.Load() != 0 || cdpConstructs.Load() != 1) {
				t.Fatalf("constructors local=%d cdp=%d", localConstructs.Load(), cdpConstructs.Load())
			}
			if saved.Credential != testAccessPassCredential() || saved.PurchaseID != "purchase-1" || saved.RequestLimit != 100_000 {
				t.Fatalf("saved pass = %#v", saved)
			}
			assertContains(t, stdout.String(), "Access pass saved", "stdout")
			assertContains(t, stdout.String(), "ttsp_abcd1234_...", "stdout")
			assertContains(t, stdout.String(), "100,000", "stdout")
			if stderr.String() != "" {
				t.Fatalf("unexpected stderr: %q", stderr.String())
			}
			assertAccessStringsDoNotLeak(t, stdout.String(), stderr.String(), false)
		})
	}
}

func TestAccessBuySaveFailurePrintsOneTimeRecoveryPassOnlyWhereDocumented(t *testing.T) {
	stdout, stderr := captureAccessWriters(t)
	restore := replaceAccessDeps(t)
	restore.client = &fakeAccessClient{buyFn: func(context.Context, access.BuyRequest) (*access.PurchaseResult, error) {
		return accessPurchaseFixture(), nil
	}}
	restore.saveAccessPass = func(config.StoredAccessPass) error {
		return errors.New("disk full for " + testAccessPassCredential())
	}
	restore.newLocalSigner = func() (wallet.Signer, error) { return &accessTestSigner{}, nil }
	setWalletEnv(t)

	cmd := accessBuyCommandForTest(t, "starter", "--wallet", "local", "--max-price", "5.00")
	if err := runAccessBuy(cmd, []string{"starter"}); err != nil {
		t.Fatalf("runAccessBuy() error = %v", err)
	}

	if strings.Count(stdout.String(), testAccessPassCredential()) != 1 {
		t.Fatalf("stdout should contain full pass exactly once, got %q", stdout.String())
	}
	assertContains(t, stderr.String(), "could not be saved", "stderr")
	assertNotContains(t, stderr.String(), testAccessPassCredential(), "stderr")
	assertAccessStringsDoNotLeak(t, stdout.String(), stderr.String(), true)
}

func TestAccessBuyJSONReturnsSingleStructuredObject(t *testing.T) {
	stdout, stderr := captureAccessWriters(t)
	restore := replaceAccessDeps(t)
	restore.client = &fakeAccessClient{buyFn: func(context.Context, access.BuyRequest) (*access.PurchaseResult, error) {
		return accessPurchaseFixture(), nil
	}}
	restore.saveAccessPass = func(config.StoredAccessPass) error {
		return errors.New("save failed for " + testAccessPassCredential())
	}
	restore.newLocalSigner = func() (wallet.Signer, error) { return &accessTestSigner{}, nil }
	setWalletEnv(t)
	flagJSON = true
	t.Cleanup(func() { flagJSON = false })

	cmd := accessBuyCommandForTest(t, "starter", "--wallet", "local", "--max-price", "5.00")
	if err := runAccessBuy(cmd, []string{"starter"}); err != nil {
		t.Fatalf("runAccessBuy() error = %v", err)
	}
	if stderr.String() != "" {
		t.Fatalf("JSON mode should not write stderr, got %q", stderr.String())
	}
	assertValidJSON(t, stdout.String())
	if strings.Count(stdout.String(), testAccessPassCredential()) != 1 {
		t.Fatalf("JSON output should contain complete pass once, got %q", stdout.String())
	}
	assertContains(t, stdout.String(), "\"saved\": false", "stdout")
}

func TestAccessBuyJSONSavedSuccessDoesNotDiscloseSecrets(t *testing.T) {
	stdout, stderr := captureAccessWriters(t)
	restore := replaceAccessDeps(t)
	restore.client = &fakeAccessClient{buyFn: func(context.Context, access.BuyRequest) (*access.PurchaseResult, error) {
		return accessPurchaseFixture(), nil
	}}
	restore.newLocalSigner = func() (wallet.Signer, error) { return &accessTestSigner{}, nil }
	setWalletEnv(t)
	flagJSON = true
	t.Cleanup(func() { flagJSON = false })

	cmd := accessBuyCommandForTest(t, "starter", "--wallet", "local", "--max-price", "5.00")
	if err := runAccessBuy(cmd, []string{"starter"}); err != nil {
		t.Fatalf("runAccessBuy() error = %v", err)
	}
	if stderr.String() != "" {
		t.Fatalf("JSON mode should not write stderr, got %q", stderr.String())
	}
	assertValidJSON(t, stdout.String())
	assertContains(t, stdout.String(), "\"saved\": true", "stdout")
	assertContains(t, stdout.String(), "ttsp_abcd1234_...", "stdout")
	assertAccessStringsDoNotLeak(t, stdout.String(), stderr.String(), false)
}

func TestAccessStatusUsesPassEnvOrStoredPassAndCallsServerWhenStoredPassExpired(t *testing.T) {
	storedPass := "ttsp_" + strings.Repeat("1", 8) + "_" + strings.Repeat("2", 48)
	envPass := testAccessPassCredential()
	tests := []struct {
		name      string
		env       []string
		envAPIKey string
		wantAuth  string
		wantState string
	}{
		{"stored expired", nil, "", storedPass, "active"},
		{"env pass", []string{"TTSBUDDY_ACCESS_PASS=" + envPass}, testSubscriptionCredential(), envPass, "revoked"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called atomic.Bool
			apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called.Store(true)
				if r.Method != http.MethodGet || r.URL.Path != "/v1/access-passes/current" {
					t.Fatalf("request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+tt.wantAuth {
					t.Fatalf("Authorization = %q, want bearer pass", got)
				}
				body := accessStatusBody(tt.wantState)
				_ = json.NewEncoder(w).Encode(body)
			}))
			home := t.TempDir()
			writeAccessTestConfig(t, home, map[string]any{
				"api_key":     testSubscriptionCredential(),
				"access_pass": storedAccessPassMap(storedPass, time.Now().Add(-time.Hour)),
			})
			env := append(envForTest(home, accessAgentURL(apiSrv), tt.envAPIKey), tt.env...)
			r := runCLI(t, env, "access", "status")
			assertExitCode(t, r, 0)
			assertContains(t, r.Stdout, tt.wantState, "stdout")
			assertContains(t, r.Stdout, "499,900", "stdout")
			assertAccessOutputDoesNotLeak(t, r, false)
			if !called.Load() {
				t.Fatal("status did not call server")
			}
		})
	}
}

func TestAccessStatusRequiresPassAndRejectsGlobalKey(t *testing.T) {
	var called atomic.Bool
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	home := t.TempDir()
	writeAccessTestConfig(t, home, map[string]any{"api_key": testSubscriptionCredential()})

	r := runCLI(t, envForTest(home, accessAgentURL(apiSrv), ""), "access", "status")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "TTSBUDDY_ACCESS_PASS", "stderr")

	r = runCLI(t, envForTest(home, accessAgentURL(apiSrv), ""), "access", "status", "--key", testSubscriptionCredential())
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "--key is not supported", "stderr")
	if called.Load() {
		t.Fatal("status called server without an access pass")
	}
}

func TestAccessForgetIsLocalOnlyIdempotentAndPreservesAPIKey(t *testing.T) {
	home := t.TempDir()
	pass := testAccessPassCredential()
	apiKey := testSubscriptionCredential()
	writeAccessTestConfig(t, home, map[string]any{
		"api_key":     apiKey,
		"access_pass": storedAccessPassMap(pass, time.Now().Add(time.Hour)),
	})

	r := runCLI(t, envForTest(home, "", ""), "access", "forget")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "Access pass removed", "stdout")
	data := readAccessTestConfig(t, home)
	assertNotContains(t, string(data), pass, "config")
	assertContains(t, string(data), apiKey, "config")

	r = runCLI(t, envForTest(home, "", ""), "access", "forget")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "No stored access pass", "stdout")
	assertAccessOutputDoesNotLeak(t, r, false)
}

func TestAccessForgetPreservesConcurrentReplacementPassAndAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	loadedPass := "ttsp_" + strings.Repeat("1", 8) + "_" + strings.Repeat("2", 48)
	replacementPass := testAccessPassCredential()
	apiKey := testSubscriptionCredential()
	writeAccessTestConfig(t, home, map[string]any{
		"api_key":     apiKey,
		"access_pass": storedAccessPassMap(loadedPass, time.Now().Add(time.Hour)),
	})

	stdout, stderr := captureAccessWriters(t)
	restore := replaceAccessDeps(t)
	var clientConstructed atomic.Bool
	restore.newClient = func(string, string, bool) (accessCLIClient, error) {
		clientConstructed.Store(true)
		return nil, errors.New("network client should not be constructed")
	}
	oldResolved := resolvedCfg
	resolvedCfg = &config.ResolvedConfig{
		AccessPass: &config.StoredAccessPass{Credential: loadedPass},
	}
	t.Cleanup(func() { resolvedCfg = oldResolved })

	writeAccessTestConfig(t, home, map[string]any{
		"api_key":     apiKey,
		"access_pass": storedAccessPassMap(replacementPass, time.Now().Add(time.Hour)),
	})
	if err := runAccessForget(&cobra.Command{Use: "forget"}); err != nil {
		t.Fatalf("runAccessForget() error = %v", err)
	}

	assertContains(t, stdout.String(), "No stored access pass", "stdout")
	if stderr.String() != "" {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	data := string(readAccessTestConfig(t, home))
	assertContains(t, data, replacementPass, "config")
	assertContains(t, data, apiKey, "config")
	assertNotContains(t, data, loadedPass, "config")
	if clientConstructed.Load() {
		t.Fatal("access forget constructed a network client")
	}
	assertAccessStringsDoNotLeak(t, stdout.String(), stderr.String(), false)
}

type accessDepsRestore struct {
	client         accessCLIClient
	newClient      func(string, string, bool) (accessCLIClient, error)
	newLocalSigner func() (wallet.Signer, error)
	newCDPSigner   func() (wallet.Signer, error)
	saveAccessPass func(config.StoredAccessPass) error
}

func replaceAccessDeps(t *testing.T) *accessDepsRestore {
	t.Helper()
	oldNewClient := newAccessClientForCLI
	oldNewLocal := newAccessLocalSigner
	oldNewCDP := newAccessCDPSigner
	oldSave := saveAccessPassForCLI
	restore := &accessDepsRestore{}
	newAccessClientForCLI = func(agentURL, bearer string, allowCustom bool) (accessCLIClient, error) {
		if restore.newClient != nil {
			return restore.newClient(agentURL, bearer, allowCustom)
		}
		if restore.client != nil {
			return restore.client, nil
		}
		return &fakeAccessClient{}, nil
	}
	newAccessLocalSigner = func() (wallet.Signer, error) {
		if restore.newLocalSigner != nil {
			return restore.newLocalSigner()
		}
		return &accessTestSigner{}, nil
	}
	newAccessCDPSigner = func() (wallet.Signer, error) {
		if restore.newCDPSigner != nil {
			return restore.newCDPSigner()
		}
		return &accessTestSigner{}, nil
	}
	saveAccessPassForCLI = func(pass config.StoredAccessPass) error {
		if restore.saveAccessPass != nil {
			return restore.saveAccessPass(pass)
		}
		return nil
	}
	t.Cleanup(func() {
		newAccessClientForCLI = oldNewClient
		newAccessLocalSigner = oldNewLocal
		newAccessCDPSigner = oldNewCDP
		saveAccessPassForCLI = oldSave
		accessBuyWallet = ""
		accessBuyMaxPrice = ""
		flagAPIKey = ""
		flagJSON = false
	})
	return restore
}

func captureAccessWriters(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	oldStdout := accessStdout
	oldStderr := accessStderr
	accessStdout = &stdout
	accessStderr = &stderr
	t.Cleanup(func() {
		accessStdout = oldStdout
		accessStderr = oldStderr
	})
	return &stdout, &stderr
}

func accessBuyCommandForTest(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	accessBuyWallet = ""
	accessBuyMaxPrice = ""
	flagAPIKey = ""
	cmd := &cobra.Command{Use: "buy"}
	cmd.Flags().StringVar(&accessBuyWallet, "wallet", "", "")
	cmd.Flags().StringVar(&accessBuyMaxPrice, "max-price", "", "")
	cmd.Flags().StringVar(&flagAPIKey, "key", "", "")
	for i := 1; i < len(args); i += 2 {
		if i+1 >= len(args) {
			t.Fatalf("missing test value for %s", args[i])
		}
		name := strings.TrimPrefix(args[i], "--")
		if err := cmd.Flags().Set(name, args[i+1]); err != nil {
			t.Fatalf("set flag %s: %v", name, err)
		}
	}
	return cmd
}

type fakeAccessClient struct {
	plansFn  func(context.Context) (*access.PlansResponse, error)
	statusFn func(context.Context) (*access.StatusResult, error)
	buyFn    func(context.Context, access.BuyRequest) (*access.PurchaseResult, error)
}

func (f *fakeAccessClient) Plans(ctx context.Context) (*access.PlansResponse, error) {
	if f.plansFn != nil {
		return f.plansFn(ctx)
	}
	return nil, errors.New("unexpected plans call")
}

func (f *fakeAccessClient) Status(ctx context.Context) (*access.StatusResult, error) {
	if f.statusFn != nil {
		return f.statusFn(ctx)
	}
	return nil, errors.New("unexpected status call")
}

func (f *fakeAccessClient) Buy(ctx context.Context, buy access.BuyRequest) (*access.PurchaseResult, error) {
	if f.buyFn != nil {
		return f.buyFn(ctx, buy)
	}
	return nil, errors.New("unexpected buy call")
}

type accessTestSigner struct{}

var _ wallet.Signer = (*accessTestSigner)(nil)

func (s *accessTestSigner) Address() string { return "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266" }

func (s *accessTestSigner) SignTypedData(context.Context, evm.TypedDataDomain, map[string][]evm.TypedDataField, string, map[string]interface{}) ([]byte, error) {
	return []byte(accessTestSignature), nil
}

func setWalletEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TTSBUDDY_EVM_PRIVATE_KEY", accessTestPrivateKey)
	t.Setenv("CDP_API_KEY_ID", "cdp-key-id")
	t.Setenv("CDP_API_KEY_SECRET", accessTestCDPSecret)
	t.Setenv("CDP_WALLET_SECRET", "cdp-wallet-secret")
	t.Setenv("TTSBUDDY_CDP_EVM_ACCOUNT_ADDRESS", "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")
}

func accessPurchaseFixture() *access.PurchaseResult {
	return &access.PurchaseResult{
		Pass:              testAccessPassCredential(),
		Status:            "active",
		AllowanceUnits:    500_000,
		ReservedUnits:     0,
		ConsumedUnits:     0,
		RemainingUnits:    500_000,
		RequestLimitUnits: 100_000,
		ExpiresAt:         time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		Receipt: access.PurchaseReceipt{
			PurchaseID:  "purchase-1",
			Network:     "eip155:84532",
			Transaction: "0xabc123",
			Asset:       "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
			Amount:      "5000000",
			Payer:       "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		},
	}
}

func accessStarterPlanBody() map[string]any {
	return map[string]any{
		"sku":     "starter",
		"version": 1,
		"price": map[string]any{
			"display":        "5.00",
			"atomic":         "5000000",
			"asset":          "USDC",
			"asset_decimals": 6,
			"network":        "eip155:84532",
			"pay_to":         "0x52908400098527886E0F7030069857D2E4169EE7",
		},
		"allowance_units":     500_000,
		"request_limit_units": 100_000,
		"valid_for_seconds":   2_592_000,
		"voice_policy":        "all_approved_public_voices",
	}
}

func accessStatusBody(status string) map[string]any {
	return map[string]any{
		"status":              status,
		"allowance_units":     500_000,
		"reserved_units":      0,
		"consumed_units":      100,
		"remaining_units":     499_900,
		"request_limit_units": 100_000,
		"expires_at":          time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"plan":                accessStarterPlanBody(),
		"receipt": map[string]any{
			"purchase_id": "purchase-1",
			"network":     "eip155:84532",
			"transaction": "0xabc123",
			"asset":       "USDC",
			"amount":      "5000000",
			"payer":       "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		},
	}
}

func storedAccessPassMap(pass string, expires time.Time) map[string]any {
	return map[string]any{
		"credential":          pass,
		"purchase_id":         "purchase-1",
		"expires_at":          expires.UTC().Format(time.RFC3339),
		"network":             "eip155:84532",
		"allowance_units":     500_000,
		"request_limit_units": 100_000,
	}
}

func writeAccessTestConfig(t *testing.T, home string, cfg map[string]any) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeRawAccessTestConfig(t, home, append(data, '\n'))
}

func writeRawAccessTestConfig(t *testing.T, home string, data []byte) {
	t.Helper()
	dir := filepath.Join(home, ".ttsbuddy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func accessAgentURL(serverURL string) string {
	return strings.TrimRight(serverURL, "/") + "/v1/agent-tts"
}

func readAccessTestConfig(t *testing.T, home string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".ttsbuddy", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testAccessPassCredential() string {
	return "ttsp_abcd1234_" + strings.Repeat("c", 48)
}

func assertAccessOutputDoesNotLeak(t *testing.T, r cliResult, allowFullPass bool) {
	t.Helper()
	assertAccessStringsDoNotLeak(t, r.Stdout, r.Stderr, allowFullPass)
}

func assertAccessStringsDoNotLeak(t *testing.T, stdout, stderr string, allowFullPass bool) {
	t.Helper()
	fullPass := testAccessPassCredential()
	if !allowFullPass {
		assertNotContains(t, stdout, fullPass, "stdout")
	}
	assertNotContains(t, stderr, fullPass, "stderr")
	for _, secret := range []string{accessTestPrivateKey, accessTestCDPSecret, accessTestSignature} {
		assertNotContains(t, stdout, secret, "stdout")
		assertNotContains(t, stderr, secret, "stderr")
	}
	if allowFullPass && strings.Count(stdout, fullPass) > 1 {
		t.Fatalf("stdout contains recovery pass more than once: %q", stdout)
	}
	if strings.Contains(fmt.Sprint(stdout, stderr), "PAYMENT-SIGNATURE") {
		t.Fatalf("payment header name leaked: stdout=%q stderr=%q", stdout, stderr)
	}
}
