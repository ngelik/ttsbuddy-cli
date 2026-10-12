package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/clerkfapi"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/spf13/cobra"
)

const deletionWarning = "Account deletion permanently removes cloud files, credentials and identity, and cancels subscriptions. Local downloaded files remain."
const deletionRecovery = "Use account delete status with the same challenge ID and config directory. Retry only with account delete confirm and the same confirmation. If proof expired, start again with the same email; if the account is gone, contact support@ttsbuddy.com with the request ID."

func init() {
	group := func(use, short string) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: noArgs, RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
	}
	account := group("account", "Manage your TTS Buddy account")
	deletion := group("delete", "Permanently delete an account using fresh email verification")
	deletion.Long = deletionWarning + "\n\nStart with: account delete start --email <address> --json\nThen: account delete confirm --challenge-id <id> --confirm-email <address> --code-stdin --yes --json\nSpeech keys and saved CLI sessions do not authorize deletion."
	start := &cobra.Command{Use: "start", Short: "Prepare fresh email verification for deletion", Args: noArgs, RunE: runDeletionStart}
	start.Flags().String("email", "", "existing account email address")
	confirm := &cobra.Command{Use: "confirm", Short: "Verify and explicitly submit or retry permanent deletion", Long: deletionWarning, Args: rejectPositionalAuthCode, RunE: runDeletionConfirm}
	confirm.Flags().String("confirm-email", "", "exact email address used by start")
	confirm.Flags().Bool("yes", false, "acknowledge permanent account deletion and subscription cancellation")
	confirm.Flags().Bool("code-stdin", false, "read the six-digit verification code from stdin")
	confirm.Flags().Bool("apple-manual-fallback-confirmed", false, "confirm you will manually revoke Sign in with Apple if automatic revocation is unavailable")
	status := &cobra.Command{Use: "status", Short: "Read deletion status without submitting deletion", Args: noArgs, RunE: runDeletionStatus}
	status.Flags().Bool("code-stdin", false, "verify a fresh prepared code through stdin before GET-only status recovery")
	cancel := &cobra.Command{Use: "cancel", Short: "Revoke and remove an unused deletion challenge", Args: noArgs, RunE: runDeletionCancel}
	for _, c := range []*cobra.Command{confirm, status, cancel} {
		c.Flags().String("challenge-id", "", "challenge ID returned by account delete start")
	}
	deletion.AddCommand(start, confirm, status, cancel)
	account.AddCommand(deletion)
	rootCmd.AddCommand(account)
}

func deletionError(code int, reason, message string) *exitError {
	e := structuredExitError(code, message, "ACCOUNT_DELETION_ERROR", reason, deletionRecovery, false, 0)
	return e
}

func deletionSettings(c *cobra.Command) (string, string, error) {
	if c.Flags().Changed("key") {
		return "", "", deletionError(2, "INVALID_ARGUMENT", "--key cannot authorize account deletion")
	}
	if resolvedCfg == nil {
		return "", "", deletionError(1, "INVALID_CONFIGURATION", "configuration unavailable")
	}
	if err := checkEmailConfigDirectory(); err != nil {
		return "", "", err
	}
	endpoint := strings.TrimSpace(os.Getenv("TTSBUDDY_ACCOUNT_DELETION_URL"))
	if endpoint == "" {
		endpoint = api.DefaultAccountDeletionURL
	}
	if err := api.ValidateAccountDeletionURL(endpoint, resolvedCfg.AllowCustomAPIURL); err != nil {
		return "", "", deletionError(1, "INVALID_CONFIGURATION", "account deletion endpoint is invalid or untrusted")
	}
	if err := config.CheckCredentialedAPIURL(resolvedCfg.ClerkFrontendAPIURL, resolvedCfg.AllowCustomAPIURL); err != nil {
		return "", "", deletionError(1, "INVALID_CONFIGURATION", "authentication endpoint is invalid or untrusted")
	}
	origin, err := authOrigin(resolvedCfg.ClerkFrontendAPIURL)
	if err != nil {
		return "", "", deletionError(1, "INVALID_CONFIGURATION", "authentication origin is invalid")
	}
	return origin, endpoint, nil
}

