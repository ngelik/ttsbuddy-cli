package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// BillingPlan is the public, provider-safe plan catalogue item.
type BillingPlan struct {
	Name                string `json:"name"`
	DisplayName         string `json:"display_name,omitempty"`
	MonthlyTTSMinutes   *int   `json:"monthly_tts_minutes,omitempty"`
	Currency            string `json:"currency"`
	RecurringUnitAmount int    `json:"recurring_unit_amount"`
	Interval            string `json:"interval"`
	TaxBehavior         string `json:"tax_behavior"`
}

type BillingAction struct {
	Type string   `json:"type"`
	URL  string   `json:"url,omitempty"`
	Argv []string `json:"argv,omitempty"`
}

type BillingSource struct {
	Tier   string `json:"tier"`
	Status string `json:"status"`
}

type BillingUsage struct {
	Month                     string   `json:"month"`
	TTSMinutesUsed            float64  `json:"tts_minutes_used"`
	MonthlyTTSMinutes         *float64 `json:"monthly_tts_minutes"`
	ProjectedRemainingMinutes *float64 `json:"projected_remaining_minutes"`
	UnlimitedMinutes          bool     `json:"unlimited_minutes"`
}

type BillingAuthorization struct {
	State          string `json:"state"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	OwnerActionURL string `json:"owner_action_url,omitempty"`
}

type BillingQuote struct {
	ExpiresAt              string   `json:"expires_at"`
	Currency               string   `json:"currency"`
	ImmediateSubtotalMinor int      `json:"immediate_subtotal_minor"`
	ImmediateTaxMinor      *int     `json:"immediate_tax_minor"`
	ImmediateTotalMinor    *int     `json:"immediate_total_minor"`
	AmountIsEstimate       bool     `json:"amount_is_estimate"`
	RecurringBaseMinor     int      `json:"recurring_base_minor"`
	Interval               string   `json:"interval"`
	TaxBehavior            string   `json:"tax_behavior"`
	ProjectedRemaining     *float64 `json:"projected_remaining_minutes"`
	UnlimitedMinutes       bool     `json:"unlimited_minutes"`
}

type BillingOperationView struct {
	OperationID      string         `json:"operation_id"`
	State            string         `json:"state"`
	TargetPlan       string         `json:"target_plan"`
	EntitlementReady bool           `json:"entitlement_ready"`
	Quote            *BillingQuote  `json:"quote,omitempty"`
	Action           *BillingAction `json:"action,omitempty"`
	Reason           string         `json:"reason,omitempty"`
}

type BillingPlansResponse struct {
	Plans []BillingPlan `json:"plans"`
}
type BillingStatusResponse struct {
	RegistrationID string                 `json:"registration_id"`
	Source         BillingSource          `json:"source"`
	Usage          BillingUsage           `json:"usage"`
	Authorization  BillingAuthorization   `json:"authorization"`
	Operations     []BillingOperationView `json:"operations"`
}

// BillingHTTPError is deliberately separate from TTS API errors so billing
// mutation is never routed through synthesis retry/idempotency code.
type BillingHTTPError struct {
	StatusCode int
	Reason     string
	Action     *BillingAction
}

func (e *BillingHTTPError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("billing API error %d: %s", e.StatusCode, e.Reason)
	}
	return fmt.Sprintf("billing API error %d", e.StatusCode)
}

func (c *Client) billingBaseURL() (string, error) {
	u, err := url.Parse(c.apiURL)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", &BillingValidationError{Kind: "INVALID_CONFIGURATION"}
	}
	p := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(p, "/v1/agent-billing") {
		// already points at the billing resource
	} else if strings.HasSuffix(p, "/v1/agent-tts") {
		p = strings.TrimSuffix(p, "/v1/agent-tts")
		p += "/v1/agent-billing"
	} else if strings.HasSuffix(p, "/functions/v1/agent-tts") {
		p = strings.TrimSuffix(p, "/functions/v1/agent-tts")
		p += "/functions/v1/agent-billing"
	} else {
		return "", &BillingValidationError{Kind: "INVALID_CONFIGURATION"}
	}
	u.Path = strings.TrimRight(p, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (c *Client) doBilling(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	base, err := c.billingBaseURL()
	if err != nil {
		return nil, 0, err
	}
	var reader io.Reader
	if body != nil {
		encoded, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return nil, 0, &BillingValidationError{Kind: "INVALID_BILLING_REQUEST"}
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return nil, 0, &BillingValidationError{Kind: "INVALID_CONFIGURATION"}
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", "ttsbuddy-cli/"+c.version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, &BillingTransportError{}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, resp.StatusCode, &BillingTransportError{}
	}
	if len(data) > maxResponseSize {
		return nil, resp.StatusCode, &BillingValidationError{Kind: "INVALID_BILLING_RESPONSE"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload struct {
			Reason string         `json:"reason"`
			Action *BillingAction `json:"action"`
		}
		if json.Unmarshal(data, &payload) != nil {
			return nil, resp.StatusCode, &BillingValidationError{Kind: "INVALID_BILLING_RESPONSE"}
		}
		return nil, resp.StatusCode, &BillingHTTPError{StatusCode: resp.StatusCode, Reason: BillingReason(payload.Reason), Action: SanitizeBillingAction(payload.Action, c.apiURL, payload.Reason)}
	}
	return data, resp.StatusCode, nil
}

func decodeBilling[T any](data []byte, target *T, baseURL string) error {
	if err := json.Unmarshal(data, target); err != nil {
		return &BillingValidationError{Kind: "INVALID_BILLING_RESPONSE"}
	}
	return validateBillingResponse(any(target), baseURL)
}

func (c *Client) BillingPlans(ctx context.Context) (*BillingPlansResponse, int, error) {
	data, status, err := c.doBilling(ctx, http.MethodGet, "/plans", nil)
	if err != nil {
		return nil, status, err
	}
	var out BillingPlansResponse
	return &out, status, decodeBilling(data, &out, c.apiURL)
}
func (c *Client) BillingStatus(ctx context.Context, operationID string) (*BillingStatusResponse, *BillingOperationView, int, error) {
	if operationID != "" {
		if !ValidBillingID(operationID) {
			return nil, nil, 0, &BillingValidationError{Kind: "INVALID_OPERATION"}
		}
		data, status, err := c.doBilling(ctx, http.MethodGet, "/operations/"+url.PathEscape(operationID), nil)
		if err != nil {
			return nil, nil, status, err
		}
		var out BillingOperationView
		err = decodeBilling(data, &out, c.apiURL)
		if err == nil && !strings.EqualFold(out.OperationID, operationID) {
			err = &BillingValidationError{Kind: "INVALID_BILLING_RESPONSE"}
		}
		return nil, &out, status, err
	}
	data, status, err := c.doBilling(ctx, http.MethodGet, "/status", nil)
	if err != nil {
		return nil, nil, status, err
	}
	var out BillingStatusResponse
	return &out, nil, status, decodeBilling(data, &out, c.apiURL)
}
func (c *Client) BillingQuote(ctx context.Context, plan string) (*BillingOperationView, int, error) {
	if plan != "pro" && plan != "ultimate" {
		return nil, 0, &BillingValidationError{Kind: "INVALID_PLAN"}
	}
	data, status, err := c.doBilling(ctx, http.MethodPost, "/quotes", map[string]string{"plan": plan})
	if err != nil {
		return nil, status, err
	}
	var out BillingOperationView
	return &out, status, decodeBilling(data, &out, c.apiURL)
}
func (c *Client) BillingUpgrade(ctx context.Context, operationID string) (*BillingOperationView, int, error) {
	if !ValidBillingID(operationID) {
		return nil, 0, &BillingValidationError{Kind: "INVALID_QUOTE"}
	}
	data, status, err := c.doBilling(ctx, http.MethodPost, "/operations/"+url.PathEscape(operationID)+"/execute", map[string]any{})
	if err != nil {
		return nil, status, err
	}
	var out BillingOperationView
	err = decodeBilling(data, &out, c.apiURL)
	if err == nil && !strings.EqualFold(out.OperationID, operationID) {
		err = &BillingValidationError{Kind: "INVALID_BILLING_RESPONSE"}
	}
	return &out, status, err
}

// BillingValidationError contains only a fixed public category, never provider data.
type BillingValidationError struct{ Kind string }

func (e *BillingValidationError) Error() string { return "invalid billing protocol or configuration" }

type BillingTransportError struct{}

func (e *BillingTransportError) Error() string { return "billing transport failed" }

var billingIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var billingCurrencyPattern = regexp.MustCompile(`^[a-zA-Z]{3}$`)
var billingMonthPattern = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

func ValidBillingID(value string) bool { return billingIDPattern.MatchString(value) }
func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
func BillingReason(reason string) string {
	if oneOf(reason, "AGENT_BILLING_AUTH_REQUIRED", "AGENT_BILLING_NOT_ENABLED", "BILLING_RATE_LIMITED", "BILLING_UNAVAILABLE", "BILLING_AUTHORIZATION_REQUIRED", "PAYMENT_SETUP_REQUIRED", "PAYMENT_ACTION_REQUIRED", "PAYMENT_FAILED", "QUOTE_EXPIRED", "PAYMENT_OUTCOME_UNKNOWN", "ENTITLEMENT_SYNC_PENDING", "CURRENT_ENTITLEMENT_UNAVAILABLE", "BILLING_MANUAL_REVIEW_REQUIRED", "BILLING_PENDING") {
		return reason
	}
	return "BILLING_UNAVAILABLE"
}

// Accept only the fixed owner page and a registration UUID; discard unrelated
// query data rather than allowing credentials or provider URLs into output.
func safeBillingOwnerURL(raw, base string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Path != "/agent/billing" || u.RawPath != "" {
		return ""
	}
	official := u.Scheme == "https" && (u.Host == "www.ttsbuddy.com" || u.Host == "ttsbuddy.com")
	same := sameOrigin(u, apiOrigin(base)) && (u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHost(u.Hostname())))
	if !official && !same {
		return ""
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["registration"]) != 1 || !ValidBillingID(q.Get("registration")) {
		return ""
	}
	u.RawQuery = url.Values{"registration": {q.Get("registration")}}.Encode()
	return u.String()
}

// Hosted invoice URLs are intentional owner payment handoffs. They are never
// accepted as account authorization destinations or followed by the CLI.
func safeBillingInvoiceURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "invoice.stripe.com" || u.User != nil || u.Fragment != "" || u.RawPath != "" || !regexp.MustCompile(`^/i/[A-Za-z0-9_/-]+$`).MatchString(u.Path) {
		return ""
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ""
	}
	if len(q) != 0 && (len(q) != 1 || len(q["s"]) != 1 || q.Get("s") != "ap") {
		return ""
	}
	return u.String()
}

// Manual review links are data for a human; the CLI never opens or sends mail.
func billingSupportOperation(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "mailto" || u.Opaque != "support@ttsbuddy.com" || u.Host != "" || u.Path != "" || u.User != nil || u.Fragment != "" {
		return ""
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["subject"]) != 1 {
		return ""
	}
	subject := q.Get("subject")
	if !strings.HasPrefix(subject, "Billing operation ") {
		return ""
	}
	id := strings.TrimPrefix(subject, "Billing operation ")
	if !ValidBillingID(id) {
		return ""
	}
	return id
}

func BillingManualReviewAction(operationID string) *BillingAction {
	if !ValidBillingID(operationID) {
		return nil
	}
	return &BillingAction{Type: "contact_support", URL: "mailto:support@ttsbuddy.com?subject=Billing%20operation%20" + operationID}
}

func SanitizeBillingAction(action *BillingAction, base string, reasons ...string) *BillingAction {
	if action == nil {
		return nil
	}
	switch action.Type {
	case "contact_support":
		if len(reasons) != 1 || reasons[0] != "BILLING_MANUAL_REVIEW_REQUIRED" || len(action.Argv) != 0 {
			return nil
		}
		return BillingManualReviewAction(billingSupportOperation(action.URL))
	case "billing_status":
		a := action.Argv
		if action.URL != "" || len(a) != 5 || a[0] != "ttsbuddy" || a[1] != "billing" || a[2] != "status" || a[3] != "--operation" || !ValidBillingID(a[4]) {
			return nil
		}
		return &BillingAction{Type: "billing_status", Argv: []string{"ttsbuddy", "billing", "status", "--operation", a[4]}}
	case "owner_billing_authorization", "payment_action_required":
		if action.Type == "payment_action_required" && len(action.Argv) == 0 {
			if safe := safeBillingInvoiceURL(action.URL); safe != "" {
				return &BillingAction{Type: action.Type, URL: safe}
			}
		}
		if len(action.Argv) != 0 {
			return nil
		}
		if safe := safeBillingOwnerURL(action.URL, base); safe != "" {
			return &BillingAction{Type: action.Type, URL: safe}
		}
	}
	return nil
}

func validBillingDate(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}
func validateBillingOperation(v *BillingOperationView, base string) bool {
	if !ValidBillingID(v.OperationID) || !oneOf(v.State, "quoted", "processing", "requires_action", "payment_pending", "syncing", "succeeded", "failed", "expired", "cancelled", "unknown") || !oneOf(v.TargetPlan, "pro", "ultimate") || (v.EntitlementReady && v.State != "succeeded") {
		return false
	}
	if v.Reason != "" {
		v.Reason = BillingReason(v.Reason)
	}
	if v.Reason == "BILLING_MANUAL_REVIEW_REQUIRED" {
		if v.EntitlementReady {
			return false
		}
	} else {
		switch v.State {
		case "succeeded":
			if !v.EntitlementReady {
				v.Reason = "CURRENT_ENTITLEMENT_UNAVAILABLE"
			}
		case "requires_action", "payment_pending":
			v.Reason = "PAYMENT_ACTION_REQUIRED"
		case "failed":
			v.Reason = "PAYMENT_FAILED"
		case "expired":
			v.Reason = "QUOTE_EXPIRED"
		}
	}
	v.Action = SanitizeBillingAction(v.Action, base, v.Reason)
	if v.Action != nil && v.Action.Type == "contact_support" && !strings.EqualFold(billingSupportOperation(v.Action.URL), v.OperationID) {
		v.Action = nil
	}
	if v.Action != nil && v.Action.Type == "billing_status" && !strings.EqualFold(v.Action.Argv[4], v.OperationID) {
		v.Action = nil
	}
	if q := v.Quote; q != nil {
		if !validBillingDate(q.ExpiresAt) || !billingCurrencyPattern.MatchString(q.Currency) || q.Interval != "month" || !oneOf(q.TaxBehavior, "inclusive", "exclusive", "unspecified") {
			return false
		}
	}
	return true
}
func validateBillingResponse(value any, base string) error {
	valid := true
	switch v := value.(type) {
	case *BillingOperationView:
		valid = validateBillingOperation(v, base)
	case *BillingPlansResponse:
		if v.Plans == nil {
			valid = false
		}
		for i := range v.Plans {
			p := &v.Plans[i]
			if !oneOf(p.Name, "pro", "ultimate") || !billingCurrencyPattern.MatchString(p.Currency) || p.Interval != "month" || !oneOf(p.TaxBehavior, "inclusive", "exclusive", "unspecified") {
				valid = false
			}
			// Display labels are canonical, not arbitrary provider text.
			if p.Name == "pro" {
				p.DisplayName = "Pro"
			} else {
				p.DisplayName = "Ultimate"
			}
		}
	case *BillingStatusResponse:
		valid = ValidBillingID(v.RegistrationID) && oneOf(v.Source.Tier, "free", "pro", "ultimate", "enterprise") && oneOf(v.Source.Status, "active", "trialing", "canceled", "past_due", "unpaid", "incomplete", "incomplete_expired", "paused", "unknown") && billingMonthPattern.MatchString(v.Usage.Month) && oneOf(v.Authorization.State, "awaiting_setup", "active", "reserved", "consumed", "revoked", "expired")
		if v.Authorization.ExpiresAt != "" && !validBillingDate(v.Authorization.ExpiresAt) {
			valid = false
		}
		v.Authorization.OwnerActionURL = safeBillingOwnerURL(v.Authorization.OwnerActionURL, base)
		for i := range v.Operations {
			if !validateBillingOperation(&v.Operations[i], base) {
				valid = false
			}
		}
	default:
		valid = false
	}
	if !valid {
		return &BillingValidationError{Kind: "INVALID_BILLING_RESPONSE"}
	}
	return nil
}
