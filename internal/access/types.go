package access

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/wallet"
)

const (
	StarterAllowanceUnits    int64 = 500_000
	StarterRequestLimitUnits int64 = 100_000
)

var ErrInvalidAccessPassReceipt = errors.New("invalid access pass receipt")

type EndpointOptions struct {
	AllowCustomAPIURL bool
}

type Endpoints struct {
	AgentTTS  *url.URL
	Plans     *url.URL
	Purchases *url.URL
	Current   *url.URL
}

type PlansResponse struct {
	Plans []Plan `json:"plans"`
}

type Plan struct {
	SKU               string    `json:"sku"`
	Version           int       `json:"version"`
	Price             PlanPrice `json:"price"`
	AllowanceUnits    int64     `json:"allowance_units"`
	RequestLimitUnits int64     `json:"request_limit_units"`
	ValidForSeconds   int64     `json:"valid_for_seconds"`
	VoicePolicy       string    `json:"voice_policy"`
}

type PlanPrice struct {
	Display       string `json:"display"`
	Atomic        string `json:"atomic"`
	Asset         string `json:"asset"`
	AssetDecimals int    `json:"asset_decimals"`
	Network       string `json:"network"`
	PayTo         string `json:"pay_to"`
}

type BuyRequest struct {
	SKU      string
	MaxPrice string
	Signer   wallet.Signer
}

type PurchaseResult struct {
	Pass              string          `json:"pass"`
	Status            string          `json:"status"`
	AllowanceUnits    int64           `json:"allowance_units"`
	ReservedUnits     int64           `json:"reserved_units"`
	ConsumedUnits     int64           `json:"consumed_units"`
	RemainingUnits    int64           `json:"remaining_units"`
	RequestLimitUnits int64           `json:"request_limit_units"`
	ExpiresAt         time.Time       `json:"expires_at"`
	Receipt           PurchaseReceipt `json:"receipt"`
}

type StatusResult struct {
	Status            string          `json:"status"`
	AllowanceUnits    int64           `json:"allowance_units"`
	ReservedUnits     int64           `json:"reserved_units"`
	ConsumedUnits     int64           `json:"consumed_units"`
	RemainingUnits    int64           `json:"remaining_units"`
	RequestLimitUnits int64           `json:"request_limit_units"`
	ExpiresAt         time.Time       `json:"expires_at"`
	Plan              Plan            `json:"plan"`
	Receipt           PurchaseReceipt `json:"receipt"`
}

type PurchaseReceipt struct {
	PurchaseID  string `json:"purchase_id"`
	Network     string `json:"network"`
	Transaction string `json:"transaction"`
	Asset       string `json:"asset"`
	Amount      string `json:"amount"`
	Payer       string `json:"payer,omitempty"`
}

