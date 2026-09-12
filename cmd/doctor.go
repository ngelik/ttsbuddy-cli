package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/spf13/cobra"
)

var doctorOnline bool

var doctorCLICredentialPattern = regexp.MustCompile(`^ttsc_[0-9a-f]{8}_[0-9a-f]{48}$`)

type doctorCheck struct {
	Reason  string         `json:"reason,omitempty"`
	Action  *api.CLIAction `json:"action,omitempty"`
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

type doctorReport struct {
	Ready       bool          `json:"ready"`
	Checks      []doctorCheck `json:"checks"`
	NextActions []string      `json:"next_actions,omitempty"`
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check local CLI readiness and configuration",
	Long:  "Inspect local configuration and credentials without changing them. Use --online for bounded read-only connectivity checks.",
	Args:  noArgs,
	RunE:  runDoctor,
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorOnline, "online", false, "run bounded read-only connectivity checks")
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	checks := make([]doctorCheck, 0, 8)
	next := make([]string, 0, 3)

	checks = append(checks, doctorCheck{Name: "version", Status: "healthy", Details: map[string]any{"version": Version}})

	var configPath string
	var pathErr error
	var cfg *config.Config
	var loadErr error
	if configDirOverrideErr != nil {
		pathErr = configDirOverrideErr
		loadErr = configDirOverrideErr
		cfg = &config.Config{}
	} else {
		configPath, pathErr = config.ConfigPath()
		cfg, loadErr = config.Load()
	}
	configDir := ""
	if pathErr == nil {
		configDir = filepath.Dir(configPath)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	if pathErr != nil {
		details := map[string]any{}
		if strings.TrimSpace(flagConfigDir) != "" {
			details["requested"] = flagConfigDir
		}
		checks = append(checks, doctorCheck{Name: "config_directory", Status: "failed", Message: "could not resolve the config directory", Details: details})
		next = append(next, "Set TTSBUDDY_CONFIG_DIR to an accessible absolute directory.")
	} else if dirErr := config.CheckConfigDirPermissions(configDir); dirErr != nil {
		var permissionErr *config.ConfigDirPermissionsError
		if errors.As(dirErr, &permissionErr) {
			checks = append(checks, doctorCheck{Name: "config_directory", Status: "failed", Reason: configDirPermissionsReason, Message: "Email authentication requires this directory to have mode 0700 (owner access only).", Details: configPermissionDetails(permissionErr), Action: configPermissionAction(permissionErr)})
			next = append(next, "Fix: "+configPermissionFix(permissionErr), "Then rerun: ttsbuddy doctor --json")
		} else {
			checks = append(checks, doctorCheck{Name: "config_directory", Status: "failed", Message: "config directory cannot be used; inspect its type and access permissions", Details: map[string]any{"path": configDir}})
		}
	} else if info, err := os.Lstat(configDir); err != nil {
		if os.IsNotExist(err) {
			checks = append(checks, doctorCheck{Name: "config_directory", Status: "warning", Message: "config directory does not exist yet", Details: map[string]any{"path": configDir}})
		} else {
			checks = append(checks, doctorCheck{Name: "config_directory", Status: "failed", Message: "config directory cannot be inspected"})
			next = append(next, "Check permissions for the configured CLI directory.")
		}
	} else if !info.IsDir() {
		checks = append(checks, doctorCheck{Name: "config_directory", Status: "failed", Message: "configured path is not a directory"})
		next = append(next, "Choose an absolute directory for CLI configuration.")
	} else {
		checks = append(checks, doctorCheck{Name: "config_directory", Status: "healthy", Details: map[string]any{"path": configDir}})
	}
	if loadErr != nil {
		checks = append(checks, doctorCheck{Name: "config_file", Status: "failed", Message: "config file could not be read or parsed"})
		next = append(next, "Repair or remove the malformed config file, then run doctor again.")
	} else {
		checks = append(checks, doctorCheck{Name: "config_file", Status: "healthy"})
	}

	flags := config.FlagValues{}
	if cmd.Flags().Changed("key") {
		flags.APIKey = &flagAPIKey
	}
	resolved, warnings := config.Resolve(cfg, flags)
	credentialStatus := doctorCredentialStatus(cfg, resolved, flags.APIKey != nil)
	credentialDetails := doctorCredentialDetails(cfg, resolved, flags.APIKey != nil)
	credentialMessage := "credential syntax checked locally; --online verifies CLI sessions and connectivity"
	if len(warnings) > 0 {
		// Warnings intentionally remain categorized and do not include the
		// offending value, which may contain a credential or endpoint detail.
		checks = append(checks, doctorCheck{Name: "credential", Status: "warning", Message: "one or more stored settings were ignored", Details: credentialDetails})
	} else {
		checks = append(checks, doctorCheck{Name: "credential", Status: credentialStatus, Message: credentialMessage, Details: credentialDetails})
	}
	if credentialStatus != "healthy" {
		if flags.APIKey != nil {
			if strings.TrimSpace(flagAPIKey) == "ttsb_demo_cli" && doctorMode(resolved.APIURL) != "demo" {
				next = append(next, "Use ttsb_demo_cli only with the official /v1/cli-demo endpoint, or provide a real permanent key.")
			} else {
				next = append(next, "Provide a valid permanent key or run either: ttsbuddy auth email or ttsbuddy auth browser")
			}
		} else {
			next = append(next, "Run either: ttsbuddy auth email or ttsbuddy auth browser")
		}
	}

	endpointChecks, endpointNext := doctorEndpointChecks(resolved)
	checks = append(checks, endpointChecks...)
	next = append(next, endpointNext...)
	checks = append(checks, doctorCheck{Name: "mode", Status: "healthy", Details: map[string]any{"mode": doctorMode(resolved.APIURL)}})

	if doctorOnline && !doctorHasBlockingCheck(checks) {
		onlineChecks, onlineNext := doctorOnlineChecks(cmd.Context(), cfg, resolved, credentialDetails["source"])
		checks = append(checks, onlineChecks...)
		next = append(next, onlineNext...)
	} else if doctorOnline {
		checks = append(checks, doctorCheck{Name: "connectivity", Status: "not_checked", Message: "repair the failed local configuration checks before running online diagnostics"})
	} else {
		checks = append(checks, doctorCheck{Name: "connectivity", Status: "not_checked", Message: "offline checks only; rerun with --online for bounded read-only connectivity"})
	}

	ready := credentialStatus == "healthy" && !doctorHasBlockingCheck(checks)
	report := doctorReport{Ready: ready, Checks: checks, NextActions: uniqueStrings(next)}
	if flagJSON {
		if !ready {
			return &exitError{code: 1, msg: "CLI is not ready; see doctor output for next actions", jsonPayload: report}
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	_, _ = fmt.Fprintf(os.Stdout, "TTSBuddy doctor: %s\n", map[bool]string{true: "ready", false: "not ready"}[ready])
	for _, check := range checks {
		if check.Reason == configDirPermissionsReason {
			_, _ = fmt.Fprintf(os.Stdout, "FAIL config_directory\nPath: %s\nPermissions: %s\nRequired: %s (owner access only)\n\n%s\n", check.Details["path"], check.Details["actual_mode"], check.Details["required_mode"], check.Message)
			continue
		}
		message := check.Status
		if check.Message != "" {
			message += " — " + check.Message
		}
		_, _ = fmt.Fprintf(os.Stdout, "%-16s %s\n", check.Name, message)
	}
	for _, action := range report.NextActions {
		_, _ = fmt.Fprintf(os.Stdout, "Next: %s\n", action)
	}
	if !ready {
		return &exitError{code: 1, msg: "CLI is not ready; see doctor output for next actions"}
	}
	return nil
}

func doctorCredentialStatus(cfg *config.Config, resolved *config.ResolvedConfig, flagSet bool) string {
	if resolved == nil || strings.TrimSpace(resolved.APIKey) == "" {
		return "warning"
	}
	if flagSet {
		if strings.TrimSpace(flagAPIKey) == "ttsb_demo_cli" && doctorMode(resolved.APIURL) == "demo" {
			return "healthy"
		}
		if !config.IsSubscriptionCredential(strings.TrimSpace(flagAPIKey)) {
			return "warning"
		}
	}
	if config.IsSubscriptionCredential(strings.TrimSpace(resolved.APIKey)) || doctorCLICredentialPattern.MatchString(strings.TrimSpace(resolved.APIKey)) {
		return "healthy"
	}
	return "warning"
}

func doctorCredentialDetails(cfg *config.Config, resolved *config.ResolvedConfig, flagSet bool) map[string]any {
	source := "none"
	kind := "none"
	if flagSet {
		source, kind = "flag", "permanent"
		if strings.TrimSpace(flagAPIKey) == "ttsb_demo_cli" && resolved != nil && doctorMode(resolved.APIURL) == "demo" {
			kind = "demo"
		}
	} else if config.IsSubscriptionCredential(os.Getenv("TTSBUDDY_API_KEY")) {
		source, kind = "environment", "permanent"
	} else if cfg != nil {
		if session, warning := config.ActiveCLISession(cfg, time.Now()); session != nil && warning == "" && resolved != nil && resolved.APIKey == session.Credential {
			source, kind = "cli_session", "temporary"
		} else if config.IsSubscriptionCredential(cfg.APIKey) && resolved != nil && resolved.APIKey == cfg.APIKey {
			source, kind = "config", "permanent"
		}
	}
	details := map[string]any{"configured": resolved != nil && resolved.APIKey != "", "source": source, "kind": kind}
	if cfg != nil && cfg.CLISession != nil {
		if expires, err := time.Parse(time.RFC3339, cfg.CLISession.ExpiresAt); err == nil {
			details["session_expires_at"] = expires.UTC().Format(time.RFC3339)
		}
	}
	return details
}

func doctorEndpointChecks(resolved *config.ResolvedConfig) ([]doctorCheck, []string) {
	if resolved == nil {
		return []doctorCheck{{Name: "endpoints", Status: "failed", Message: "configuration could not be resolved"}}, []string{"Check the CLI endpoint configuration."}
	}
	checks := make([]doctorCheck, 0, 3)
	next := make([]string, 0, 2)
	for _, endpoint := range []struct {
		name  string
		value string
		cred  bool
	}{
		{"api_endpoint", resolved.APIURL, true},
		{"cli_auth_endpoint", resolved.CLIAuthURL, true},
		{"tts_endpoint", resolved.TTSAPIBaseURL, false},
	} {
		details := map[string]any{"url": doctorSafeURL(endpoint.value)}
		parsed, err := url.Parse(endpoint.value)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			checks = append(checks, doctorCheck{Name: endpoint.name, Status: "failed", Message: "endpoint URL is invalid", Details: details})
			next = append(next, "Use an HTTPS endpoint or an explicitly allowed local endpoint.")
			continue
		}
		if endpoint.cred {
			if err := config.CheckCredentialedAPIURL(endpoint.value, resolved.AllowCustomAPIURL); err != nil {
				checks = append(checks, doctorCheck{Name: endpoint.name, Status: "failed", Message: "credentialed endpoint is not trusted", Details: details})
				next = append(next, "Use the official TTS Buddy endpoint or explicitly enable a trusted custom endpoint.")
				continue
			}
		}
		checks = append(checks, doctorCheck{Name: endpoint.name, Status: "healthy", Details: details})
	}
	return checks, next
}

func doctorOnlineChecks(parent context.Context, cfg *config.Config, resolved *config.ResolvedConfig, credentialSource any) ([]doctorCheck, []string) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if resolved == nil {
		return []doctorCheck{{Name: "connectivity", Status: "failed", Message: "configuration could not be resolved"}}, []string{"Repair the local configuration before running online checks."}
	}
	if credentialSource == "flag" && doctorCredentialStatus(cfg, resolved, true) != "healthy" {
		return []doctorCheck{{Name: "connectivity", Status: "failed", Message: "the credential supplied by --key is invalid"}}, []string{"Provide a valid permanent key or use browser/email authentication."}
	}
	if credentialSource == "cli_session" {
		session, warning := config.ActiveCLISession(cfg, time.Now())
		if session == nil || warning != "" {
			return []doctorCheck{{Name: "connectivity", Status: "failed", Message: "stored CLI session is not usable"}}, []string{"Run either: ttsbuddy auth email or ttsbuddy auth browser"}
		}
		client, err := api.NewCLIAuthClient(resolved.CLIAuthURL, session.Credential, Version, resolved.AllowCustomAPIURL)
		if err != nil {
			return []doctorCheck{{Name: "connectivity", Status: "failed", Message: "CLI session endpoint is invalid"}}, []string{"Check the CLI authentication endpoint configuration."}
		}
		response, status, err := client.Status(ctx)
		if err != nil {
			mapped := classifyCLIAuthHTTPError(err, "Run either: ttsbuddy auth email or ttsbuddy auth browser")
			return []doctorCheck{{Name: "connectivity", Status: "failed", Message: mapped.msg, Details: map[string]any{"http_status": status}}}, []string{mapped.nextAction}
		}
		if response == nil || !response.Success || response.Credential == nil || response.Entitlement == nil || !response.Credential.Usable || !response.Entitlement.APIAccess {
			return []doctorCheck{{Name: "connectivity", Status: "failed", Message: "CLI session is not usable for API access"}}, []string{"Run either: ttsbuddy auth email or ttsbuddy auth browser"}
		}
		return []doctorCheck{{Name: "connectivity", Status: "healthy", Message: "CLI session and API entitlement verified"}}, nil
	}
	if resolved.APIKey == "" {
		return []doctorCheck{{Name: "connectivity", Status: "not_checked", Message: "no credential is configured"}}, nil
	}
	// Permanent ttsb credentials are never sent to the CLI-auth endpoint. A
	// public voices request verifies network reachability without disclosing it.
	client := api.NewClient("", "", Version)
	if _, err := client.FetchVoices(ctx, resolved.TTSAPIBaseURL); err != nil {
		return []doctorCheck{{Name: "connectivity", Status: "failed", Message: "public voice endpoint could not be reached"}}, []string{"Check network access and the TTS API endpoint."}
	}
	return []doctorCheck{{Name: "connectivity", Status: "healthy", Message: "public voice endpoint reachable; permanent key syntax checked locally"}}, nil
}

func doctorMode(apiURL string) string {
	if parsed, err := url.Parse(apiURL); err == nil {
		host := strings.ToLower(parsed.Hostname())
		path := strings.TrimRight(parsed.Path, "/")
		if (host == "www.ttsbuddy.com" || host == "ttsbuddy.com") && path == "/v1/cli-demo" {
			return "demo"
		}
	}
	return "real"
}

func doctorSafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "configured"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

func doctorHasBlockingCheck(checks []doctorCheck) bool {
	for _, check := range checks {
		if check.Status == "failed" {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
