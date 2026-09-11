package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/clerkfapi"
	"github.com/ngelik/ttsbuddy-cli/internal/clerkoauth"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/ngelik/ttsbuddy-cli/internal/prompt"
	"github.com/spf13/cobra"
)

var authLocalOnly bool

var (
	authEmailStartAddress string
	authEmailStartSignup  bool
	authEmailVerifyID     string
	authEmailVerifyStdin  bool
	authEmailCancelID     string
)

const (
	signupEmailAddressBlockedMessage = "This email address is not allowed for signup. Use a different, non-disposable email address and run: ttsbuddy auth email --signup"
)

// ClerkOAuthClientID is public configuration. Keeping the production client ID
// as the source default makes browser authentication work for `go install`
// builds, while release builds can still inject and verify the same value.
var ClerkOAuthClientID = "gRApqxGCscvVfceh"

// ClerkOAuthIssuer is public configuration and may be overridden for local
// development only when custom API URLs have been explicitly enabled.
var ClerkOAuthIssuer = config.DefaultClerkFAPIURL

var runBrowserOAuth = func(ctx context.Context, issuer, clientID string, allowCustom bool, output io.Writer) (string, error) {
	client, err := clerkoauth.New(clerkoauth.Config{IssuerURL: issuer, ClientID: clientID, AllowCustomIssuer: allowCustom, Output: output})
	if err != nil {
		return "", err
	}
	return client.Run(ctx)
}

var authCmd = &cobra.Command{Use: "auth", Short: "Sign in and manage the CLI session", Args: noArgs}
var authLoginCmd = &cobra.Command{Use: "login", Short: "Sign in with an email code (or create an account with --signup)", Args: noArgs, RunE: runAuthLogin}
var authEmailCmd = &cobra.Command{Use: "email", Short: "Sign in with an email code (or create an account with --signup)", Args: noArgs, RunE: runAuthLogin}
var authEmailStartCmd = &cobra.Command{Use: "start", Short: "Start a machine-readable email authentication challenge", Args: noArgs, RunE: runAuthEmailStart}
var authEmailVerifyCmd = &cobra.Command{Use: "verify", Short: "Verify a prepared email authentication challenge", Args: rejectPositionalAuthCode, RunE: runAuthEmailVerify}
var authEmailCancelCmd = &cobra.Command{Use: "cancel", Short: "Cancel a prepared email authentication challenge", Args: noArgs, RunE: runAuthEmailCancel}
var authBrowserCmd = &cobra.Command{Use: "browser", Short: "Sign in with a browser", Args: noArgs, RunE: runAuthBrowser}
var authStatusCmd = &cobra.Command{Use: "status", Short: "Show CLI session status", Args: noArgs, RunE: runAuthStatus}
var authLogoutCmd = &cobra.Command{Use: "logout", Short: "Sign out the CLI session", Args: noArgs, RunE: runAuthLogout}

func init() {
	authLogoutCmd.Flags().BoolVar(&authLocalOnly, "local-only", false, "remove local session without server revocation")
	authLoginCmd.Flags().Bool("signup", false, "create a new account instead of signing in")
	authEmailCmd.Flags().Bool("signup", false, "create a new account instead of signing in")
	authEmailStartCmd.Flags().StringVar(&authEmailStartAddress, "email", "", "email address to use for the challenge")
	authEmailStartCmd.Flags().BoolVar(&authEmailStartSignup, "signup", false, "create a new account instead of signing in")
	authEmailVerifyCmd.Flags().StringVar(&authEmailVerifyID, "challenge-id", "", "opaque challenge ID returned by auth email start")
	authEmailVerifyCmd.Flags().BoolVar(&authEmailVerifyStdin, "code-stdin", false, "read the six-digit verification code from stdin")
	authEmailCancelCmd.Flags().StringVar(&authEmailCancelID, "challenge-id", "", "opaque challenge ID returned by auth email start")
	authEmailCmd.AddCommand(authEmailStartCmd, authEmailVerifyCmd, authEmailCancelCmd)
	authCmd.AddCommand(authLoginCmd, authEmailCmd, authBrowserCmd, authStatusCmd, authLogoutCmd)
	rootCmd.AddCommand(authCmd)
}

