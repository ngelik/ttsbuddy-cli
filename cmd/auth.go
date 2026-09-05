package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/clerkfapi"
	"github.com/ngelik/ttsbuddy-cli/internal/clerkoauth"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/ngelik/ttsbuddy-cli/internal/prompt"
	"github.com/spf13/cobra"
)

var authLocalOnly bool

const (
	signupEmailAddressBlockedCode    = "form_email_address_blocked"
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
var authBrowserCmd = &cobra.Command{Use: "browser", Short: "Sign in with a browser", Args: noArgs, RunE: runAuthBrowser}
var authStatusCmd = &cobra.Command{Use: "status", Short: "Show CLI session status", Args: noArgs, RunE: runAuthStatus}
var authLogoutCmd = &cobra.Command{Use: "logout", Short: "Sign out the CLI session", Args: noArgs, RunE: runAuthLogout}

func init() {
	authLogoutCmd.Flags().BoolVar(&authLocalOnly, "local-only", false, "remove local session without server revocation")
	authLoginCmd.Flags().Bool("signup", false, "create a new account instead of signing in")
	authEmailCmd.Flags().Bool("signup", false, "create a new account instead of signing in")
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
			if clerkfapi.FailureCode(startErr) == signupEmailAddressBlockedCode {
				return &exitError{code: 1, msg: signupEmailAddressBlockedMessage}
			}
			if clerkfapi.IsSignupEmailExists(startErr) {
				return &exitError{code: 1, msg: "An account already exists for this email. Run: ttsbuddy auth email"}
			}
			if clerkfapi.IsSignupBrowserFallback(startErr) {
				return &exitError{code: 1, msg: "CLI signup requires browser authentication. Run: ttsbuddy auth browser"}
			}
			return startErr
		}
		fmt.Fprintln(os.Stderr, "If this is a new eligible address, check your email for a verification code.")
		fmt.Fprintln(os.Stderr, "Already registered? Run: ttsbuddy auth email")
		signUpChallenge = started
	} else {
		started, startErr := clerk.StartEmailCode(ctx, email)
		if startErr != nil {
			if clerkfapi.FailureCode(startErr) == "form_identifier_not_found" {
				return &exitError{code: 1, msg: "No TTS Buddy account was found for this email. To create one, run: ttsbuddy auth email --signup"}
			}
			return startErr
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
		if signup && clerkfapi.FailureCode(err) == signupEmailAddressBlockedCode {
			return &exitError{code: 1, msg: signupEmailAddressBlockedMessage}
		}
		if signup && clerkfapi.IsSignupEmailExists(err) {
			return &exitError{code: 1, msg: "An account already exists for this email. Run: ttsbuddy auth email"}
		}
		if signup && clerkfapi.IsSignupBrowserFallback(err) {
			return &exitError{code: 1, msg: "CLI signup requires browser authentication. Run: ttsbuddy auth browser"}
		}
		return err
	}
	exchanged, err = exchangeAndStoreCLISession(ctx, proof.Token, false)
	return err
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
		return false, &exitError{code: 1, msg: fmt.Sprintf("CLI login exchange failed (status %d)", status)}
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
	if response.Replaced {
		fmt.Fprintln(os.Stderr, "Previous CLI session signed out.")
	}
	fmt.Fprintf(os.Stderr, "Signed in. CLI session expires at %s.\n", credential.ExpiresAt)
	return true, nil
}

func shouldAttemptClerkCleanup(exchangeSucceeded bool) bool {
	return !exchangeSucceeded
}

func validateLoginCredential(response *api.CLIAuthResponse) (*api.CLIAuthCredential, error) {
	if response == nil || !response.Success || response.Credential == nil || response.Credential.Type != "cli_session" || response.Credential.Scope != "agent_tts" {
		return nil, &exitError{code: 1, msg: "CLI login returned an invalid credential"}
	}
	expires, err := time.Parse(time.RFC3339, response.Credential.ExpiresAt)
	if err != nil || !expires.After(time.Now()) || !regexp.MustCompile(`^ttsc_[0-9a-f]{8}_[0-9a-f]{48}$`).MatchString(response.Credential.Token) {
		return nil, &exitError{code: 1, msg: "CLI login returned an invalid credential"}
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
		return err
	}
	if session == nil {
		return &exitError{code: 1, msg: "Not signed in. Run: ttsbuddy auth browser"}
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
		return &exitError{code: 1, msg: fmt.Sprintf("CLI session status failed (status %d)", status)}
	}
	if response == nil || !response.Success || response.Credential == nil || response.Entitlement == nil {
		return &exitError{code: 1, msg: "CLI session status returned an invalid response"}
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
