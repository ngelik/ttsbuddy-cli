package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/config"
)

const deletionTestEmail = "deletion-fixture@example.com"
const deletionCorrelation = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

type deletionFixture struct {
	home          string
	env           []string
	jwt           string
	clerkCalls    atomic.Int32
	deleteCalls   atomic.Int32
	getCalls      atomic.Int32
	outcome       atomic.Int32 // 0 complete, 1 malformed, 2 partial, 3 unauthorized
	wrongOwner    atomic.Bool
	secondJWT     string
	failStart     atomic.Bool
	failStartSave atomic.Bool
}

func deletionTestJWT(owner string) string {
	data, _ := json.Marshal(map[string]any{"sub": owner, "exp": time.Now().Add(time.Hour).Unix()})
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(data) + ".fixture-signature"
}
func newDeletionFixture(t *testing.T) *deletionFixture {
	t.Helper()
	f := &deletionFixture{home: t.TempDir(), jwt: deletionTestJWT("user_fixture"), secondJWT: deletionTestJWT("user_other")}
	server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/delete-account" {
			expectedJWT := f.jwt
			if f.wrongOwner.Load() {
				expectedJWT = f.secondJWT
			}
			if r.Header.Get("Authorization") != "Bearer "+expectedJWT {
				t.Error("deletion used an unexpected credential")
			}
			switch r.Method {
			case http.MethodDelete:
				f.deleteCalls.Add(1)
				var body struct {
					Key string `json:"idempotency_key"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid body")
				}
				state := readDeletionFixture(t, f)
				if body.Key == "" || body.Key != state.RequestID || body.Key != r.Header.Get("Idempotency-Key") || !state.Submitted {
					t.Error("request identity was not persisted before DELETE")
				}
			case http.MethodGet:
				f.getCalls.Add(1)
			default:
				t.Errorf("unexpected API method %s", r.Method)
			}
			switch f.outcome.Load() {
			case 1:
				_, _ = fmt.Fprint(w, `{"invalid":"response"}`)
			case 2:
				w.WriteHeader(409)
				_, _ = fmt.Fprintf(w, `{"success":false,"state":"failed","partial_cleanup":true,"retryable":true,"error_category":"provider_object_deletion","error_code":"PROVIDER_CLEANUP_FAILED","correlation_id":%q}`, deletionCorrelation)
			case 4:
				w.WriteHeader(404)
				_, _ = fmt.Fprint(w, `{"success":false,"state":"none"}`)
			case 3:
				w.WriteHeader(401)
				_, _ = fmt.Fprint(w, `{"success":false,"error_category":"authentication","error_code":"UNAUTHORIZED"}`)
			default:
				if f.outcome.Load() == 5 {
					if err := os.Chmod(filepath.Join(f.home, ".ttsbuddy", "pending_deletion.json"), 0644); err != nil {
						t.Error(err)
					}
				}
				_, _ = fmt.Fprintf(w, `{"success":true,"state":"completed","apple_authorization":"not_linked","correlation_id":%q}`, deletionCorrelation)
			}
			return
		}
		f.clerkCalls.Add(1)
		w.Header().Set("Authorization", "native-client-fixture")
		switch r.URL.Path {
		case "/v1/client":
			_, _ = fmt.Fprint(w, `{"response":{"id":"client_fixture"}}`)
		case "/v1/client/sign_ins":
			if f.failStart.Load() {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, `{"errors":[{"code":"form_identifier_not_found"}]}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"response":{"id":"si_fixture","status":"needs_first_factor","supported_first_factors":[{"strategy":"email_code","email_address_id":"idn_fixture"}]}}`)
		case "/v1/client/sign_ins/si_fixture/prepare_first_factor":
			if f.failStartSave.Load() {
				if err := os.Chmod(filepath.Join(f.home, ".ttsbuddy"), 0755); err != nil {
					t.Error(err)
				}
			}
			_, _ = fmt.Fprint(w, `{"response":{"id":"si_fixture","status":"needs_first_factor"}}`)
		case "/v1/client/sign_ins/si_fixture/attempt_first_factor":
			_, _ = fmt.Fprint(w, `{"response":{"id":"si_fixture","status":"complete","created_session_id":"sess_fixture"}}`)
		case "/v1/client/sessions/sess_fixture":
			_, _ = fmt.Fprint(w, `{"response":{"id":"sess_fixture","status":"active"}}`)
		case "/v1/client/sessions/sess_fixture/tokens":
			token := f.jwt
			if f.wrongOwner.Load() {
				token = f.secondJWT
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"jwt": token})
		case "/v1/client/sessions/sess_fixture/end":
			_, _ = fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected Clerk path: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	f.env = []string{"HOME=" + f.home, "TTSBUDDY_ACCOUNT_DELETION_URL=" + server + "/v1/delete-account", "TTSBUDDY_CLERK_FRONTEND_API_URL=" + server, "TTSBUDDY_ALLOW_CUSTOM_API_URL=true"}
	return f
}
func readDeletionFixture(t *testing.T, f *deletionFixture) *config.PendingDeletion {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".ttsbuddy", "pending_deletion.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s config.PendingDeletion
	if err = json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}
func writeDeletionFixture(t *testing.T, f *deletionFixture, s *config.PendingDeletion) {
	t.Helper()
	data, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(f.home, ".ttsbuddy", "pending_deletion.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func startDeletionFixture(t *testing.T, f *deletionFixture) string {
	t.Helper()
	r := runCLI(t, f.env, "account", "delete", "start", "--email", deletionTestEmail, "--json")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, `"deleted":false`, "preparation")
	return readDeletionFixture(t, f).ChallengeID
}
func confirmDeletionFixture(t *testing.T, f *deletionFixture, id string) cliResult {
	t.Helper()
	return runCLIInput(t, strings.Repeat("1", 6)+"\n", f.env, "account", "delete", "confirm", "--challenge-id", id, "--confirm-email", deletionTestEmail, "--code-stdin", "--yes", "--json")
}

func TestAccountAndAuthUnknownChildrenExitNonzero(t *testing.T) {
	for _, args := range [][]string{{"auth", "delete", "--json"}, {"account", "unknown", "--json"}, {"account", "delete", "unknown", "--json"}} {
		r := runCLI(t, []string{"HOME=" + t.TempDir()}, args...)
		assertExitCode(t, r, 2)
		assertValidJSON(t, r.Stdout)
		if r.Stderr != "" {
			t.Error("JSON error wrote stderr")
		}
	}
	for _, args := range [][]string{{"auth", "--help"}, {"account", "--help"}, {"account", "delete"}, {"account", "delete", "--help"}} {
		assertExitCode(t, runCLI(t, []string{"HOME=" + t.TempDir()}, args...), 0)
	}
}
func TestDeletionConfirmationGuardsBeforeNetwork(t *testing.T) {
	f := newDeletionFixture(t)
	id := startDeletionFixture(t, f)
	calls := f.clerkCalls.Load()
	for _, flags := range [][]string{{}, {"--yes"}, {"--confirm-email", deletionTestEmail}, {"--yes", "--confirm-email", "other@example.com"}, {"--yes", "--confirm-email", deletionTestEmail}} {
		args := append([]string{"account", "delete", "confirm", "--json", "--challenge-id", id}, flags...)
		r := runCLI(t, f.env, args...)
		assertExitCode(t, r, 2)
	}
	if f.clerkCalls.Load() != calls || f.deleteCalls.Load() != 0 {
		t.Fatal("guard sent a network request")
	}
	r := runCLI(t, f.env, "account", "delete", "start", "--email", "different@example.com", "--json")
	assertExitCode(t, r, 1)
	if f.clerkCalls.Load() != calls {
		t.Fatal("conflicting start sent a request")
	}
}
func TestDeletionFullFlowPreservesOtherCredentialsAndErasesProof(t *testing.T) {
	f := newDeletionFixture(t)
	writeAuthConfig(t, f.home)
	cfgPath := filepath.Join(f.home, ".ttsbuddy", "config.json")
	before, _ := os.ReadFile(cfgPath)
	id := startDeletionFixture(t, f)
	r := confirmDeletionFixture(t, f, id)
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, `"deleted":true`, "completion")
	for _, secret := range []string{f.jwt, "native-client-fixture", strings.Repeat("1", 6), authFixtureToken()} {
		assertNotContains(t, r.Stdout+r.Stderr, secret, "secret redaction")
	}
	state := readDeletionFixture(t, f)
	if state.Proof != "" || state.NativeClientToken != "" || state.SessionID != "" {
		t.Fatal("completed state retained credentials")
	}
	after, _ := os.ReadFile(cfgPath)
	if string(before) != string(after) {
		t.Fatal("deletion modified unrelated config")
	}
	r = runCLI(t, f.env, "account", "delete", "status", "--challenge-id", id, "--json")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "local_completed_receipt", "receipt provenance")
	if f.deleteCalls.Load() != 1 || f.getCalls.Load() != 0 {
		t.Fatal("local completion issued a new network request")
	}
}
func TestDeletionMalformedReplyRecoversViaGetOnly(t *testing.T) {
	f := newDeletionFixture(t)
	f.outcome.Store(1)
	id := startDeletionFixture(t, f)
	r := confirmDeletionFixture(t, f, id)
	assertExitCode(t, r, 1)
	assertContains(t, r.Stdout, `"deleted": false`, "unknown result")
	state := readDeletionFixture(t, f)
	if state.Proof == "" || state.RequestID == "" || !state.Submitted {
		t.Fatal("lost recovery state")
	}
	f.outcome.Store(0)
	r = runCLI(t, f.env, "account", "delete", "status", "--challenge-id", id, "--json")
	assertExitCode(t, r, 0)
	if f.deleteCalls.Load() != 1 || f.getCalls.Load() != 1 {
		t.Fatal("status replayed destructive request")
	}
}
func TestDeletionExplicitRetryPreservesUUIDAndPartialReceipt(t *testing.T) {
	f := newDeletionFixture(t)
	f.outcome.Store(2)
	id := startDeletionFixture(t, f)
	r := confirmDeletionFixture(t, f, id)
	assertExitCode(t, r, 1)
	assertContains(t, r.Stdout, `"partial_cleanup": true`, "partial result")
	assertContains(t, r.Stdout, "PROVIDER_CLEANUP_FAILED", "server error")
	old := readDeletionFixture(t, f)
	calls := f.clerkCalls.Load()
	cancelled := runCLI(t, f.env, "account", "delete", "cancel", "--challenge-id", id, "--json")
	assertExitCode(t, cancelled, 1)
	f.outcome.Store(0)
	r = confirmDeletionFixture(t, f, id)
	assertExitCode(t, r, 0)
	if readDeletionFixture(t, f).RequestID != old.RequestID || f.deleteCalls.Load() != 2 || f.clerkCalls.Load() != calls {
		t.Fatal("explicit retry lost original identity or reauthenticated unnecessarily")
	}
}
func TestDeletionExpiredProofRetainsIDsAndReauthRejectsDifferentOwner(t *testing.T) {
	f := newDeletionFixture(t)
	f.outcome.Store(1)
	id := startDeletionFixture(t, f)
	assertExitCode(t, confirmDeletionFixture(t, f, id), 1)
	state := readDeletionFixture(t, f)
	originalID := state.RequestID
	state.ProofExpiresAt = time.Now().Add(-time.Minute)
	writeDeletionFixture(t, f, state)
	r := runCLI(t, f.env, "account", "delete", "status", "--challenge-id", id, "--json")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stdout, originalID, "recovery request ID")
	assertContains(t, r.Stdout, "PROOF_EXPIRED", "expired state")
	sameID := startDeletionFixture(t, f)
	if sameID != id || readDeletionFixture(t, f).RequestID != originalID {
		t.Fatal("reauth discarded operation identity")
	}
	f.wrongOwner.Store(true)
	r = confirmDeletionFixture(t, f, id)
	assertExitCode(t, r, 1)
	assertContains(t, r.Stdout, "OWNER_MISMATCH", "owner guard")
	if f.deleteCalls.Load() != 1 {
		t.Fatal("changed owner issued another DELETE")
	}
}
func TestDeletionEndpointBindingAndCancelledChallenge(t *testing.T) {
	f := newDeletionFixture(t)
	id := startDeletionFixture(t, f)
	calls := f.clerkCalls.Load()
	env := append(append([]string{}, f.env...), "TTSBUDDY_ACCOUNT_DELETION_URL=http://localhost:9876/v1/delete-account")
	r := runCLI(t, env, "account", "delete", "cancel", "--challenge-id", id, "--json")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stdout, "AUTH_ORIGIN_MISMATCH", "origin guard")
	if f.clerkCalls.Load() != calls {
		t.Fatal("mismatched endpoint caused authentication call")
	}
	r = runCLI(t, f.env, "account", "delete", "cancel", "--challenge-id", id, "--json")
	assertExitCode(t, r, 0)
	if _, err := os.Stat(filepath.Join(f.home, ".ttsbuddy", "pending_deletion.json")); !os.IsNotExist(err) {
		t.Fatal("cancel retained unused challenge")
	}
	if f.deleteCalls.Load() != 0 {
		t.Fatal("cancel submitted deletion")
	}
}

func TestDeletionReadOnlyVerificationBeforeConfirmation(t *testing.T) {
	f := newDeletionFixture(t)
	f.outcome.Store(4)
	id := startDeletionFixture(t, f)
	r := runCLIInput(t, strings.Repeat("1", 6)+"\n", f.env, "account", "delete", "status", "--challenge-id", id, "--code-stdin", "--json")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stdout, `"state": "none"`, "no server receipt")
	state := readDeletionFixture(t, f)
	if state.OwnerID != "user_fixture" || state.Proof == "" || state.Submitted || state.RequestID != "" || f.deleteCalls.Load() != 0 || f.getCalls.Load() != 1 {
		t.Fatal("read-only verification submitted deletion or lost proof")
	}
	f.outcome.Store(0)
	r = confirmDeletionFixture(t, f, id)
	assertExitCode(t, r, 0)
	if f.deleteCalls.Load() != 1 {
		t.Fatal("confirmation did not perform exactly one deletion")
	}
}
func TestDeletionReadOnlyReauthenticationKeepsOriginalOperation(t *testing.T) {
	f := newDeletionFixture(t)
	f.outcome.Store(1)
	id := startDeletionFixture(t, f)
	assertExitCode(t, confirmDeletionFixture(t, f, id), 1)
	state := readDeletionFixture(t, f)
	requestID := state.RequestID
	state.ProofExpiresAt = time.Now().Add(-time.Minute)
	writeDeletionFixture(t, f, state)
	if startDeletionFixture(t, f) != id {
		t.Fatal("reauth changed challenge ID")
	}
	f.outcome.Store(0)
	r := runCLIInput(t, strings.Repeat("1", 6)+"\n", f.env, "account", "delete", "status", "--challenge-id", id, "--code-stdin", "--json")
	assertExitCode(t, r, 0)
	if state = readDeletionFixture(t, f); state.RequestID != requestID || state.OwnerID != "user_fixture" || f.deleteCalls.Load() != 1 || f.getCalls.Load() != 1 {
		t.Fatal("read-only recovery replayed DELETE or changed identity")
	}
}

func TestDeletionCompletedReceiptSurvivesLocalSaveFailure(t *testing.T) {
	f := newDeletionFixture(t)
	f.outcome.Store(5)
	id := startDeletionFixture(t, f)
	r := confirmDeletionFixture(t, f, id)
	assertExitCode(t, r, 1)
	assertContains(t, r.Stdout, `"deleted": true`, "authoritative completion")
	assertContains(t, r.Stdout, `"local_receipt_saved": false`, "persistence failure")
	assertContains(t, r.Stdout, "LOCAL_RECEIPT_SAVE_FAILED", "local cleanup warning")
	assertContains(t, r.Stdout, deletionCorrelation, "retained receipt")
	assertNotContains(t, r.Stdout, f.jwt, "proof redaction")
}

func TestDeletionTextModeRetainsCompletedReceiptOnSaveFailure(t *testing.T) {
	f := newDeletionFixture(t)
	f.outcome.Store(5)
	id := startDeletionFixture(t, f)
	r := runCLIInput(t, strings.Repeat("1", 6)+"\n", f.env, "account", "delete", "confirm", "--challenge-id", id, "--confirm-email", deletionTestEmail, "--code-stdin", "--yes")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stdout, `"deleted": true`, "text completion receipt")
	assertContains(t, r.Stdout, `"local_receipt_saved": false`, "text local persistence failure")
	assertContains(t, r.Stdout, deletionCorrelation, "text correlation ID")
	assertNotContains(t, r.Stdout+r.Stderr, f.jwt, "proof redaction")
}

func TestDeletionStartAfterCompletionBeginsNewAccountLifecycle(t *testing.T) {
	f := newDeletionFixture(t)
	firstID := startDeletionFixture(t, f)
	assertExitCode(t, confirmDeletionFixture(t, f, firstID), 0)
	first := readDeletionFixture(t, f)
	receipt := runCLI(t, f.env, "account", "delete", "status", "--challenge-id", firstID, "--json")
	assertExitCode(t, receipt, 0)
	assertContains(t, receipt.Stdout, "local_completed_receipt", "original terminal receipt")
	// Recreating the same email gives it a different Clerk subject.
	f.wrongOwner.Store(true)
	secondID := startDeletionFixture(t, f)
	fresh := readDeletionFixture(t, f)
	if secondID == firstID || fresh.State != "verification_required" || fresh.Submitted || fresh.OwnerID != "" || fresh.RequestID != "" || fresh.CorrelationID != "" || !fresh.CompletedAt.IsZero() {
		t.Fatal("new lifecycle inherited prior deletion authority or receipt")
	}
	oldStatus := runCLI(t, f.env, "account", "delete", "status", "--challenge-id", firstID, "--json")
	assertExitCode(t, oldStatus, 1)
	assertContains(t, oldStatus.Stdout, "CHALLENGE_NOT_FOUND", "old challenge invalidation")
	assertExitCode(t, confirmDeletionFixture(t, f, secondID), 0)
	second := readDeletionFixture(t, f)
	if second.OwnerID != "user_other" || second.RequestID == "" || second.RequestID == first.RequestID || second.ChallengeID != secondID || f.deleteCalls.Load() != 2 {
		t.Fatal("second lifecycle reused first owner/request or did not submit deletion")
	}
}

func TestDeletionFailedRestartPreservesCompletedReceipt(t *testing.T) {
	for _, failure := range []string{"authentication", "persistence"} {
		t.Run(failure, func(t *testing.T) {
			f := newDeletionFixture(t)
			id := startDeletionFixture(t, f)
			assertExitCode(t, confirmDeletionFixture(t, f, id), 0)
			statePath := filepath.Join(f.home, ".ttsbuddy", "pending_deletion.json")
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "authentication" {
				f.failStart.Store(true)
			} else {
				f.failStartSave.Store(true)
			}
			result := runCLI(t, f.env, "account", "delete", "start", "--email", deletionTestEmail, "--json")
			assertExitCode(t, result, 1)
			if failure == "persistence" {
				if err := os.Chmod(filepath.Join(f.home, ".ttsbuddy"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("failed fresh start overwrote the prior terminal receipt")
			}
			status := runCLI(t, f.env, "account", "delete", "status", "--challenge-id", id, "--json")
			assertExitCode(t, status, 0)
			assertContains(t, status.Stdout, "local_completed_receipt", "preserved terminal receipt")
			if f.deleteCalls.Load() != 1 {
				t.Fatal("fresh start submitted another deletion")
			}
		})
	}
}