func rejectAuthGlobalCredentialFlags(cmd *cobra.Command, login bool) error {
	if cmd.Flags().Changed("key") {
		return &exitError{code: 2, msg: "--key is not supported by auth commands"}
	}
	if login && flagJSON {
		return &exitError{code: 2, msg: "--json is not supported by auth login"}
	}
	return nil
}

func rejectPositionalAuthCode(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return structuredExitError(2, "verification codes cannot be command-line arguments; use --code-stdin for automation or the interactive prompt", "CLI_ERROR", "INVALID_ARGUMENT", "Provide the six-digit code through --code-stdin.", false, 0)
	}
	return nil
}

func validateAuthURL(resolved *config.ResolvedConfig) error {
	if resolved == nil {
		return errors.New("config not loaded")
	}
	return config.CheckCredentialedAPIURL(resolved.CLIAuthURL, resolved.AllowCustomAPIURL)
}

func runAuthLogin(cmd *cobra.Command, _ []string) error {
	if err := rejectAuthGlobalCredentialFlags(cmd, true); err != nil {
		return err
	}
	signup, _ := cmd.Flags().GetBool("signup")
	if err := validateAuthURL(resolvedCfg); err != nil {
		return err
	}
	if err := config.CheckCredentialedAPIURL(resolvedCfg.ClerkFrontendAPIURL, resolvedCfg.AllowCustomAPIURL); err != nil {
		return err
	}
	lock, err := config.AcquireLoginLock()
	if err != nil {
		return &exitError{code: 1, msg: err.Error()}
	}
	defer func() { _ = lock.Release() }()

	fmt.Fprintln(os.Stderr, "Signing in successfully will sign out any existing CLI session on another machine.")
	if signup {
		fmt.Fprintln(os.Stderr, "Creating a TTS Buddy account using email verification.")
	}
	p := prompt.New(cmd.InOrStdin(), cmd.ErrOrStderr())
	email, err := p.RequiredLine("Email: ", 254)
	if err != nil {
		return &exitError{code: 2, msg: err.Error()}
	}
	clerk, err := clerkfapi.New(resolvedCfg.ClerkFrontendAPIURL, Version)
	if err != nil {
		return err
	}
	exchanged := false
	defer func() {
		if !shouldAttemptClerkCleanup(exchanged) {
			clerk.Close()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cleanupErr := clerk.Cleanup(ctx)
		if cleanupErr != nil {
			fmt.Fprintln(os.Stderr, "Warning: temporary authentication cleanup could not be confirmed.")
		}
	}()
	ctx := cmd.Context()
	var signUpChallenge *clerkfapi.SignUpChallenge
	var signInChallenge *clerkfapi.Challenge
	if signup {
		started, startErr := clerk.StartEmailSignUp(ctx, email)
		if startErr != nil {
			return classifyClerkAuthError(startErr, true)
		}
		fmt.Fprintln(os.Stderr, "If this is a new eligible address, check your email for a verification code.")
		fmt.Fprintln(os.Stderr, "Already registered? "+authMethodSuggestion)
		signUpChallenge = started
	} else {
		started, startErr := clerk.StartEmailCode(ctx, email)
		if startErr != nil {
			return classifyClerkAuthError(startErr, false)
		}
		fmt.Fprintln(os.Stderr, "If this address belongs to an eligible TTS Buddy account, check your email for a code.")
		signInChallenge = started
	}
	code, err := p.Secret("Code: ", 6)
	if err != nil {
		return &exitError{code: 2, msg: err.Error()}
	}
	if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(code) {
		return &exitError{code: 2, msg: "code must be exactly six digits"}
	}
	var proof *clerkfapi.SessionProof
	if signup {
		proof, err = clerk.VerifyEmailSignUp(ctx, *signUpChallenge, code)
	} else {
		proof, err = clerk.VerifyEmailCode(ctx, *signInChallenge, code)
	}
	if err != nil {
		return classifyClerkAuthError(err, signup)
	}
	exchanged, err = exchangeAndStoreCLISession(ctx, proof.Token, false)
	return err
}

const pendingAuthHandoff = "Successful sign-in replaces any existing CLI session. A human/mailbox owner must authorize this sign-in and provide the six-digit code, or use explicitly authorized mailbox access."

func authOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("authentication origin is invalid")
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return "", errors.New("authentication origin is invalid")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

func validateStructuredAuthConfig() (clerkOrigin, apiOrigin string, err error) {
	if err := validateAuthURL(resolvedCfg); err != nil {
		return "", "", err
	}
	if _, err := api.NewCLIAuthClient(resolvedCfg.CLIAuthURL, "", Version, resolvedCfg.AllowCustomAPIURL); err != nil {
		return "", "", err
	}
	if err := config.CheckCredentialedAPIURL(resolvedCfg.ClerkFrontendAPIURL, resolvedCfg.AllowCustomAPIURL); err != nil {
		return "", "", err
	}
	clerkOrigin, err = authOrigin(resolvedCfg.ClerkFrontendAPIURL)
	if err != nil {
		return "", "", err
	}
	apiOrigin, err = authOrigin(resolvedCfg.CLIAuthURL)
	if err != nil {
		return "", "", err
	}
	return clerkOrigin, apiOrigin, nil
}

func pendingChallengeResult(state *config.PendingAuth) map[string]any {
	return map[string]any{
		"success":                     true,
		"status":                      "verification_required",
		"challenge_id":                state.ChallengeID,
		"mode":                        state.Mode,
		"requires_email_verification": true,
		"human_action_required":       false,
		"next_action":                 fmt.Sprintf("Provide the six-digit code from the authorized mailbox via: ttsbuddy auth email verify --challenge-id %s --code-stdin --json", state.ChallengeID),
		"expires_at":                  state.ExpiresAt.UTC().Format(time.RFC3339),
		"expiry_scope":                "cli_continuation_deadline",
		"handoff":                     pendingAuthHandoff,
	}
}

func renderPendingChallenge(state *config.PendingAuth) error {
	if flagJSON {
		return json.NewEncoder(os.Stdout).Encode(pendingChallengeResult(state))
	}
	fmt.Fprintln(os.Stderr, "Email authentication challenge prepared.")
	fmt.Fprintln(os.Stderr, pendingAuthHandoff)
	fmt.Fprintf(os.Stderr, "The CLI continuation expires at %s (this is not a claimed provider OTP lifetime).\n", state.ExpiresAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "Continue with: ttsbuddy auth email verify --challenge-id %s\n", state.ChallengeID)
	return nil
}

func runAuthEmailStart(cmd *cobra.Command, _ []string) error {
	if err := rejectAuthGlobalCredentialFlags(cmd, false); err != nil {
		return err
	}
	if strings.TrimSpace(authEmailStartAddress) == "" {
		return structuredExitError(2, "--email is required", "CLI_ERROR", "INVALID_ARGUMENT", "ttsbuddy auth email start --email <address> --json", false, 0)
	}
	clerkOrigin, apiOrigin, err := validateStructuredAuthConfig()
	if err != nil {
		return structuredExitError(1, "authentication configuration is invalid", "CLI_ERROR", "INVALID_CONFIGURATION", "Check the CLI endpoint configuration.", false, 0)
	}
	lock, err := config.AcquireLoginLock()
	if err != nil {
		return structuredExitError(1, err.Error(), "CLI_ERROR", "AUTH_IN_PROGRESS", "Wait for the other authentication process to finish.", true, 0)
	}
	defer func() { _ = lock.Release() }()
	if pending, loadErr := config.LoadPendingAuth(); loadErr != nil {
		return structuredExitError(1, "pending authentication state could not be read", "CLI_ERROR", "PENDING_STATE_INVALID", "Run: ttsbuddy auth email cancel --challenge-id <id> --json", false, 0)
	} else if pending != nil {
		if pending.ExpiresAt.After(time.Now().UTC()) {
			if pending.ClerkOrigin != clerkOrigin || pending.APIOrigin != apiOrigin {
				return structuredExitError(1, "a pending challenge belongs to a different authentication origin", "CLI_ERROR", "AUTH_ORIGIN_MISMATCH", "Restore the original endpoint configuration or cancel the pending challenge.", false, 0)
			}
			return renderPendingChallenge(pending)
		}
		_ = config.ClearPendingAuth(pending.ChallengeID)
	}
	clerk, err := clerkfapi.New(resolvedCfg.ClerkFrontendAPIURL, Version)
	if err != nil {
		return structuredExitError(1, "email authentication is unavailable", "AUTH_ERROR", "INVALID_CONFIGURATION", "Check the Clerk endpoint configuration.", false, 0)
	}
	preserved := false
	defer func() {
		if !preserved {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			_ = clerk.Cleanup(ctx)
			cancel()
		}
	}()
	ctx := cmd.Context()
	var challengeID, signInID, emailAddressID, signUpID string
	mode := "login"
	if authEmailStartSignup {
		mode = "signup"
		challenge, startErr := clerk.StartEmailSignUp(ctx, authEmailStartAddress)
		if startErr != nil {
			return classifyClerkAuthError(startErr, true)
		}
		signUpID = challenge.SignUpID
		challengeID, err = config.NewPendingAuthID()
	} else {
		challenge, startErr := clerk.StartEmailCode(ctx, authEmailStartAddress)
		if startErr != nil {
			return classifyClerkAuthError(startErr, false)
		}
		signInID, emailAddressID = challenge.SignInID, challenge.EmailAddressID
		challengeID, err = config.NewPendingAuthID()
	}
	if err != nil {
		return structuredExitError(1, "could not prepare authentication challenge", "AUTH_ERROR", "AUTH_FAILED", authMethodSuggestion, true, 0)
	}
	nativeToken, err := clerk.ExportNativeClientToken()
	if err != nil {
		return structuredExitError(1, "could not save authentication continuation", "AUTH_ERROR", "AUTH_STATE_UNAVAILABLE", "Run the browser authentication flow instead.", false, 0)
	}
	now := time.Now().UTC()
	state := config.PendingAuth{Version: config.PendingAuthVersion, ChallengeID: challengeID, Mode: mode, CreatedAt: now, ExpiresAt: now.Add(config.PendingAuthTTL), ClerkOrigin: clerkOrigin, APIOrigin: apiOrigin, SignInID: signInID, EmailAddressID: emailAddressID, SignUpID: signUpID, NativeClientToken: nativeToken}
	if err := config.SavePendingAuth(state); err != nil {
		return structuredExitError(1, "could not save authentication continuation", "AUTH_ERROR", "AUTH_STATE_UNAVAILABLE", "Run the browser authentication flow instead.", false, 0)
	}
	preserved = true
	clerk.Close()
	return renderPendingChallenge(&state)
}

func readVerificationCode(cmd *cobra.Command) (string, error) {
	if authEmailVerifyStdin {
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 9))
		if err != nil {
			return "", errors.New("unable to read verification code")
		}
		if len(data) > 8 {
			return "", errors.New("code input is too long")
		}
		code := strings.TrimSpace(string(data))
		if len(code) != 6 || !regexp.MustCompile(`^[0-9]{6}$`).MatchString(code) {
			return "", errors.New("code must be exactly six digits")
		}
		return code, nil
	}
	if flagJSON {
		return "", errors.New("--code-stdin is required with --json")
	}
	code, err := prompt.New(cmd.InOrStdin(), cmd.ErrOrStderr()).Secret("Code: ", 6)
	if err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(code) {
		return "", errors.New("code must be exactly six digits")
	}
	return code, nil
}

