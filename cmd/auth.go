package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/clerkfapi"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/ngelik/ttsbuddy-cli/internal/prompt"
	"github.com/spf13/cobra"
)

var authLocalOnly bool

var authCmd = &cobra.Command{Use: "auth", Short: "Sign in and manage the CLI session", Args: noArgs}
var authLoginCmd = &cobra.Command{Use: "login", Short: "Sign in with an email code", Args: noArgs, RunE: runAuthLogin}
var authStatusCmd = &cobra.Command{Use: "status", Short: "Show CLI session status", Args: noArgs, RunE: runAuthStatus}
var authLogoutCmd = &cobra.Command{Use: "logout", Short: "Sign out the CLI session", Args: noArgs, RunE: runAuthLogout}

func init() {
	authLogoutCmd.Flags().BoolVar(&authLocalOnly, "local-only", false, "remove local session without server revocation")
	authCmd.AddCommand(authLoginCmd, authStatusCmd, authLogoutCmd)
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
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cleanupErr := clerk.Cleanup(ctx)
		if shouldWarnClerkCleanup(exchanged, cleanupErr) {
			fmt.Fprintln(os.Stderr, "Warning: temporary sign-in cleanup could not be confirmed.")
		}
		clerk.Close()
	}()
	ctx := cmd.Context()
	challenge, err := clerk.StartEmailCode(ctx, email)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "If this address belongs to an eligible TTS Buddy account, check your email for a code.")
	code, err := p.Secret("Code: ", 6)
	if err != nil {
		return &exitError{code: 2, msg: err.Error()}
	}
	if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(code) {
		return &exitError{code: 2, msg: "code must be exactly six digits"}
	}
	proof, err := clerk.VerifyEmailCode(ctx, *challenge, code)
	if err != nil {
		return err
	}
	client, err := api.NewCLIAuthClient(resolvedCfg.CLIAuthURL, proof.Token, Version, resolvedCfg.AllowCustomAPIURL)
	if err != nil {
		return err
	}
	response, status, err := client.Exchange(ctx)
	if err != nil {
		return &exitError{code: 1, msg: fmt.Sprintf("CLI login exchange failed (status %d)", status)}
	}
	exchanged = true
	credential, err := validateLoginCredential(response)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
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
			return &exitError{code: 1, msg: "saving CLI session failed; server cleanup could not be confirmed"}
		}
		return &exitError{code: 1, msg: "saving CLI session failed; issued session was revoked"}
	}
	if response.Replaced {
		fmt.Fprintln(os.Stderr, "Previous CLI session signed out.")
	}
	fmt.Fprintf(os.Stderr, "Signed in. CLI session expires at %s.\n", credential.ExpiresAt)
	return nil
}

func shouldWarnClerkCleanup(exchangeSucceeded bool, cleanupErr error) bool {
	return cleanupErr != nil && !exchangeSucceeded
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
		return &exitError{code: 1, msg: "Not signed in. Run: ttsbuddy auth login"}
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