func deletionLock() (*config.LoginLock, error) {
	l, err := config.AcquireLoginLock()
	if err != nil {
		return nil, deletionError(1, "AUTH_IN_PROGRESS", "another authentication operation is running")
	}
	return l, nil
}
func loadDeletion(id, origin, endpoint string) (*config.PendingDeletion, error) {
	s, err := config.LoadPendingDeletion()
	if err != nil {
		return nil, deletionError(1, "PENDING_STATE_INVALID", "protected deletion state could not be read")
	}
	if s == nil || s.ChallengeID != id {
		return nil, deletionError(1, "CHALLENGE_NOT_FOUND", "deletion challenge was not found in this config directory")
	}
	if s.ClerkOrigin != origin || s.Endpoint != endpoint {
		return nil, deletionError(1, "AUTH_ORIGIN_MISMATCH", "restore the exact original Clerk origin and deletion endpoint")
	}
	return s, nil
}
func saveDeletion(s *config.PendingDeletion) error {
	if config.SavePendingDeletion(*s) != nil {
		return deletionError(1, "PENDING_STATE_UNAVAILABLE", "could not securely persist deletion recovery state; inspect the saved request ID and contact support if a request was submitted")
	}
	return nil
}
func deletionID(c *cobra.Command) (string, error) {
	id, _ := c.Flags().GetString("challenge-id")
	if !config.IsPendingAuthID(id) {
		return "", deletionError(2, "INVALID_ARGUMENT", "--challenge-id must be the ID returned by account delete start")
	}
	return id, nil
}

func deletionResult(s *config.PendingDeletion, source string) map[string]any {
	result := map[string]any{"success": s.State == "completed", "deleted": s.State == "completed", "state": s.State, "challenge_id": s.ChallengeID, "source": source, "request_id": s.RequestID, "correlation_id": s.CorrelationID}
	if s.State == "completed" {
		result["apple_authorization"] = s.AppleAuthorization
		result["verified_at"] = s.CompletedAt.UTC().Format(time.RFC3339)
		result["local_credentials"] = "preserved; ownership is not recorded"
	} else {
		result["next_action"] = deletionRecovery
	}
	return result
}
func outputDeletion(result map[string]any) error {
	if flagJSON {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	_, err := fmt.Fprintln(os.Stdout, string(data))
	return err
}
func renderDeletionChallenge(s *config.PendingDeletion) error {
	result := deletionResult(s, "local_continuation")
	result["success"] = true
	result["deleted"] = false
	result["expires_at"] = s.ExpiresAt.UTC().Format(time.RFC3339)
	result["expiry_scope"] = "cli_continuation_deadline"
	result["warning"] = deletionWarning
	result["next_action"] = fmt.Sprintf("ttsbuddy account delete confirm --challenge-id %s --confirm-email <exact-prepared-email> --code-stdin --yes --json", s.ChallengeID)
	return outputDeletion(result)
}

func runDeletionStart(c *cobra.Command, _ []string) error {
	email, _ := c.Flags().GetString("email")
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return deletionError(2, "INVALID_ARGUMENT", "--email must be an existing account email address")
	}
	origin, endpoint, err := deletionSettings(c)
	if err != nil {
		return err
	}
	lock, err := deletionLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	old, err := config.LoadPendingDeletion()
	if err != nil {
		return deletionError(1, "PENDING_STATE_INVALID", "protected deletion state could not be read")
	}
	if old != nil {
		if old.Email != email || old.ClerkOrigin != origin || old.Endpoint != endpoint {
			return deletionError(1, "CHALLENGE_CONFLICT", "existing deletion recovery state belongs to a different email or endpoint; use its original configuration")
		}
		if old.State == "completed" {
			// Explicit start begins a new lifecycle, even when the email is
			// reused by a recreated account. Keep the terminal receipt on disk
			// until the fresh challenge has been prepared and saved safely.
			old = nil
		} else if old.ExpiresAt.After(time.Now()) && (old.Proof == "" || old.ProofExpiresAt.After(time.Now())) {
			return renderDeletionChallenge(old)
		}
	}
	clerk, err := clerkfapi.New(resolvedCfg.ClerkFrontendAPIURL, Version)
	if err != nil {
		return deletionError(1, "INVALID_CONFIGURATION", "authentication is unavailable")
	}
	preserved := false
	defer func() {
		if preserved {
			clerk.Close()
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_ = clerk.Cleanup(ctx)
		}
	}()
	// Expired native clients are revoked before replacement; submitted request identity is retained.
	if old != nil && old.NativeClientToken != "" {
		previous, e := clerkfapi.New(resolvedCfg.ClerkFrontendAPIURL, Version)
		if e == nil {
			if previous.RestoreNativeClientToken(old.NativeClientToken) == nil {
				if previous.Cleanup(c.Context()) != nil {
					return deletionError(1, "AUTH_CLEANUP_FAILED", "could not revoke expired temporary authentication; recovery state retained")
				}
			}
			previous.Close()
		}
	}
	challenge, err := clerk.StartEmailCode(c.Context(), email)
	if err != nil {
		return deletionError(1, "AUTH_FAILED", "could not prepare existing-account email verification; no account was created")
	}
	token, err := clerk.ExportNativeClientToken()
	if err != nil {
		return deletionError(1, "AUTH_FAILED", "authentication continuation unavailable")
	}
	id, err := config.NewPendingAuthID()
	if err != nil {
		return deletionError(1, "AUTH_FAILED", "could not prepare challenge ID")
	}
	now := time.Now().UTC()
	s := &config.PendingDeletion{Version: config.PendingDeletionVersion, ChallengeID: id, Email: email, CreatedAt: now, ExpiresAt: now.Add(config.PendingAuthTTL), ClerkOrigin: origin, Endpoint: endpoint, SignInID: challenge.SignInID, EmailAddressID: challenge.EmailAddressID, NativeClientToken: token, State: "verification_required"}
	if old != nil && old.Submitted {
		s.ChallengeID = old.ChallengeID
		s.Submitted = true
		s.OwnerID = old.OwnerID
		s.RequestID = old.RequestID
		s.CorrelationID = old.CorrelationID
	}
	if err = saveDeletion(s); err != nil {
		return err
	}
	preserved = true
	return renderDeletionChallenge(s)
}