func runAuthEmailVerify(cmd *cobra.Command, _ []string) error {
	if err := rejectAuthGlobalCredentialFlags(cmd, false); err != nil {
		return err
	}
	if !config.IsPendingAuthID(authEmailVerifyID) {
		return structuredExitError(2, "--challenge-id must be the opaque ID returned by auth email start", "CLI_ERROR", "INVALID_ARGUMENT", "Start a new challenge with auth email start.", false, 0)
	}
	lock, err := config.AcquireLoginLock()
	if err != nil {
		return structuredExitError(1, err.Error(), "CLI_ERROR", "AUTH_IN_PROGRESS", "Wait for the other authentication process to finish.", true, 0)
	}
	defer func() { _ = lock.Release() }()
	clerkOrigin, apiOrigin, err := validateStructuredAuthConfig()
	if err != nil {
		return structuredExitError(1, "authentication configuration is invalid", "CLI_ERROR", "INVALID_CONFIGURATION", "Check the CLI endpoint configuration.", false, 0)
	}
	state, err := config.LoadPendingAuth()
	if err != nil {
		return structuredExitError(1, "pending authentication state could not be read", "CLI_ERROR", "PENDING_STATE_INVALID", "Cancel the pending challenge and start again.", false, 0)
	}
	if state == nil {
		return structuredExitError(1, "no pending authentication challenge", "CLI_ERROR", "NO_PENDING_CHALLENGE", "Start one with: ttsbuddy auth email start --email <address> --json", false, 0)
	}
	if state.ChallengeID != authEmailVerifyID {
		return structuredExitError(1, "challenge ID does not match the pending authentication", "CLI_ERROR", "CHALLENGE_MISMATCH", "Use the challenge ID returned by auth email start.", false, 0)
	}
	if state.ClerkOrigin != clerkOrigin || state.APIOrigin != apiOrigin {
		return structuredExitError(1, "authentication endpoint configuration changed since the challenge started", "CLI_ERROR", "AUTH_ORIGIN_MISMATCH", "Restore the original endpoint configuration or cancel the pending challenge.", false, 0)
	}
	if !state.ExpiresAt.After(time.Now().UTC()) {
		_ = config.ClearPendingAuth(state.ChallengeID)
		next := "ttsbuddy auth email start --email <address> --json"
		if state.Mode == "signup" {
			next = "ttsbuddy auth email start --email <address> --signup --json"
		}
		return structuredExitError(1, "the CLI authentication continuation expired", "AUTH_ERROR", "CHALLENGE_EXPIRED", next, false, 0)
	}
	code, err := readVerificationCode(cmd)
	if err != nil {
		return structuredExitError(2, err.Error(), "AUTH_ERROR", "INVALID_CODE", fmt.Sprintf("Provide a bounded six-digit code through stdin for the same challenge: ttsbuddy auth email verify --challenge-id %s --code-stdin --json", state.ChallengeID), true, 0)
	}
	clerk, err := clerkfapi.New(resolvedCfg.ClerkFrontendAPIURL, Version)
	if err != nil {
		return structuredExitError(1, "email authentication is unavailable", "AUTH_ERROR", "INVALID_CONFIGURATION", "Check the Clerk endpoint configuration.", false, 0)
	}
	clerkPreserved := false
	exchanged := false
	defer func() {
		if clerkPreserved || exchanged {
			clerk.Close()
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = clerk.Cleanup(cleanupCtx)
		cancel()
	}()
	if err := clerk.RestoreNativeClientToken(state.NativeClientToken); err != nil {
		return structuredExitError(1, "pending authentication state is invalid", "CLI_ERROR", "PENDING_STATE_INVALID", "Cancel the pending challenge and start again.", false, 0)
	}
	var proof *clerkfapi.SessionProof
	if state.Mode == "signup" {
		proof, err = clerk.VerifyEmailSignUp(cmd.Context(), clerkfapi.SignUpChallenge{SignUpID: state.SignUpID}, code)
	} else {
		proof, err = clerk.VerifyEmailCode(cmd.Context(), clerkfapi.Challenge{SignInID: state.SignInID, EmailAddressID: state.EmailAddressID}, code)
	}
	if err != nil {
		if rotated, exportErr := clerk.ExportNativeClientToken(); exportErr == nil && rotated != state.NativeClientToken {
			state.NativeClientToken = rotated
			if saveErr := config.SavePendingAuth(*state); saveErr != nil {
				_ = config.ClearPendingAuth(state.ChallengeID)
				return structuredExitError(1, "authentication continuation could not be updated safely", "AUTH_ERROR", "PENDING_STATE_UNAVAILABLE", "Start a new challenge with auth email start.", false, 0)
			}
		}
		mapped := classifyClerkAuthError(err, state.Mode == "signup")
		if mapped.reason == "INVALID_CODE" {
			mapped.nextAction = fmt.Sprintf("Retry the same challenge: ttsbuddy auth email verify --challenge-id %s --code-stdin --json", state.ChallengeID)
		}
		if mapped.retryable {
			clerkPreserved = true
			return mapped
		}
		_ = config.ClearPendingAuth(state.ChallengeID)
		return mapped
	}
	if err := config.ClearPendingAuth(state.ChallengeID); err != nil {
		return structuredExitError(1, "email verification succeeded but the local continuation could not be cleared; no CLI session was stored", "AUTH_ERROR", "PENDING_STATE_CLEANUP_FAILED", "Remove the pending state only after cleanup is confirmed, then start again.", false, 0)
	}
	exchanged, exchangeErr := exchangeAndStoreCLISession(cmd.Context(), proof.Token, false)
	if exchangeErr != nil {
		return exchangeErr
	}
	if flagJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"success": true, "status": "signed_in", "mode": state.Mode})
	}
	return nil
}

