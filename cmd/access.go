package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/access"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/ngelik/ttsbuddy-cli/internal/wallet"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	accessStarterSKU          = "starter"
	accessPassLocalUnitLimit  = 100_000
	accessWalletLocal         = "local"
	accessWalletCDP           = "cdp"
	accessBuyCommandHint      = "ttsbuddy access buy starter --wallet <local|cdp> --max-price <decimal>"
	localWalletEnvInstruction = "Set TTSBUDDY_EVM_PRIVATE_KEY to an existing funded EVM private key."
	cdpWalletEnvInstruction   = "Set CDP_API_KEY_ID, CDP_API_KEY_SECRET, CDP_WALLET_SECRET, and TTSBUDDY_CDP_EVM_ACCOUNT_ADDRESS for an existing funded CDP EVM account."
)

var (
	accessBuyWallet   string
	accessBuyMaxPrice string

	accessStdout io.Writer = os.Stdout
	accessStderr io.Writer = os.Stderr
)

type accessCLIClient interface {
	Plans(context.Context) (*access.PlansResponse, error)
	Buy(context.Context, access.BuyRequest) (*access.PurchaseResult, error)
	Status(context.Context) (*access.StatusResult, error)
}

var (
	newAccessClientForCLI = func(agentURL, bearer string, allowCustom bool) (accessCLIClient, error) {
		return access.NewClient(access.ClientOptions{
			AgentURL:          agentURL,
			Bearer:            bearer,
			Version:           Version,
			AllowCustomAPIURL: allowCustom,
		})
	}
	newAccessLocalSigner   = wallet.NewLocalFromEnvironment
	newAccessCDPSigner     = wallet.NewCDPFromEnvironment
	saveAccessPassForCLI   = config.SaveAccessPass
	forgetAccessPassForCLI = config.ForgetAccessPass
)

var accessCmd = &cobra.Command{
	Use:          "access",
	Short:        "Manage prepaid access passes",
	Args:         noArgs,
	SilenceUsage: true,
}

var accessPlansCmd = &cobra.Command{
	Use:               "plans",
	Short:             "List prepaid access pass plans",
	Args:              noArgs,
	SilenceUsage:      true,
	PersistentPreRunE: accessSkipRootConfig,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAccessPlans(cmd)
	},
}

var accessBuyCmd = &cobra.Command{
	Use:               "buy starter",
	Short:             "Buy the starter prepaid access pass",
	Args:              exactArgs(1),
	SilenceUsage:      true,
	PersistentPreRunE: accessSkipRootConfig,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAccessBuy(cmd, args)
	},
}

var accessStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show prepaid access pass status",
	Args:         noArgs,
	SilenceUsage: true,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		return rejectAccessKeyFlag(cmd, "access status")
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAccessStatus(cmd)
	},
}

var accessForgetCmd = &cobra.Command{
	Use:          "forget",
	Short:        "Forget the locally stored prepaid access pass",
	Args:         noArgs,
	SilenceUsage: true,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		return rejectAccessKeyFlag(cmd, "access forget")
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAccessForget(cmd)
	},
}

func init() {
	accessBuyCmd.Flags().StringVar(&accessBuyWallet, "wallet", "", "wallet signer to use (local or cdp)")
	accessBuyCmd.Flags().StringVar(&accessBuyMaxPrice, "max-price", "", "maximum acceptable price as a decimal")

	accessCmd.AddCommand(accessPlansCmd, accessBuyCmd, accessStatusCmd, accessForgetCmd)
	rootCmd.AddCommand(accessCmd)
}

func accessSkipRootConfig(cmd *cobra.Command, args []string) error {
	resolvedCfg = nil
	return nil
}