// The unsigned parse only binds continuation identity and expiry. The service
// verifies the JWT signature and claims before granting any deletion authority.
func deletionProofIdentity(token string) (string, time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 8192 {
		return "", time.Time{}, errors.New("invalid session proof")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", time.Time{}, errors.New("invalid session proof")
	}
	var claims struct {
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || !regexp.MustCompile(`^[A-Za-z0-9_:-]{1,128}$`).MatchString(claims.Sub) || claims.Exp <= time.Now().Unix() {
		return "", time.Time{}, errors.New("invalid session proof")
	}
	return claims.Sub, time.Unix(claims.Exp, 0).UTC(), nil
}

func runDeletionConfirm(c *cobra.Command, _ []string) error {
	yes, _ := c.Flags().GetBool("yes")
	email, _ := c.Flags().GetString("confirm-email")
	if !yes || email == "" {
		return deletionError(2, "CONFIRMATION_REQUIRED", "--yes and --confirm-email are required. "+deletionWarning)
	}
	id, err := deletionID(c)
	if err != nil {
		return err
	}
	origin, endpoint, err := deletionSettings(c)
	if err != nil {
		return err
	}
	lock, err := deletionLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	s, err := loadDeletion(id, origin, endpoint)
	if err != nil {
		return err
	}
	if email != s.Email {
		return deletionError(2, "EMAIL_MISMATCH", "--confirm-email must exactly match the prepared email address")
	}
	if s.State == "completed" {
		return outputDeletion(deletionResult(s, "local_completed_receipt"))
	}
	if s.Proof == "" {
		if err := verifyDeletionProof(c, s); err != nil {
			return err
		}
	}
	if !s.ProofExpiresAt.After(time.Now()) {
		return expiredDeletionProof(s)
	}
	if s.RequestID == "" {
		s.RequestID = api.GenerateNew()
	}
	s.Submitted = true
	s.State = "unknown"
	if err = saveDeletion(s); err != nil {
		return err
	}
	client, err := api.NewAccountDeletionClient(endpoint, s.Proof, Version, resolvedCfg.AllowCustomAPIURL)
	if err != nil {
		return deletionError(1, "INVALID_CONFIGURATION", "deletion client is unavailable")
	}
	fallback, _ := c.Flags().GetBool("apple-manual-fallback-confirmed")
	response, httpStatus, requestErr := client.Delete(c.Context(), s.RequestID, fallback)
	return finishDeletion(s, response, httpStatus, requestErr)
}

func finishDeletion(s *config.PendingDeletion, r *api.AccountDeletionResponse, httpStatus int, requestErr error) error {
	if r == nil {
		e := deletionError(1, "DELETION_OUTCOME_UNKNOWN", "deletion outcome could not be verified; recovery state retained")
		result := deletionResult(s, "unverified_response")
		result["error_code"] = "DELETION_OUTCOME_UNKNOWN"
		return deletionResultError(e, result)
	}
	s.State = r.State
	if s.State == "" {
		s.State = "unknown"
	}
	if r.CorrelationID != "" {
		s.CorrelationID = r.CorrelationID
	}
	if s.State == "completed" && r.Success && requestErr == nil {
		s.AppleAuthorization = r.AppleAuthorization
		s.CompletedAt = time.Now().UTC()
		s.Proof = ""
		s.NativeClientToken = ""
		s.SessionID = ""
	}
	saveErr := saveDeletion(s)
	if saveErr != nil && s.State != "completed" {
		return saveErr
	}
	result := deletionResult(s, "server_response")
	result["local_receipt_saved"] = saveErr == nil
	if saveErr != nil {
		result["warning"] = "Server completion was verified but the local receipt could not be saved; retain this receipt and remove temporary state from the private config directory."
	}
	result["http_status"] = httpStatus
	result["retryable"] = r.Retryable
	result["partial_cleanup"] = r.PartialCleanup
	if r.ErrorCode != "" {
		result["error_code"] = r.ErrorCode
	}
	if r.ErrorCategory != "" {
		result["error_category"] = r.ErrorCategory
	}
	if r.PendingCount != nil {
		result["pending_count"] = r.PendingCount
	}
	if r.RetryAfterSeconds != nil {
		result["retry_after_seconds"] = r.RetryAfterSeconds
	}
	if s.State == "completed" {
		if saveErr != nil {
			e := deletionError(1, "LOCAL_RECEIPT_SAVE_FAILED", "server deletion completed but the local receipt could not be saved; retain the receipt and remove temporary authentication state")
			result["error_code"] = "LOCAL_RECEIPT_SAVE_FAILED"
			return deletionResultError(e, result)
		}
		return outputDeletion(result)
	}
	e := deletionError(1, "DELETION_INCOMPLETE", "account deletion is incomplete; recovery state retained")
	return deletionResultError(e, result)
}

func runDeletionStatus(c *cobra.Command, _ []string) error {
	id, err := deletionID(c)
	if err != nil {
		return err
	}
	origin, endpoint, err := deletionSettings(c)
	if err != nil {
		return err
	}
	lock, err := deletionLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	s, err := loadDeletion(id, origin, endpoint)
	if err != nil {
		return err
	}
	if s.State == "completed" {
		return outputDeletion(deletionResult(s, "local_completed_receipt"))
	}
	if s.Proof == "" {
		readCode, _ := c.Flags().GetBool("code-stdin")
		if readCode {
			if err := verifyDeletionProof(c, s); err != nil {
				return err
			}
		}
	}
	if s.Proof == "" {
		e := deletionError(1, "VERIFICATION_REQUIRED", "email verification required; deletion has not been confirmed")
		return deletionResultError(e, deletionResult(s, "local_continuation"))
	}
	if !s.ProofExpiresAt.After(time.Now()) {
		return expiredDeletionProof(s)
	}
	client, err := api.NewAccountDeletionClient(endpoint, s.Proof, Version, resolvedCfg.AllowCustomAPIURL)
	if err != nil {
		return deletionError(1, "INVALID_CONFIGURATION", "deletion client is unavailable")
	}
	response, httpStatus, requestErr := client.Status(c.Context())
	return finishDeletion(s, response, httpStatus, requestErr)
}

func runDeletionCancel(c *cobra.Command, _ []string) error {
	id, err := deletionID(c)
	if err != nil {
		return err
	}
	origin, endpoint, err := deletionSettings(c)
	if err != nil {
		return err
	}
	lock, err := deletionLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	s, err := loadDeletion(id, origin, endpoint)
	if err != nil {
		return err
	}
	if s.Submitted || s.State == "completed" {
		return deletionError(1, "ALREADY_SUBMITTED", "a submitted deletion cannot be cancelled; check status")
	}
	clerk, err := clerkfapi.New(resolvedCfg.ClerkFrontendAPIURL, Version)
	if err != nil {
		return deletionError(1, "AUTH_FAILED", "authentication cleanup unavailable")
	}
	defer clerk.Close()
	if clerk.RestoreNativeClientToken(s.NativeClientToken) != nil || clerk.Cleanup(c.Context()) != nil {
		return deletionError(1, "AUTH_CLEANUP_FAILED", "temporary authentication cleanup could not be confirmed; state retained")
	}
	if config.ClearPendingDeletion(id) != nil {
		return deletionError(1, "PENDING_STATE_UNAVAILABLE", "could not remove cancelled challenge")
	}
	return outputDeletion(map[string]any{"success": true, "deleted": false, "state": "cancelled", "challenge_id": id})
}

func expiredDeletionProof(s *config.PendingDeletion) error {
	e := deletionError(1, "PROOF_EXPIRED", "deletion outcome remains unresolved because temporary proof expired; reauthenticate with the same email or contact support with the saved request ID")
	result := deletionResult(s, "local_continuation")
	result["error_code"] = "PROOF_EXPIRED"
	return deletionResultError(e, result)
}

func verifyDeletionProof(c *cobra.Command, s *config.PendingDeletion) error {

	if !s.ExpiresAt.After(time.Now()) {
		return deletionError(1, "CHALLENGE_EXPIRED", "email challenge expired; run start with the same email")
	}
	stdin, _ := c.Flags().GetBool("code-stdin")
	if !stdin {
		return deletionError(2, "INVALID_CODE", "--code-stdin is required; verification codes must not be command arguments")
	}
	data, e := io.ReadAll(io.LimitReader(c.InOrStdin(), 9))
	code := strings.TrimSpace(string(data))
	if e != nil || len(data) > 8 || !regexp.MustCompile(`^[0-9]{6}$`).MatchString(code) {
		return deletionError(2, "INVALID_CODE", "provide exactly six digits through stdin")
	}
	clerk, e := clerkfapi.New(resolvedCfg.ClerkFrontendAPIURL, Version)
	if e != nil {
		return deletionError(1, "AUTH_FAILED", "authentication is unavailable")
	}
	preserved := false
	defer func() {
		if preserved {
			clerk.Close()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = clerk.Cleanup(ctx)
	}()
	if clerk.RestoreNativeClientToken(s.NativeClientToken) != nil {
		return deletionError(1, "PENDING_STATE_INVALID", "authentication continuation is invalid")
	}
	proof, verifyErr := clerk.VerifyEmailCode(c.Context(), clerkfapi.Challenge{SignInID: s.SignInID, EmailAddressID: s.EmailAddressID}, code)
	if rotated, exportErr := clerk.ExportNativeClientToken(); exportErr == nil {
		s.NativeClientToken = rotated
	}
	if verifyErr != nil {
		if err := saveDeletion(s); err != nil {
			return err
		}
		preserved = true
		return deletionError(1, "AUTH_FAILED", "email verification failed; deletion was not submitted")
	}
	owner, expiry, e := deletionProofIdentity(proof.Token)
	if e != nil || (s.OwnerID != "" && owner != s.OwnerID) {
		return deletionError(1, "OWNER_MISMATCH", "session proof does not match the original deletion account")
	}
	s.OwnerID = owner
	s.Proof = proof.Token
	s.ProofExpiresAt = expiry
	s.SessionID = proof.SessionID
	s.State = "ready"
	if err := saveDeletion(s); err != nil {
		return err
	}
	preserved = true
	return nil
}

// Keep public recovery receipts visible in both output modes, including failures.
func deletionResultError(e *exitError, result map[string]any) *exitError {
	e.jsonPayload = result
	if !flagJSON {
		_ = outputDeletion(result)
	}
	return e
}