func runAuthEmailCancel(cmd *cobra.Command, _ []string) error {
	if err := rejectAuthGlobalCredentialFlags(cmd, false); err != nil {
		return err
	}
	if !config.IsPendingAuthID(authEmailCancelID) {
		return structuredExitError(2, "--challenge-id must be the opaque ID returned by auth email start", "CLI_ERROR", "INVALID_ARGUMENT", "Use the challenge ID returned by auth email start.", false, 0)
	}
	lock, err := config.AcquireLoginLock()
	if err != nil {
		return structuredExitError(1, err.Error(), "CLI_ERROR", "AUTH_IN_PROGRESS", "Wait for the other authentication process to finish.", true, 0)
	}
	defer func() { _ = lock.Release() }()
	state, err := config.LoadPendingAuth()
	if err != nil {
		return structuredExitError(1, "pending authentication state could not be read", "CLI_ERROR", "PENDING_STATE_INVALID", "Remove the malformed state only after inspecting the isolated config directory.", false, 0)
	}
	if state == nil || state.ChallengeID != authEmailCancelID {
		return structuredExitError(1, "pending authentication challenge was not found", "CLI_ERROR", "NO_PENDING_CHALLENGE", "Start a new challenge with auth email start.", false, 0)
	}
	if err := config.ClearPendingAuth(state.ChallengeID); err != nil {
		return structuredExitError(1, "pending authentication could not be canceled safely", "CLI_ERROR", "PENDING_STATE_CLEANUP_FAILED", "Retry cancel with the same challenge ID after checking the config directory.", true, 0)
	}
	cleanupConfirmed := false
	clerkOrigin, _, originErr := validateStructuredAuthConfig()
	if originErr == nil && clerkOrigin == state.ClerkOrigin {
		if clerk, newErr := clerkfapi.New(resolvedCfg.ClerkFrontendAPIURL, Version); newErr == nil {
			if restoreErr := clerk.RestoreNativeClientToken(state.NativeClientToken); restoreErr == nil {
				ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
				cleanupConfirmed = clerk.Cleanup(ctx) == nil
				cancel()
			} else {
				clerk.Close()
			}
		}
	}
	if flagJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"success": true, "status": "canceled", "remote_cleanup_confirmed": cleanupConfirmed})
	}
	_, _ = fmt.Fprintln(os.Stdout, "Pending email authentication canceled.")
	if !cleanupConfirmed {
		fmt.Fprintln(os.Stderr, "Warning: remote Clerk cleanup could not be confirmed; the short-lived challenge will expire.")
	}
	return nil
}

