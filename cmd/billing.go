package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/spf13/cobra"
)

var billingPlan string
var billingQuoteID string
var billingOperationID string

var billingCmd = &cobra.Command{Use: "billing", Short: "Inspect and explicitly execute agent billing operations"}

var billingPlansCmd = &cobra.Command{
	Use: "plans", Short: "List eligible monthly billing plans", Args: noArgs,
	RunE: func(cmd *cobra.Command, args []string) error { return runBillingPlans(cmd.Context()) },
}

var billingStatusCmd = &cobra.Command{
	Use: "status", Short: "Show billing authorization, quota and operation status", Args: noArgs,
	RunE: func(cmd *cobra.Command, args []string) error { return runBillingStatus(cmd.Context()) },
}

var billingQuoteCmd = &cobra.Command{
	Use: "quote", Short: "Create a read-only quote for one eligible upgrade", Args: noArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(billingPlan) == "" {
			return structuredExitError(2, "--plan is required", "CLI_ERROR", "PLAN_REQUIRED", "Use --plan pro or --plan ultimate.", false, 0)
		}
		return runBillingQuote(cmd.Context(), billingPlan)
	},
}

var billingUpgradeCmd = &cobra.Command{
	Use: "upgrade", Short: "Explicitly execute one server-owned billing quote", Args: noArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(billingQuoteID) == "" {
			return structuredExitError(2, "--quote is required", "CLI_ERROR", "QUOTE_REQUIRED", "Use the operation ID returned by ttsbuddy billing quote.", false, 0)
		}
		return runBillingUpgrade(cmd.Context(), billingQuoteID)
	},
}

func init() {
	billingQuoteCmd.Flags().StringVar(&billingPlan, "plan", "", "target monthly plan (pro or ultimate)")
	billingUpgradeCmd.Flags().StringVar(&billingQuoteID, "quote", "", "server-created quote operation ID")
	billingStatusCmd.Flags().StringVar(&billingOperationID, "operation", "", "show one operation instead of account status")
	billingCmd.AddCommand(billingPlansCmd, billingStatusCmd, billingQuoteCmd, billingUpgradeCmd)
	rootCmd.AddCommand(billingCmd)
}

func billingClient() (*api.Client, error) {
	if resolvedCfg == nil {
		return nil, structuredExitError(1, "config not loaded", "CLI_ERROR", "INVALID_CONFIGURATION", "Run ttsbuddy doctor.", false, 0)
	}
	if strings.TrimSpace(resolvedCfg.APIKey) == "" {
		missing := structuredExitError(2, "Billing requires an approved agent session credential.", "CLI_ERROR", "AGENT_BILLING_AUTH_REQUIRED", "Use a ttsa_ agent session from https://www.ttsbuddy.com/auth.md; supply it with --key or TTSBUDDY_API_KEY.", false, 0)
		missing.action = requiredAction(actionAuthenticate, "agent_session_credential")
		return nil, missing
	}
	return api.NewClient(resolvedCfg.APIURL, resolvedCfg.APIKey, Version), nil
}

func billingMinorAmount(value *int) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *value)
}

