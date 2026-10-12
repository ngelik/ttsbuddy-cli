package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/config"
)

// DefaultAccountDeletionURL is the existing Clerk-authenticated deletion service.
const DefaultAccountDeletionURL = "https://hndrrtessqblbnkimjdw.supabase.co/functions/v1/delete-account"
const maxAccountDeletionBody = 1 << 20

var deletionUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var deletionErrorCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// AccountDeletionResponse contains only the service's public receipt fields.
// Success on a status read does not mean deletion completed; inspect State.
type AccountDeletionResponse struct {
	Success            bool   `json:"success"`
	State              string `json:"state,omitempty"`
	Retryable          bool   `json:"retryable"`
	PartialCleanup     bool   `json:"partial_cleanup"`
	ErrorCategory      string `json:"error_category,omitempty"`
	ErrorCode          string `json:"error_code,omitempty"`
	CorrelationID      string `json:"correlation_id,omitempty"`
	AppleAuthorization string `json:"apple_authorization,omitempty"`
	PendingCount       *int   `json:"pending_count,omitempty"`
	RetryAfterSeconds  *int   `json:"retry_after_seconds,omitempty"`
}

type AccountDeletionHTTPError struct{ StatusCode int }

func (e *AccountDeletionHTTPError) Error() string {
	return fmt.Sprintf("account deletion request failed with status %d", e.StatusCode)
}

type AccountDeletionClient struct {
	httpClient           *http.Client
	url, bearer, version string
}

func NewAccountDeletionClient(endpoint, bearer, version string, allowCustom bool) (*AccountDeletionClient, error) {
	if endpoint == "" {
		endpoint = DefaultAccountDeletionURL
	}
	if err := ValidateAccountDeletionURL(endpoint, allowCustom); err != nil {
		return nil, err
	}
	if strings.TrimSpace(bearer) == "" || strings.ContainsAny(bearer, " \t\r\n") || strings.HasPrefix(bearer, "ttsb_") || strings.HasPrefix(bearer, "ttsc_") || strings.HasPrefix(bearer, "ttsa_") {
		return nil, errors.New("account deletion requires fresh Clerk verification")
	}
	return &AccountDeletionClient{
		httpClient: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
		url:        endpoint, bearer: bearer, version: version,
	}, nil
}

// ValidateAccountDeletionURL checks a destination before acquiring deletion proof.
func ValidateAccountDeletionURL(endpoint string, allowCustom bool) error {
	if endpoint == "" {
		endpoint = DefaultAccountDeletionURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return errors.New("invalid account deletion URL")
	}
	if endpoint != DefaultAccountDeletionURL && !allowCustom {
		return errors.New("custom account deletion endpoint requires explicit opt in")
	}
	// The exact pinned Supabase URL is trusted independently of the speech API's host list.
	if err := config.CheckCredentialedAPIURL(endpoint, endpoint == DefaultAccountDeletionURL || allowCustom); err != nil {
		return errors.New("unsafe account deletion URL")
	}
	if parsed.Path != "/functions/v1/delete-account" && parsed.Path != "/v1/delete-account" {
		return errors.New("invalid account deletion endpoint path")
	}
	return nil
}

func (c *AccountDeletionClient) Status(ctx context.Context) (*AccountDeletionResponse, int, error) {
	return c.do(ctx, http.MethodGet, "", nil)
}

func (c *AccountDeletionClient) Delete(ctx context.Context, idempotencyKey string, appleManualFallback bool) (*AccountDeletionResponse, int, error) {
	if !deletionUUID.MatchString(idempotencyKey) {
		return nil, 0, errors.New("account deletion requires a UUID idempotency key")
	}
	body, err := json.Marshal(struct {
		IdempotencyKey      string `json:"idempotency_key"`
		AppleManualFallback bool   `json:"apple_manual_fallback_confirmed,omitempty"`
	}{idempotencyKey, appleManualFallback})
	if err != nil {
		return nil, 0, errors.New("creating account deletion request")
	}
	return c.do(ctx, http.MethodDelete, idempotencyKey, bytes.NewReader(body))
}