func (p PlansResponse) validate() error {
	if len(p.Plans) == 0 {
		return errors.New("access pass plan response has no plans")
	}
	for _, plan := range p.Plans {
		if err := plan.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p Plan) validate() error {
	switch {
	case strings.TrimSpace(p.SKU) == "":
		return errors.New("access pass plan missing sku")
	case p.Version <= 0:
		return errors.New("access pass plan missing version")
	case !isAtomicAmount(p.Price.Atomic):
		return errors.New("access pass plan has invalid atomic price")
	case strings.TrimSpace(p.Price.Asset) == "":
		return errors.New("access pass plan missing asset")
	case p.Price.AssetDecimals < 0 || p.Price.AssetDecimals > 36:
		return errors.New("access pass plan has invalid asset decimals")
	case strings.TrimSpace(p.Price.Network) == "":
		return errors.New("access pass plan missing network")
	case strings.TrimSpace(p.Price.PayTo) == "":
		return errors.New("access pass plan missing payee")
	case p.AllowanceUnits <= 0 || p.RequestLimitUnits <= 0 || p.RequestLimitUnits > p.AllowanceUnits:
		return errors.New("access pass plan has invalid unit limits")
	case p.ValidForSeconds <= 0:
		return errors.New("access pass plan missing validity")
	case strings.TrimSpace(p.VoicePolicy) == "":
		return errors.New("access pass plan missing voice policy")
	default:
		return nil
	}
}

func (r PurchaseResult) validateSuccess(plan Plan, settlementNetwork, settlementTransaction, settlementAmount string, now time.Time) error {
	if !isAccessPassCredential(r.Pass) {
		return fmt.Errorf("%w: pass credential missing or malformed", ErrInvalidAccessPassReceipt)
	}
	if err := validateCommonPassReceipt(r.Status, r.AllowanceUnits, r.ReservedUnits, r.ConsumedUnits, r.RemainingUnits, r.RequestLimitUnits, r.ExpiresAt, r.Receipt, plan, settlementNetwork, settlementTransaction, settlementAmount, now); err != nil {
		return err
	}
	return nil
}

func (r StatusResult) validate() error {
	if r.Status == "" {
		return errors.New("access pass status missing status")
	}
	if r.AllowanceUnits <= 0 || r.RequestLimitUnits <= 0 || r.RequestLimitUnits > r.AllowanceUnits {
		return errors.New("access pass status has invalid unit limits")
	}
	if r.RemainingUnits != r.AllowanceUnits-r.ReservedUnits-r.ConsumedUnits {
		return errors.New("access pass status has inconsistent unit counters")
	}
	if r.ExpiresAt.IsZero() {
		return errors.New("access pass status missing expiry")
	}
	if r.Receipt.PurchaseID == "" || r.Receipt.Network == "" || r.Receipt.Transaction == "" || r.Receipt.Asset == "" || r.Receipt.Amount == "" {
		return errors.New("access pass status missing receipt")
	}
	if err := r.Plan.validate(); err != nil {
		return err
	}
	return nil
}

func validateCommonPassReceipt(status string, allowance, reserved, consumed, remaining, requestLimit int64, expiresAt time.Time, receipt PurchaseReceipt, plan Plan, settlementNetwork, settlementTransaction, settlementAmount string, now time.Time) error {
	switch {
	case status != "active":
		return fmt.Errorf("%w: pass status is not active", ErrInvalidAccessPassReceipt)
	case allowance != StarterAllowanceUnits:
		return fmt.Errorf("%w: allowance_units must be 500000", ErrInvalidAccessPassReceipt)
	case requestLimit != StarterRequestLimitUnits:
		return fmt.Errorf("%w: request_limit_units must be 100000", ErrInvalidAccessPassReceipt)
	case reserved != 0:
		return fmt.Errorf("%w: reserved_units must be zero", ErrInvalidAccessPassReceipt)
	case consumed != 0:
		return fmt.Errorf("%w: consumed_units must be zero", ErrInvalidAccessPassReceipt)
	case remaining != allowance:
		return fmt.Errorf("%w: remaining_units must equal allowance", ErrInvalidAccessPassReceipt)
	case !expiresAt.After(now):
		return fmt.Errorf("%w: expires_at is not in the future", ErrInvalidAccessPassReceipt)
	case receipt.PurchaseID == "":
		return fmt.Errorf("%w: purchase_id missing", ErrInvalidAccessPassReceipt)
	case receipt.Network != plan.Price.Network || receipt.Network != settlementNetwork:
		return fmt.Errorf("%w: settlement network mismatch", ErrInvalidAccessPassReceipt)
	case receipt.Transaction == "" || receipt.Transaction != settlementTransaction:
		return fmt.Errorf("%w: settlement transaction mismatch", ErrInvalidAccessPassReceipt)
	case receipt.Asset != plan.Price.Asset:
		return fmt.Errorf("%w: asset mismatch", ErrInvalidAccessPassReceipt)
	case receipt.Amount != plan.Price.Atomic || receipt.Amount != settlementAmount:
		return fmt.Errorf("%w: amount mismatch", ErrInvalidAccessPassReceipt)
	default:
		return nil
	}
}

func isAccessPassCredential(value string) bool {
	parts := strings.Split(value, "_")
	if len(parts) != 3 || parts[0] != "ttsp" || len(parts[1]) != 8 || len(parts[2]) != 48 {
		return false
	}
	for _, part := range parts[1:] {
		for _, r := range part {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return false
			}
		}
	}
	return true
}

func decodeStrict(data []byte, out interface{ validate() error }) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return out.validate()
}

func isAtomicAmount(value string) bool {
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