func runAuthBrowser(cmd *cobra.Command, _ []string) error {
	if err := rejectAuthGlobalCredentialFlags(cmd, true); err != nil {
		return err
	}
	if err := validateAuthURL(resolvedCfg); err != nil {
		return err
	}
	issuer, clientID := ClerkOAuthIssuer, ClerkOAuthClientID
	if resolvedCfg.AllowCustomAPIURL {
		if value := os.Getenv("TTSBUDDY_CLERK_OAUTH_ISSUER"); value != "" {
			issuer = value
		}
		if value := os.Getenv("TTSBUDDY_CLERK_OAUTH_CLIENT_ID"); value != "" {
			clientID = value
		}
	}
	if clientID == "" {
		return &exitError{code: 1, msg: "browser authentication is not configured: missing Clerk OAuth client ID"}
	}
	lock, err := config.AcquireLoginLock()
	if err != nil {
		return &exitError{code: 1, msg: err.Error()}
	}
	defer func() { _ = lock.Release() }()

	fmt.Fprintln(os.Stderr, "Signing in successfully will sign out any existing CLI session on another machine.")
	proof, err := runBrowserOAuth(cmd.Context(), issuer, clientID, resolvedCfg.AllowCustomAPIURL, cmd.ErrOrStderr())
	if err != nil {
		return &exitError{code: 1, msg: err.Error()}
	}
	_, err = exchangeAndStoreCLISession(cmd.Context(), proof, true)
	return err
}