func (c *AccountDeletionClient) do(ctx context.Context, method, key string, body io.Reader) (*AccountDeletionResponse, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.url, body)
	if err != nil {
		return nil, 0, errors.New("creating account deletion request")
	}
	req.Header.Set("Authorization", "Bearer "+c.bearer)
	req.Header.Set("User-Agent", "ttsbuddy-cli/"+c.version)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		// Do not let net/http replay a destructive request on a reused connection.
		req.GetBody = nil
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, fmt.Errorf("account deletion request interrupted: %w", ctx.Err())
		}
		return nil, 0, errors.New("sending account deletion request; outcome is unknown")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAccountDeletionBody+1))
	if err != nil || len(data) > maxAccountDeletionBody {
		return nil, resp.StatusCode, errors.New("invalid account deletion response; outcome is unknown")
	}
	result, err := parseAccountDeletionResponse(data, method, resp.StatusCode)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if method == http.MethodGet && resp.StatusCode == http.StatusNotFound && result.State == "none" {
		return result, resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return result, resp.StatusCode, &AccountDeletionHTTPError{StatusCode: resp.StatusCode}
	}
	if method == http.MethodDelete && result.State != "completed" {
		return result, resp.StatusCode, errors.New("account deletion is incomplete")
	}
	return result, resp.StatusCode, nil
}

func parseAccountDeletionResponse(data []byte, method string, status int) (*AccountDeletionResponse, error) {
	invalid := errors.New("invalid account deletion response; outcome is unknown")
	// Reject duplicate keys and null typed fields, rather than allowing conflicting receipts.
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalid
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, invalid
		}
		name, ok := token.(string)
		if !ok {
			return nil, invalid
		}
		if _, exists := fields[name]; exists {
			return nil, invalid
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, invalid
		}
		fields[name] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, invalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalid
	}
	safeFields := make(map[string]json.RawMessage)
	for _, name := range []string{"success", "state", "retryable", "partial_cleanup", "error_category", "error_code", "correlation_id", "apple_authorization", "pending_count", "retry_after_seconds"} {
		if raw, exists := fields[name]; exists {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return nil, invalid
			}
			safeFields[name] = raw
		}
	}
	if _, ok := fields["success"]; !ok {
		return nil, invalid
	}
	safeData, err := json.Marshal(safeFields)
	if err != nil {
		return nil, invalid
	}
	var r AccountDeletionResponse
	if err := json.Unmarshal(safeData, &r); err != nil {
		return nil, invalid
	}
	switch r.State {
	case "", "none", "requested", "running", "failed", "completed":
	default:
		return nil, invalid
	}
	switch r.ErrorCategory {
	case "", "authentication", "reverification_required", "stripe_cancellation", "apple_token_revocation", "provider_object_deletion", "provider_task_unknown", "database_cleanup", "clerk_deletion", "unknown":
	default:
		return nil, invalid
	}
	if r.ErrorCode != "" && !deletionErrorCode.MatchString(r.ErrorCode) {
		return nil, invalid
	}
	if r.CorrelationID != "" && !deletionUUID.MatchString(r.CorrelationID) {
		return nil, invalid
	}
	if r.PendingCount != nil && *r.PendingCount < 0 {
		return nil, invalid
	}
	if r.RetryAfterSeconds != nil && (*r.RetryAfterSeconds < 1 || *r.RetryAfterSeconds > 300) {
		return nil, invalid
	}
	if r.State == "completed" {
		if status != http.StatusOK || !r.Success || r.CorrelationID == "" || r.Retryable || r.ErrorCategory != "" || r.ErrorCode != "" || r.PendingCount != nil || r.RetryAfterSeconds != nil {
			return nil, invalid
		}
		switch r.AppleAuthorization {
		case "not_linked", "revoked", "already_revoked_by_apple", "manual_action_required":
		default:
			return nil, invalid
		}
	} else if r.AppleAuthorization != "" {
		return nil, invalid
	}
	if r.State == "none" && (method != http.MethodGet || status != http.StatusNotFound || r.Success || r.PartialCleanup || r.Retryable || r.CorrelationID != "" || r.ErrorCategory != "" || r.ErrorCode != "") {
		return nil, invalid
	}
	if status == http.StatusOK {
		if !r.Success || r.State == "" || r.State == "none" || r.CorrelationID == "" {
			return nil, invalid
		}
	} else if r.Success || r.State == "completed" {
		return nil, invalid
	}
	return &r, nil
}
