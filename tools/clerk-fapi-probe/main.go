package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/clerkfapi"
	"golang.org/x/term"
)

const frontendAPIEnv = "TTSBUDDY_CLERK_FRONTEND_API_URL"

func main() {
	signup := flag.Bool("signup", false, "exercise the email signup flow instead of existing-account login")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	frontendAPIURL := strings.TrimSpace(os.Getenv(frontendAPIEnv))
	if frontendAPIURL == "" {
		writeHardGate("missing TTSBUDDY_CLERK_FRONTEND_API_URL")
		os.Exit(2)
	}

	client, err := clerkfapi.New(frontendAPIURL, "probe")
	if err != nil {
		writeJSON(map[string]any{
			"stage": "configuration",
			"ok":    false,
			"error": "invalid development Frontend API URL",
		})
		os.Exit(1)
	}
	defer client.Close()

	prompt := "Email (existing development account): "
	if *signup {
		prompt = "Email (new development account): "
	}
	email, err := promptLine(prompt)
	if err != nil {
		writeJSON(map[string]any{"stage": "input_email", "ok": false, "error": "unable to read email"})
		os.Exit(1)
	}

	var challenge *clerkfapi.Challenge
	var signupChallenge *clerkfapi.SignUpChallenge
	stage := "start_email_code"
	if *signup {
		signupChallenge, err = client.StartEmailSignUp(ctx, email)
		stage = "start_email_signup"
	} else {
		challenge, err = client.StartEmailCode(ctx, email)
	}
	if err != nil {
		writeFailure(stage, err, client)
		os.Exit(1)
	}
	writeJSON(map[string]any{
		"stage":             stage,
		"ok":                true,
		"fapi_version":      clerkfapi.APIVersion,
		"request_headers":   []string{"Clerk-API-Version", "User-Agent", "Authorization"},
		"native_query_flag": "_is_native=true",
		"request_ids":       client.RequestIDs(),
	})

	code, err := promptHidden("Email code: ")
	if err != nil {
		writeFailure("read_email_code", err, client)
		os.Exit(1)
	}

	verifyStage := "verify_email_code"
	var proof *clerkfapi.SessionProof
	if *signup {
		proof, err = client.VerifyEmailSignUp(ctx, *signupChallenge, code)
		verifyStage = "verify_email_signup"
	} else {
		proof, err = client.VerifyEmailCode(ctx, *challenge, code)
	}
	if err != nil {
		writeFailure(verifyStage, err, client)
		os.Exit(1)
	}

	claimTypes := summarizeJWTClaimTypes(proof.Token)
	writeJSON(map[string]any{
		"stage":             verifyStage,
		"ok":                true,
		"session_id_issued": proof.SessionID != "",
		"jwt_claim_types":   claimTypes,
		"request_ids":       client.RequestIDs(),
	})

	if err := cleanup(client); err != nil {
		writeJSON(map[string]any{"stage": "cleanup", "ok": false, "error": "cleanup failed"})
		os.Exit(1)
	}
	writeJSON(map[string]any{"stage": "cleanup", "ok": true})
}

func cleanup(client *clerkfapi.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.Cleanup(ctx)
}

func writeFailure(stage string, err error, client *clerkfapi.Client) {
	// Never print protocol error text: Clerk may include account-sensitive
	// details, and the probe must not become an account-enumeration oracle.
	record := map[string]any{
		"stage":       stage,
		"ok":          false,
		"error":       "probe failed",
		"request_ids": client.RequestIDs(),
	}
	if protocolStage := clerkfapi.FailureStage(err); protocolStage != "" {
		record["protocol_stage"] = protocolStage
	}
	var requestErr *clerkfapi.RequestError
	if errors.As(err, &requestErr) && requestErr.RetryAfterSeconds > 0 {
		record["retry_after_seconds"] = requestErr.RetryAfterSeconds
	}
	writeJSON(record)
	if cleanupErr := cleanup(client); cleanupErr != nil {
		writeJSON(map[string]any{"stage": "cleanup", "ok": false, "error": "cleanup failed"})
		return
	}
	writeJSON(map[string]any{"stage": "cleanup", "ok": true})
}

func promptLine(label string) (string, error) {
	_, _ = fmt.Fprint(os.Stdout, label)
	reader := bufio.NewReader(os.Stdin)
	value, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func promptHidden(label string) (string, error) {
	_, _ = fmt.Fprint(os.Stdout, label)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(os.Stdout)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

func summarizeJWTClaimTypes(token string) map[string]string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return map[string]string{"format": "unreadable"}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return map[string]string{"format": "unreadable"}
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return map[string]string{"format": "unreadable"}
	}

	keys := make([]string, 0, len(claims))
	for key := range claims {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	types := make(map[string]string, len(keys))
	for _, key := range keys {
		types[key] = jsonType(claims[key])
	}
	return types
}

func jsonType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func writeJSON(value map[string]any) {
	value["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeHardGate(reason string) {
	writeJSON(map[string]any{
		"stage":            "configuration",
		"ok":               false,
		"development_only": true,
		"live_gate_open":   false,
		"error":            reason,
	})
}