func runAccessPlans(cmd *cobra.Command) error {
	if err := rejectAccessKeyFlag(cmd, "access plans"); err != nil {
		return err
	}
	agentURL, allowCustom, err := accessEndpointSettingsFromEnv()
	if err != nil {
		return err
	}
	client, err := newAccessClientForCLI(agentURL, "", allowCustom)
	if err != nil {
		return &exitError{code: 1, msg: sanitizeAccessMessage(err.Error())}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	plans, err := client.Plans(ctx)
	if err != nil {
		return &exitError{code: 1, msg: sanitizeAccessMessage(err.Error())}
	}
	if flagJSON {
		return writeAccessJSON(plans)
	}
	renderAccessPlans(plans)
	return nil
}

func runAccessBuy(cmd *cobra.Command, args []string) error {
	if err := rejectAccessKeyFlag(cmd, "access buy"); err != nil {
		return err
	}
	if len(args) != 1 || args[0] != accessStarterSKU {
		return &exitError{code: 2, msg: `access buy supports only "starter": ` + accessBuyCommandHint}
	}
	if !cmd.Flags().Changed("max-price") {
		return &exitError{code: 2, msg: "access buy requires --max-price <decimal>"}
	}
	if err := validatePositiveDecimal(accessBuyMaxPrice); err != nil {
		return &exitError{code: 2, msg: "--max-price " + err.Error()}
	}
	if !cmd.Flags().Changed("wallet") {
		return &exitError{code: 2, msg: "access buy requires --wallet local|cdp"}
	}
	walletName := strings.ToLower(strings.TrimSpace(accessBuyWallet))
	if walletName != accessWalletLocal && walletName != accessWalletCDP {
		return &exitError{code: 2, msg: "access buy --wallet must be local or cdp"}
	}
	if err := validateAccessWalletEnvironment(walletName); err != nil {
		return err
	}

	signer, err := accessSignerForWallet(walletName)
	if err != nil {
		return &exitError{code: 2, msg: sanitizeAccessMessage(err.Error())}
	}
	agentURL, allowCustom, err := accessEndpointSettingsFromEnv()
	if err != nil {
		return err
	}
	client, err := newAccessClientForCLI(agentURL, "", allowCustom)
	if err != nil {
		return &exitError{code: 1, msg: sanitizeAccessMessage(err.Error())}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, err := client.Buy(ctx, access.BuyRequest{SKU: accessStarterSKU, MaxPrice: accessBuyMaxPrice, Signer: signer})
	if err != nil {
		return &exitError{code: 1, msg: "access buy failed: " + sanitizeAccessMessage(err.Error())}
	}
	if result == nil {
		return &exitError{code: 1, msg: "access buy failed: empty purchase result"}
	}

	saveErr := saveAccessPassForCLI(storedPassFromPurchase(*result))
	if flagJSON {
		return writeAccessJSON(accessBuyJSONFromResult(*result, saveErr))
	}
	if saveErr != nil {
		fmt.Fprintln(accessStdout, result.Pass)
		fmt.Fprintf(accessStderr, "Warning: access pass purchase settled, but the pass could not be saved locally. Store the one-time pass printed on stdout in TTSBUDDY_ACCESS_PASS. Save error: %s\n", sanitizeAccessMessage(saveErr.Error()))
		return nil
	}

	renderAccessBuySuccess(*result)
	return nil
}

func runAccessStatus(cmd *cobra.Command) error {
	resolved := resolvedCfg
	if resolved == nil {
		return &exitError{code: 1, msg: "config not loaded"}
	}
	if resolved.CredentialKind != config.CredentialKindAccessPass || resolved.APIKey == "" {
		return &exitError{code: 2, msg: "access status requires an access pass. Set TTSBUDDY_ACCESS_PASS=ttsp_... or run: " + accessBuyCommandHint}
	}
	client, err := newAccessClientForCLI(resolved.APIURL, resolved.APIKey, resolved.AllowCustomAPIURL)
	if err != nil {
		return &exitError{code: 1, msg: sanitizeAccessMessage(err.Error())}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	status, err := client.Status(ctx)
	if err != nil {
		return &exitError{code: 1, msg: sanitizeAccessMessage(err.Error())}
	}
	if flagJSON {
		return writeAccessJSON(status)
	}
	renderAccessStatus(status)
	return nil
}

func runAccessForget(cmd *cobra.Command) error {
	resolved := resolvedCfg
	if resolved == nil {
		return &exitError{code: 1, msg: "config not loaded"}
	}
	if resolved.AccessPass == nil || strings.TrimSpace(resolved.AccessPass.Credential) == "" {
		return renderAccessForget(false)
	}
	removed, err := forgetAccessPassForCLI(resolved.AccessPass.Credential)
	if err != nil {
		return &exitError{code: 1, msg: sanitizeAccessMessage(err.Error())}
	}
	return renderAccessForget(removed)
}

type accessBuyJSON struct {
	Success           bool                   `json:"success"`
	Saved             bool                   `json:"saved"`
	SaveError         string                 `json:"save_error,omitempty"`
	Pass              string                 `json:"pass"`
	Status            string                 `json:"status"`
	AllowanceUnits    int64                  `json:"allowance_units"`
	ReservedUnits     int64                  `json:"reserved_units"`
	ConsumedUnits     int64                  `json:"consumed_units"`
	RemainingUnits    int64                  `json:"remaining_units"`
	RequestLimitUnits int64                  `json:"request_limit_units"`
	ExpiresAt         time.Time              `json:"expires_at"`
	Receipt           access.PurchaseReceipt `json:"receipt"`
}

type accessForgetJSON struct {
	Success   bool `json:"success"`
	Forgotten bool `json:"forgotten"`
}

func accessBuyJSONFromResult(result access.PurchaseResult, saveErr error) accessBuyJSON {
	pass := result.Pass
	if saveErr == nil {
		pass = config.RedactCredential(result.Pass)
	}
	out := accessBuyJSON{
		Success:           true,
		Saved:             saveErr == nil,
		Pass:              pass,
		Status:            result.Status,
		AllowanceUnits:    result.AllowanceUnits,
		ReservedUnits:     result.ReservedUnits,
		ConsumedUnits:     result.ConsumedUnits,
		RemainingUnits:    result.RemainingUnits,
		RequestLimitUnits: result.RequestLimitUnits,
		ExpiresAt:         result.ExpiresAt,
		Receipt:           result.Receipt,
	}
	if saveErr != nil {
		out.SaveError = sanitizeAccessMessage(saveErr.Error())
	}
	return out
}

func renderAccessPlans(plans *access.PlansResponse) {
	fmt.Fprintf(accessStdout, "%-12s %-15s %-14s %-14s %-14s %s\n", "SKU", "PRICE", "NETWORK", "ALLOWANCE", "REQUEST", "VALID")
	for _, plan := range plans.Plans {
		price := strings.TrimSpace(plan.Price.Display)
		if price == "" {
			price = plan.Price.Atomic
		}
		if plan.Price.Asset != "" && !strings.Contains(price, plan.Price.Asset) {
			price += " " + plan.Price.Asset
		}
		fmt.Fprintf(accessStdout, "%-12s %-15s %-14s %-14s %-14s %s\n",
			plan.SKU,
			price,
			plan.Price.Network,
			formatUnits(plan.AllowanceUnits),
			formatUnits(plan.RequestLimitUnits),
			formatAccessValidity(plan.ValidForSeconds),
		)
	}
}

func renderAccessBuySuccess(result access.PurchaseResult) {
	fmt.Fprintf(accessStdout, "Access pass saved: %s\n", config.RedactCredential(result.Pass))
	fmt.Fprintf(accessStdout, "Status: %s\n", result.Status)
	fmt.Fprintf(accessStdout, "Remaining units: %s / %s\n", formatUnits(result.RemainingUnits), formatUnits(result.AllowanceUnits))
	fmt.Fprintf(accessStdout, "Per-request limit: %s\n", formatUnits(result.RequestLimitUnits))
	fmt.Fprintf(accessStdout, "Expires: %s\n", result.ExpiresAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(accessStdout, "Purchase ID: %s\n", result.Receipt.PurchaseID)
}

func renderAccessStatus(status *access.StatusResult) {
	fmt.Fprintf(accessStdout, "Status: %s\n", status.Status)
	fmt.Fprintf(accessStdout, "Remaining units: %s / %s\n", formatUnits(status.RemainingUnits), formatUnits(status.AllowanceUnits))
	fmt.Fprintf(accessStdout, "Reserved units: %s\n", formatUnits(status.ReservedUnits))
	fmt.Fprintf(accessStdout, "Consumed units: %s\n", formatUnits(status.ConsumedUnits))
	fmt.Fprintf(accessStdout, "Per-request limit: %s\n", formatUnits(status.RequestLimitUnits))
	fmt.Fprintf(accessStdout, "Expires: %s\n", status.ExpiresAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(accessStdout, "Plan: %s\n", status.Plan.SKU)
	fmt.Fprintf(accessStdout, "Purchase ID: %s\n", status.Receipt.PurchaseID)
}

func renderAccessForget(removed bool) error {
	if flagJSON {
		return writeAccessJSON(accessForgetJSON{Success: true, Forgotten: removed})
	}
	if removed {
		fmt.Fprintln(accessStdout, "Access pass removed from local config.")
	} else {
		fmt.Fprintln(accessStdout, "No stored access pass to forget.")
	}
	return nil
}

func writeAccessJSON(value any) error {
	enc := json.NewEncoder(accessStdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func accessEndpointSettingsFromEnv() (string, bool, error) {
	agentURL := config.DefaultAPIURL
	if v := strings.TrimSpace(os.Getenv("TTSBUDDY_API_URL")); v != "" {
		agentURL = v
	}
	allowCustom := false
	if v := strings.TrimSpace(os.Getenv("TTSBUDDY_ALLOW_CUSTOM_API_URL")); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return "", false, &exitError{code: 2, msg: fmt.Sprintf("invalid TTSBUDDY_ALLOW_CUSTOM_API_URL=%q (use true or false)", v)}
		}
		allowCustom = parsed
	}
	return agentURL, allowCustom, nil
}

func rejectAccessKeyFlag(cmd *cobra.Command, commandName string) error {
	if accessGlobalKeyChanged(cmd) {
		return &exitError{code: 2, msg: "--key is not supported by " + commandName}
	}
	return nil
}

func accessGlobalKeyChanged(cmd *cobra.Command) bool {
	for _, flags := range []*pflag.FlagSet{cmd.Flags(), cmd.InheritedFlags(), cmd.Root().PersistentFlags()} {
		if flags == nil {
			continue
		}
		if flag := flags.Lookup("key"); flag != nil && flag.Changed {
			return true
		}
	}
	return false
}

func validateAccessWalletEnvironment(walletName string) error {
	switch walletName {
	case accessWalletLocal:
		if os.Getenv("TTSBUDDY_EVM_PRIVATE_KEY") == "" {
			return &exitError{code: 2, msg: "local wallet requires TTSBUDDY_EVM_PRIVATE_KEY. " + localWalletEnvInstruction}
		}
	case accessWalletCDP:
		required := []string{"CDP_API_KEY_ID", "CDP_API_KEY_SECRET", "CDP_WALLET_SECRET", "TTSBUDDY_CDP_EVM_ACCOUNT_ADDRESS"}
		var missing []string
		for _, name := range required {
			if os.Getenv(name) == "" {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return &exitError{code: 2, msg: "cdp wallet requires " + strings.Join(missing, ", ") + ". " + cdpWalletEnvInstruction}
		}
	default:
		return &exitError{code: 2, msg: "access buy --wallet must be local or cdp"}
	}
	return nil
}

func accessSignerForWallet(walletName string) (wallet.Signer, error) {
	switch walletName {
	case accessWalletLocal:
		return newAccessLocalSigner()
	case accessWalletCDP:
		return newAccessCDPSigner()
	default:
		return nil, errors.New("access buy --wallet must be local or cdp")
	}
}

func validatePositiveDecimal(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return errors.New("must be a positive decimal")
	}
	if strings.ContainsAny(trimmed, "eE") || strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "$") {
		return errors.New("must be a positive plain decimal")
	}
	intPart, decPart, hasDot := strings.Cut(trimmed, ".")
	if intPart == "" {
		intPart = "0"
	}
	if !allASCIIDigits(intPart) || hasDot && !allASCIIDigits(decPart) {
		return errors.New("must be a positive plain decimal")
	}
	if strings.Trim(intPart+decPart, "0") == "" {
		return errors.New("must be greater than zero")
	}
	return nil
}

func allASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func storedPassFromPurchase(result access.PurchaseResult) config.StoredAccessPass {
	return config.StoredAccessPass{
		Credential:   result.Pass,
		PurchaseID:   result.Receipt.PurchaseID,
		ExpiresAt:    result.ExpiresAt,
		Network:      result.Receipt.Network,
		Allowance:    result.AllowanceUnits,
		RequestLimit: result.RequestLimitUnits,
	}
}

func formatAccessValidity(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	if seconds%86_400 == 0 {
		return fmt.Sprintf("%dd", seconds/86_400)
	}
	return (time.Duration(seconds) * time.Second).String()
}

func formatUnits(n int64) string {
	negative := n < 0
	if negative {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if negative {
		return "-" + s
	}
	return s
}

func sanitizeAccessMessage(value string) string {
	for _, prefix := range []string{"ttsp", "ttsb", "ttsc"} {
		value = redactCredentialPrefixForAccess(value, prefix)
	}
	for _, name := range []string{"TTSBUDDY_EVM_PRIVATE_KEY", "CDP_API_KEY_ID", "CDP_API_KEY_SECRET", "CDP_WALLET_SECRET", "TTSBUDDY_CDP_EVM_ACCOUNT_ADDRESS"} {
		secret := os.Getenv(name)
		if secret != "" {
			value = strings.ReplaceAll(value, secret, name+"=<redacted>")
		}
	}
	value = strings.ReplaceAll(value, "PAYMENT-SIGNATURE", "payment header")
	value = strings.ReplaceAll(value, "PAYMENT_SIGNATURE", "payment header")
	return value
}

func redactCredentialPrefixForAccess(value, prefix string) string {
	for {
		start := strings.Index(value, prefix+"_")
		if start < 0 {
			return value
		}
		end := start
		for end < len(value) && !strings.ContainsRune(" \t\r\n\"'<>", rune(value[end])) {
			end++
		}
		value = value[:start] + "[redacted " + prefix + " credential]" + value[end:]
	}
}