func exchangeAndStoreCLISession(ctx context.Context, proof string, browser bool) (bool, error) {
	client, err := api.NewCLIAuthClient(resolvedCfg.CLIAuthURL, proof, Version, resolvedCfg.AllowCustomAPIURL)
	if err != nil {
		return false, err
	}
	var response *api.CLIAuthResponse
	var status int
	if browser {
		response, status, err = client.ExchangeBrowser(ctx)
	} else {
		response, status, err = client.Exchange(ctx)
	}
	if err != nil {
		mapped := classifyCLIAuthHTTPError(err, authMethodSuggestion)
		if status != 0 {
			mapped.msg = fmt.Sprintf("CLI login exchange failed (status %d). %s", status, mapped.msg)
		}
		return false, mapped
	}
	credential, err := validateLoginCredential(response)
	if err != nil {
		return true, err
	}
	cfg, err := config.Load()
	if err != nil {
		return true, err
	}
	expected := ""
	if cfg.CLISession != nil {
		expected = cfg.CLISession.Credential
	}
	if err := config.StoreCLISession(expected, config.StoredCLISession{Credential: credential.Token, ExpiresAt: credential.ExpiresAt}); err != nil {
		cleanup, cleanupErr := api.NewCLIAuthClient(resolvedCfg.CLIAuthURL, credential.Token, Version, resolvedCfg.AllowCustomAPIURL)
		if cleanupErr == nil {
			_, _, cleanupErr = cleanup.Revoke(ctx)
		}
		if cleanupErr != nil {
			return true, &exitError{code: 1, msg: "saving CLI session failed; server cleanup could not be confirmed"}
		}
		return true, &exitError{code: 1, msg: "saving CLI session failed; issued session was revoked"}
	}
	if response.Replaced && !flagJSON {
		fmt.Fprintln(os.Stderr, "Previous CLI session signed out.")
	}
	if !flagJSON {
		fmt.Fprintf(os.Stderr, "Signed in. CLI session expires at %s.\n", credential.ExpiresAt)
	}
	return true, nil
}