func emitBilling(value any) error {
	// Preserve the caller's configuration context for read-only recovery commands,
	// including actions nested in successful status responses.
	contextualize := func(operation *api.BillingOperationView) {
		if operation.Action == nil {
			return
		}
		action := billingActionFromAPI(operation.Action, operation.Reason)
		if action == nil {
			operation.Action = nil
			return
		}
		operation.Action = &api.BillingAction{Type: action.Type, URL: action.URL, Argv: action.Argv}
	}
	switch typed := value.(type) {
	case *api.BillingOperationView:
		contextualize(typed)
	case *api.BillingStatusResponse:
		for i := range typed.Operations {
			contextualize(&typed.Operations[i])
		}
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	var output strings.Builder
	switch typed := value.(type) {
	case *api.BillingPlansResponse:
		for _, plan := range typed.Plans {
			_, _ = fmt.Fprintf(&output, "%s\t%s %s\t%s\t%d\n", plan.Name, plan.Currency, plan.Interval, plan.TaxBehavior, plan.RecurringUnitAmount)
		}
	case *api.BillingStatusResponse:
		_, _ = fmt.Fprintf(&output, "registration: %s\n", typed.RegistrationID)
		_, _ = fmt.Fprintf(&output, "source: %s (%s)\n", typed.Source.Tier, typed.Source.Status)
		_, _ = fmt.Fprintf(&output, "usage: %.2f used", typed.Usage.TTSMinutesUsed)
		if typed.Usage.UnlimitedMinutes {
			_, _ = fmt.Fprintln(&output, ", unlimited")
		} else if typed.Usage.ProjectedRemainingMinutes != nil {
			_, _ = fmt.Fprintf(&output, ", %.2f remaining\n", *typed.Usage.ProjectedRemainingMinutes)
		} else {
			_, _ = fmt.Fprintln(&output)
		}
		_, _ = fmt.Fprintf(&output, "authorization: %s\n", typed.Authorization.State)
		if typed.Authorization.OwnerActionURL != "" {
			_, _ = fmt.Fprintf(&output, "action: %s\n", typed.Authorization.OwnerActionURL)
		}
		_, _ = fmt.Fprintf(&output, "operations: %d\n", len(typed.Operations))
	case *api.BillingOperationView:
		_, _ = fmt.Fprintf(&output, "%s: %s (%s)\n", typed.OperationID, typed.State, typed.TargetPlan)
		_, _ = fmt.Fprintf(&output, "current entitlement ready: %t\n", typed.EntitlementReady)
		if typed.Quote != nil {
			_, _ = fmt.Fprintf(&output, "quote (minor currency units): %s %d subtotal, tax %s, total %s, renewal %d/%s, estimate=%t\n", typed.Quote.Currency, typed.Quote.ImmediateSubtotalMinor, billingMinorAmount(typed.Quote.ImmediateTaxMinor), billingMinorAmount(typed.Quote.ImmediateTotalMinor), typed.Quote.RecurringBaseMinor, typed.Quote.Interval, typed.Quote.AmountIsEstimate)
		}
		if typed.Action != nil && typed.Action.URL != "" {
			_, _ = fmt.Fprintf(&output, "action: %s\n", typed.Action.URL)
		}
	}
	_, err := fmt.Fprint(os.Stdout, output.String())
	return err
}

func runBillingPlans(ctx context.Context) error {
	client, err := billingClient()
	if err != nil {
		return err
	}
	result, _, requestErr := client.BillingPlans(ctx)
	if requestErr != nil {
		return classifyBillingCLIError(requestErr, "", false)
	}
	return emitBilling(result)
}

func runBillingStatus(ctx context.Context) error {
	client, err := billingClient()
	if err != nil {
		return err
	}
	result, operation, _, requestErr := client.BillingStatus(ctx, billingOperationID)
	if requestErr != nil {
		return classifyBillingCLIError(requestErr, billingOperationID, false)
	}
	if operation != nil {
		return emitBilling(operation)
	}
	return emitBilling(result)
}

func runBillingQuote(ctx context.Context, plan string) error {
	if plan != "pro" && plan != "ultimate" {
		return structuredExitError(2, "--plan must be pro or ultimate", "CLI_ERROR", "INVALID_PLAN", "Use --plan pro or --plan ultimate.", false, 0)
	}
	client, err := billingClient()
	if err != nil {
		return err
	}
	result, _, requestErr := client.BillingQuote(ctx, plan)
	if requestErr != nil {
		return classifyBillingCLIError(requestErr, "", false)
	}
	return emitBilling(result)
}

func runBillingUpgrade(ctx context.Context, operationID string) error {
	if !api.ValidBillingID(operationID) {
		return structuredExitError(2, "--quote must be a server-created operation ID", "CLI_ERROR", "INVALID_QUOTE", "Use the operation ID returned by ttsbuddy billing quote.", false, 0)
	}
	client, err := billingClient()
	if err != nil {
		return err
	}
	result, _, requestErr := client.BillingUpgrade(ctx, operationID)
	if requestErr != nil {
		return classifyBillingCLIError(requestErr, operationID, true)
	}
	if result == nil {
		return structuredExitError(1, "billing upgrade returned no operation", "BILLING_ERROR", "BILLING_UNAVAILABLE", "Run ttsbuddy billing status --operation "+operationID+".", true, 0)
	}
	if result.Reason == "BILLING_MANUAL_REVIEW_REQUIRED" {
		mapped := structuredExitError(1, "This billing operation requires manual support review.", "BILLING_ERROR", "BILLING_MANUAL_REVIEW_REQUIRED", "Contact support about this operation and wait for review before attempting another purchase or resuming synthesis.", false, 0)
		mapped.action = billingActionFromAPI(api.BillingManualReviewAction(operationID), result.Reason)
		if flagJSON {
			mapped.jsonPayload = billingErrorPayload(mapped, result)
		}
		return mapped
	}
	if result.State == "succeeded" && !result.EntitlementReady {
		mapped := structuredExitError(1, "The historical billing operation succeeded, but current entitlement is unavailable.", "BILLING_ERROR", "CURRENT_ENTITLEMENT_UNAVAILABLE", "Inspect current account billing status before requesting a new quote or resuming synthesis.", false, 0)
		mapped.action = billingStatusAction("")
		if flagJSON {
			mapped.jsonPayload = billingErrorPayload(mapped, result)
		}
		return mapped
	}
	if result.State != "succeeded" || !result.EntitlementReady {
		terminal := result.State == "failed" || result.State == "expired" || result.State == "cancelled"
		reason := api.BillingReason(result.Reason)
		if reason == "" {
			reason = "BILLING_PENDING"
		}
		code := "BILLING_PENDING"
		message := "billing upgrade is not complete"
		next := "Run ttsbuddy billing status --operation " + operationID + "."
		if terminal {
			code = "BILLING_TERMINAL"
			message = "billing upgrade ended without entitlement"
			next = "Request a new quote after resolving the billing state."
		}
		human := reason == "PAYMENT_ACTION_REQUIRED" || reason == "BILLING_AUTHORIZATION_REQUIRED" || reason == "PAYMENT_SETUP_REQUIRED"
		if human {
			next = "Complete the owner billing action before checking operation status."
		}
		mapped := structuredExitError(1, message, code, reason, next, !terminal && !human, 0)
		mapped.action = billingActionFromAPI(result.Action, result.Reason)
		if mapped.action == nil {
			mapped.action = billingStatusAction(operationID)
		}
		if flagJSON {
			mapped.jsonPayload = billingErrorPayload(mapped, result)
		}
		return mapped
	}
	return emitBilling(result)
}

func billingErrorPayload(err *exitError, operation *api.BillingOperationView) api.CLIError {
	payload := structuredErrorPayload(err)
	if operation != nil {
		payload.Error.Details = map[string]any{"operation_id": operation.OperationID, "state": operation.State}
	}
	return payload
}

func classifyBillingCLIError(err error, operationID string, executing bool) error {
	var invalid *api.BillingValidationError
	if errors.As(err, &invalid) {
		mapped := structuredExitError(1, "Invalid billing response or configuration.", "BILLING_ERROR", invalid.Kind, "Check configuration and the billing API contract before retrying.", false, 0)
		if api.ValidBillingID(operationID) {
			mapped.action = billingStatusAction(operationID)
			mapped.nextAction = "Check operation status before attempting execution again."
		}
		return mapped
	}
	if httpErr, ok := err.(*api.BillingHTTPError); ok {
		reason := api.BillingReason(httpErr.Reason)
		if reason == "" {
			reason = "BILLING_UNAVAILABLE"
		}
		status := httpErr.StatusCode
		message := "Billing request failed."
		next := "Run ttsbuddy billing status"
		retryable := status >= 500 || status == http.StatusTooManyRequests
		if operationID != "" {
			next = "Run ttsbuddy billing status --operation " + operationID
		}
		mapped := structuredExitError(1, message, "BILLING_ERROR", reason, next, retryable, 0)
		if executing && status >= 500 {
			mapped.retryable = false
		}
		if reason == "AGENT_BILLING_AUTH_REQUIRED" {
			mapped.nextAction = "Use a ttsa_ agent session from the approved owner registration; see https://www.ttsbuddy.com/auth.md"
			mapped.action = requiredAction(actionAuthenticate, "agent_session_credential")
		}
		if reason == "BILLING_AUTHORIZATION_REQUIRED" || reason == "PAYMENT_SETUP_REQUIRED" || reason == "PAYMENT_ACTION_REQUIRED" {
			mapped.retryable = false
			mapped.nextAction = "Complete the owner billing action before retrying."
			mapped.action = billingActionFromAPI(httpErr.Action, httpErr.Reason)
			if mapped.action == nil {
				mapped.action = accountAction("")
			}
		}
		if reason == "BILLING_MANUAL_REVIEW_REQUIRED" {
			mapped.retryable = false
			mapped.nextAction = "Contact support about this operation and wait for review before attempting another purchase or resuming synthesis."
			mapped.action = billingActionFromAPI(httpErr.Action, reason)
			if api.ValidBillingID(operationID) {
				mapped.action = billingActionFromAPI(api.BillingManualReviewAction(operationID), reason)
			}
			if mapped.action == nil {
				mapped.action = requiredAction("contact_support", "billing_operation_id")
			}
			payload := structuredErrorPayload(mapped)
			if api.ValidBillingID(operationID) {
				payload.Error.Details = map[string]any{"operation_id": operationID}
			}
			mapped.jsonPayload = payload
		}
		if mapped.action == nil {
			mapped.action = billingActionFromAPI(httpErr.Action, httpErr.Reason)
		}
		if operationID != "" && mapped.action == nil {
			mapped.action = billingStatusAction(operationID)
		}
		return mapped
	}
	mapped := structuredExitError(1, "Billing request failed.", "BILLING_ERROR", "TRANSPORT_ERROR", "Retry the same billing command.", true, 0)
	if operationID != "" {
		if executing {
			mapped.reason = "EXECUTION_OUTCOME_UNKNOWN"
			mapped.retryable = false
		}
		mapped.nextAction = "Run ttsbuddy billing status --operation " + operationID + "."
		mapped.action = billingStatusAction(operationID)
	}
	return mapped
}