func shouldAttemptClerkCleanup(exchangeSucceeded bool) bool {
	return !exchangeSucceeded
}

func validateLoginCredential(response *api.CLIAuthResponse) (*api.CLIAuthCredential, error) {
	if response == nil || !response.Success || response.Credential == nil || response.Credential.Type != "cli_session" || response.Credential.Scope != "agent_tts" {
		return nil, structuredExitError(1, "CLI login returned an invalid credential", "CLI_AUTH_ERROR", "INVALID_CREDENTIAL", authMethodSuggestion, false, 0)
	}
	expires, err := time.Parse(time.RFC3339, response.Credential.ExpiresAt)
	if err != nil || !expires.After(time.Now()) || !regexp.MustCompile(`^ttsc_[0-9a-f]{8}_[0-9a-f]{48}$`).MatchString(response.Credential.Token) {
		return nil, structuredExitError(1, "CLI login returned an invalid credential", "CLI_AUTH_ERROR", "INVALID_CREDENTIAL", authMethodSuggestion, false, 0)
	}
	return response.Credential, nil
}

func storedSession() (*config.Config, *config.StoredCLISession, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	if cfg.CLISession == nil {
		return cfg, nil, nil
	}
	return cfg, cfg.CLISession, nil
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	if err := rejectAuthGlobalCredentialFlags(cmd, false); err != nil {
		return err
	}
	_, session, err := storedSession()
	if err != nil {
		return structuredExitError(1, "could not read the CLI session", "CLI_AUTH_ERROR", "LOCAL_STATE_ERROR", "Run ttsbuddy doctor.", false, 0)
	}
	if session == nil {
		return structuredExitError(1, "Not signed in. "+authMethodSuggestion, "CLI_AUTH_ERROR", "AUTH_REQUIRED", authMethodSuggestion, false, 0)
	}
	if err := validateAuthURL(resolvedCfg); err != nil {
		return err
	}
	client, err := api.NewCLIAuthClient(resolvedCfg.CLIAuthURL, session.Credential, Version, resolvedCfg.AllowCustomAPIURL)
	if err != nil {
		return err
	}
	response, status, err := client.Status(cmd.Context())
	if err != nil {
		if status == http.StatusUnauthorized {
			return structuredExitError(1, "CLI session is no longer valid. "+authMethodSuggestion, "CLI_AUTH_ERROR", "SESSION_REJECTED", authMethodSuggestion, false, 0)
		}
		mapped := classifyCLIAuthHTTPError(err, authMethodSuggestion)
		if status != 0 {
			mapped.msg = fmt.Sprintf("CLI session status failed (status %d). %s", status, mapped.msg)
		}
		return mapped
	}
	if response == nil || !response.Success || response.Credential == nil || response.Entitlement == nil {
		return structuredExitError(1, "CLI session status returned an invalid response", "CLI_AUTH_ERROR", "INVALID_RESPONSE", authMethodSuggestion, false, 0)
	}
	response.Credential.Token = ""
	if flagJSON {
		return json.NewEncoder(os.Stdout).Encode(response)
	}
	_, _ = fmt.Fprintf(os.Stdout, "Credential: %s\nUsable: %t\nExpires: %s\nEntitlement: %s\nAPI access: %t\n", response.Credential.Status, response.Credential.Usable, response.Credential.ExpiresAt, response.Entitlement.Status, response.Entitlement.APIAccess)
	return nil
}

func runAuthLogout(cmd *cobra.Command, _ []string) error {
	if err := rejectAuthGlobalCredentialFlags(cmd, false); err != nil {
		return err
	}
	_, session, err := storedSession()
	if err != nil {
		return err
	}
	if session == nil {
		return printAuthLogoutResult("signed_out", true)
	}
	if authLocalOnly {
		if err := config.ClearCLISession(session.Credential); err != nil {
			return err
		}
		if !flagJSON {
			fmt.Fprintln(os.Stderr, "Warning: server validity may continue until the CLI session expires.")
		}
		if flagJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"success": true, "status": "local_only", "remote_revocation_confirmed": false})
		}
		return nil
	}
	if err := validateAuthURL(resolvedCfg); err != nil {
		return err
	}
	client, err := api.NewCLIAuthClient(resolvedCfg.CLIAuthURL, session.Credential, Version, resolvedCfg.AllowCustomAPIURL)
	if err != nil {
		return err
	}
	response, status, revokeErr := client.Revoke(cmd.Context())
	if revokeErr != nil && status != 401 {
		payload := map[string]any{"success": false, "error": map[string]any{"code": "CLI_LOGOUT_FAILED", "message": "Logout failed; the local session was retained."}, "local_session_retained": true}
		var httpErr *api.CLIAuthHTTPError
		if errors.As(revokeErr, &httpErr) && httpErr.RetryAfterSeconds > 0 {
			payload["retry_after_seconds"] = httpErr.RetryAfterSeconds
		}
		return &exitError{code: 1, msg: "logout failed; local CLI session retained", jsonPayload: payload}
	}
	if revokeErr == nil && (response == nil || !response.Success || (response.Status != "revoked" && response.Status != "already_unusable")) {
		payload := map[string]any{"success": false, "error": map[string]any{"code": "CLI_LOGOUT_FAILED", "message": "Logout failed; the local session was retained."}, "local_session_retained": true}
		return &exitError{code: 1, msg: "logout failed; local CLI session retained", jsonPayload: payload}
	}
	if err := config.ClearCLISession(session.Credential); err != nil {
		return err
	}
	outcome := "already_unusable"
	if response != nil && response.Status != "" {
		outcome = response.Status
	}
	return printAuthLogoutResult(outcome, true)
}

func printAuthLogoutResult(status string, success bool) error {
	if flagJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"success": success, "status": status})
	}
	if status == "signed_out" {
		_, _ = fmt.Fprintln(os.Stdout, "Already signed out.")
	} else {
		_, _ = fmt.Fprintln(os.Stdout, "Signed out.")
	}
	return nil
}
