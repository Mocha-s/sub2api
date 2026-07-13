package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const videoTaskMaxSeconds = 3600

// VideoTaskMaxOutputs bounds immutable video settlement snapshots. The create endpoint
// currently produces one output, while 16 leaves room for bounded multi-output adapters.
const VideoTaskMaxOutputs = 16

// These limits exceed practical representations of 1..3600 while bounding big.Rat allocation.
const (
	videoTaskMaxNumericTextLength = 128
	videoTaskMaxMantissaDigits    = 64
	videoTaskMaxExponentDigits    = 4
	videoTaskMaxExponentMagnitude = 1000
)

var videoResolutionSuffixPattern = regexp.MustCompile(`(?i)(?:^|[-_])(480p|720p|1080p|4k)$`)
var videoTaskDecimalPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

type VideoTaskEffectiveParams struct {
	Seconds    int    `json:"seconds"`
	Resolution string `json:"resolution"`
	VideoCount int    `json:"video_count"`
}

type VideoTaskQuote struct {
	BillingMode           BillingMode              `json:"billing_mode"`
	BillingModel          string                   `json:"billing_model"`
	Effective             VideoTaskEffectiveParams `json:"effective"`
	UnitPriceUSD          float64                  `json:"unit_price_usd"`
	GrossCostUSD          float64                  `json:"gross_cost_usd"`
	ActualCostUSD         float64                  `json:"actual_cost_usd"`
	AccountUnitPriceUSD   float64                  `json:"account_unit_price_usd"`
	AccountBaseCostUSD    float64                  `json:"account_base_cost_usd"`
	AccountCostUSD        float64                  `json:"account_cost_usd"`
	RateMultiplier        float64                  `json:"rate_multiplier"`
	AccountRateMultiplier float64                  `json:"account_rate_multiplier"`
}

type VideoTaskPricingResolveInput struct {
	GroupID        int64
	UserID         int64
	APIKey         *APIKey
	Account        *Account
	RequestedModel string
	UpstreamModel  string
}

type VideoTaskPricingSelection struct {
	Pricing               *ChannelModelPricing
	AccountStatsPricing   *ChannelModelPricing
	BillingModel          string
	BillingModelSource    string
	ChannelID             int64
	RateMultiplier        float64
	AccountRateMultiplier float64
}

type videoTaskPricingResolver interface {
	ResolveVideoTaskPricing(ctx context.Context, input VideoTaskPricingResolveInput) VideoTaskPricingSelection
}

func ResolveVideoTaskQuote(body []byte, billingModel string, pricing *ChannelModelPricing, rateMultiplier, accountRateMultiplier float64) (VideoTaskQuote, error) {
	if pricing == nil {
		return VideoTaskQuote{}, invalidVideoPricingConfig("matched pricing is required")
	}
	switch pricing.BillingMode {
	case BillingModeVideo:
		return resolveDurationVideoTaskQuote(body, billingModel, pricing, rateMultiplier, accountRateMultiplier)
	case BillingModePerRequest:
		return resolvePerRequestVideoTaskQuote(body, billingModel, pricing, rateMultiplier, accountRateMultiplier)
	default:
		return VideoTaskQuote{}, invalidVideoPricingConfig("matched pricing must use video or per-request billing mode")
	}
}

func resolveDurationVideoTaskQuote(body []byte, billingModel string, pricing *ChannelModelPricing, rateMultiplier, accountRateMultiplier float64) (VideoTaskQuote, error) {
	if pricing.VideoDefaultSeconds == nil || *pricing.VideoDefaultSeconds <= 0 || *pricing.VideoDefaultSeconds > videoTaskMaxSeconds {
		return VideoTaskQuote{}, invalidVideoPricingConfig("video_default_seconds must be between 1 and 3600")
	}
	if !validVideoPriceMultiplier(rateMultiplier) || !validVideoPriceMultiplier(accountRateMultiplier) {
		return VideoTaskQuote{}, invalidVideoPricingConfig("video pricing multipliers must be finite and non-negative")
	}

	payload, err := decodeVideoPricingPayload(body)
	if err != nil {
		return VideoTaskQuote{}, invalidVideoTaskRequest("invalid video create JSON: %v", err)
	}
	seconds, err := resolveVideoTaskSeconds(payload, *pricing.VideoDefaultSeconds, pricing.VideoAllowedSeconds)
	if err != nil {
		return VideoTaskQuote{}, err
	}
	resolution := resolveVideoTaskResolution(payload, billingModel)
	unitPrice, err := resolveVideoTaskUnitPrice(pricing, resolution)
	if err != nil {
		return VideoTaskQuote{}, err
	}

	grossCost, err := NormalizeVideoTaskPricingAmount(unitPrice * float64(seconds))
	if err != nil {
		return VideoTaskQuote{}, invalidVideoPricingConfig("video gross cost must be finite")
	}
	actualCost := grossCost * rateMultiplier
	if !validVideoPriceMultiplier(actualCost) {
		return VideoTaskQuote{}, invalidVideoPricingConfig("video actual cost must be finite")
	}
	accountUnitPrice, _ := decimal.NewFromFloat(unitPrice).Round(10).Float64()
	accountBaseCost, err := NormalizeVideoTaskPricingAmount(accountUnitPrice * float64(seconds))
	if err != nil {
		return VideoTaskQuote{}, invalidVideoPricingConfig("video account base cost is outside pricing precision")
	}
	accountCost := accountBaseCost * accountRateMultiplier
	if !validVideoPriceMultiplier(accountCost) {
		return VideoTaskQuote{}, invalidVideoPricingConfig("video account cost must be finite")
	}
	actualCost, err = NormalizeVideoTaskSettlementAmount(actualCost)
	if err != nil {
		return VideoTaskQuote{}, invalidVideoPricingConfig("video actual cost is outside applied precision")
	}
	accountCost, err = NormalizeVideoTaskSettlementAmount(accountCost)
	if err != nil {
		return VideoTaskQuote{}, invalidVideoPricingConfig("video account cost is outside applied precision")
	}
	return VideoTaskQuote{
		BillingMode:           BillingModeVideo,
		BillingModel:          strings.TrimSpace(billingModel),
		Effective:             VideoTaskEffectiveParams{Seconds: seconds, Resolution: resolution, VideoCount: 1},
		UnitPriceUSD:          unitPrice,
		GrossCostUSD:          grossCost,
		ActualCostUSD:         actualCost,
		AccountUnitPriceUSD:   accountUnitPrice,
		AccountBaseCostUSD:    accountBaseCost,
		AccountCostUSD:        accountCost,
		RateMultiplier:        rateMultiplier,
		AccountRateMultiplier: accountRateMultiplier,
	}, nil
}

func resolvePerRequestVideoTaskQuote(body []byte, billingModel string, pricing *ChannelModelPricing, rateMultiplier, accountRateMultiplier float64) (VideoTaskQuote, error) {
	unitPrice := 0.0
	if pricing.PerRequestPrice != nil {
		unitPrice = *pricing.PerRequestPrice
	}
	if !validVideoPriceMultiplier(unitPrice) {
		return VideoTaskQuote{}, invalidVideoPricingConfig("per-request price must be finite and non-negative")
	}
	if !validVideoPriceMultiplier(rateMultiplier) || !validVideoPriceMultiplier(accountRateMultiplier) {
		return VideoTaskQuote{}, invalidVideoPricingConfig("video pricing multipliers must be finite and non-negative")
	}

	grossCost, err := NormalizeVideoTaskPricingAmount(unitPrice)
	if err != nil {
		return VideoTaskQuote{}, invalidVideoPricingConfig("per-request gross cost must be finite")
	}
	actualCost := grossCost * rateMultiplier
	if !validVideoPriceMultiplier(actualCost) {
		return VideoTaskQuote{}, invalidVideoPricingConfig("per-request actual cost must be finite")
	}
	accountUnitPrice, _ := decimal.NewFromFloat(unitPrice).Round(10).Float64()
	accountBaseCost, err := NormalizeVideoTaskPricingAmount(accountUnitPrice)
	if err != nil {
		return VideoTaskQuote{}, invalidVideoPricingConfig("per-request account base cost is outside pricing precision")
	}
	accountCost := accountBaseCost * accountRateMultiplier
	if !validVideoPriceMultiplier(accountCost) {
		return VideoTaskQuote{}, invalidVideoPricingConfig("per-request account cost must be finite")
	}
	actualCost, err = NormalizeVideoTaskSettlementAmount(actualCost)
	if err != nil {
		return VideoTaskQuote{}, invalidVideoPricingConfig("per-request actual cost is outside applied precision")
	}
	accountCost, err = NormalizeVideoTaskSettlementAmount(accountCost)
	if err != nil {
		return VideoTaskQuote{}, invalidVideoPricingConfig("per-request account cost is outside applied precision")
	}
	payload, err := decodeVideoPricingPayload(body)
	if err != nil {
		return VideoTaskQuote{}, invalidVideoTaskRequest("invalid video create JSON: %v", err)
	}
	return VideoTaskQuote{
		BillingMode:           BillingModePerRequest,
		BillingModel:          strings.TrimSpace(billingModel),
		Effective:             VideoTaskEffectiveParams{Seconds: resolveOptionalVideoTaskSeconds(payload), Resolution: resolveOptionalVideoTaskResolution(payload), VideoCount: 1},
		UnitPriceUSD:          unitPrice,
		GrossCostUSD:          grossCost,
		ActualCostUSD:         actualCost,
		AccountUnitPriceUSD:   accountUnitPrice,
		AccountBaseCostUSD:    accountBaseCost,
		AccountCostUSD:        accountCost,
		RateMultiplier:        rateMultiplier,
		AccountRateMultiplier: accountRateMultiplier,
	}, nil
}

func decodeVideoPricingPayload(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, fmt.Errorf("body must be an object")
	}
	return payload, nil
}

func resolveVideoTaskSeconds(payload map[string]any, defaultSeconds int, allowed []int) (int, error) {
	seconds := defaultSeconds
	for _, field := range []string{"seconds", "duration", "duration_seconds"} {
		value, exists := payload[field]
		if !exists {
			continue
		}
		parsed, err := parseVideoTaskSeconds(value)
		if err != nil {
			return 0, invalidVideoTaskRequest("%s must be an integer between 1 and 3600", field)
		}
		seconds = parsed
		break
	}
	if seconds <= 0 || seconds > videoTaskMaxSeconds {
		return 0, invalidVideoTaskRequest("video seconds must be between 1 and 3600")
	}
	if len(allowed) > 0 {
		matched := false
		for _, candidate := range allowed {
			if seconds == candidate {
				matched = true
				break
			}
		}
		if !matched {
			return 0, invalidVideoTaskRequest("video seconds %d is not allowed", seconds)
		}
	}
	return seconds, nil
}

func resolveOptionalVideoTaskSeconds(payload map[string]any) int {
	for _, field := range []string{"seconds", "duration", "duration_seconds"} {
		value, exists := payload[field]
		if !exists {
			continue
		}
		seconds, err := parseVideoTaskSeconds(value)
		if err != nil {
			return 0
		}
		return seconds
	}
	return 0
}

func parseVideoTaskSeconds(value any) (int, error) {
	var text string
	switch value := value.(type) {
	case json.Number:
		text = value.String()
	case string:
		text = strings.TrimSpace(value)
	default:
		return 0, fmt.Errorf("not a number or numeric string")
	}
	if text == "" {
		return 0, fmt.Errorf("not an integer")
	}
	if !videoTaskDecimalPattern.MatchString(text) {
		return 0, fmt.Errorf("not a decimal number")
	}
	if !videoTaskDurationTextWithinBounds(text) {
		return 0, fmt.Errorf("numeric representation is too large")
	}
	number, ok := new(big.Rat).SetString(text)
	if !ok || number.Sign() <= 0 || number.Cmp(big.NewRat(videoTaskMaxSeconds, 1)) > 0 || number.Denom().Cmp(big.NewInt(1)) != 0 {
		return 0, fmt.Errorf("outside supported range")
	}
	return int(number.Num().Int64()), nil
}

func videoTaskDurationTextWithinBounds(text string) bool {
	if len(text) > videoTaskMaxNumericTextLength {
		return false
	}
	mantissa := text
	exponent := ""
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		mantissa = text[:index]
		exponent = text[index+1:]
	}
	mantissaDigits := 0
	for _, char := range mantissa {
		if char >= '0' && char <= '9' {
			mantissaDigits++
		}
	}
	if mantissaDigits > videoTaskMaxMantissaDigits {
		return false
	}
	if exponent == "" {
		return true
	}
	exponent = strings.TrimPrefix(strings.TrimPrefix(exponent, "+"), "-")
	if len(exponent) > videoTaskMaxExponentDigits {
		return false
	}
	magnitude, err := strconv.Atoi(exponent)
	return err == nil && magnitude <= videoTaskMaxExponentMagnitude
}

func resolveVideoTaskResolution(payload map[string]any, model string) string {
	if value, ok := payload["resolution"].(string); ok {
		if normalized := NormalizeVideoResolutionTier(value); normalized != "" {
			return normalized
		}
	}
	if value, ok := payload["size"].(string); ok {
		if normalized := NormalizeVideoResolutionTier(value); normalized != "" {
			return normalized
		}
	}
	if match := videoResolutionSuffixPattern.FindStringSubmatch(strings.TrimSpace(model)); len(match) == 2 {
		return strings.ToLower(match[1])
	}
	return "720p"
}

func resolveOptionalVideoTaskResolution(payload map[string]any) string {
	for _, field := range []string{"resolution", "size"} {
		value, ok := payload[field].(string)
		if !ok {
			continue
		}
		if normalized := NormalizeVideoResolutionTier(value); normalized != "" {
			return normalized
		}
	}
	return ""
}

// NormalizeVideoResolutionTier returns the canonical label used for video tier matching.
func NormalizeVideoResolutionTier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "854x480", "480p":
		return "480p"
	case "1280x720", "720p":
		return "720p"
	case "1920x1080", "1080p":
		return "1080p"
	case "3840x2160", "2160p", "4k":
		return "4k"
	default:
		return value
	}
}

func resolveVideoTaskUnitPrice(pricing *ChannelModelPricing, resolution string) (float64, error) {
	for i := range pricing.Intervals {
		interval := &pricing.Intervals[i]
		if NormalizeVideoResolutionTier(interval.TierLabel) != resolution || interval.VideoPricePerSecond == nil {
			continue
		}
		if !validVideoPriceMultiplier(*interval.VideoPricePerSecond) {
			return 0, invalidVideoPricingConfig("video tier price must be finite and non-negative")
		}
		return *interval.VideoPricePerSecond, nil
	}
	if pricing.VideoPricePerSecond == nil || !validVideoPriceMultiplier(*pricing.VideoPricePerSecond) {
		return 0, invalidVideoPricingConfig("video price is missing or invalid for resolution %s", resolution)
	}
	return *pricing.VideoPricePerSecond, nil
}

func validVideoPriceMultiplier(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func applyEffectiveVideoDuration(body []byte, seconds int) ([]byte, error) {
	if seconds <= 0 || seconds > videoTaskMaxSeconds {
		return nil, invalidVideoTaskRequest("video seconds must be between 1 and 3600")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, invalidVideoTaskRequest("invalid video create JSON: %v", err)
	}
	if payload == nil {
		return nil, invalidVideoTaskRequest("video create JSON body must be an object")
	}
	delete(payload, "duration")
	delete(payload, "duration_seconds")
	encoded, err := json.Marshal(strconv.Itoa(seconds))
	if err != nil {
		return nil, err
	}
	payload["seconds"] = encoded
	return json.Marshal(payload)
}

func invalidVideoTaskRequest(format string, args ...any) error {
	return infraerrors.Newf(http.StatusBadRequest, "VIDEO_TASK_INVALID_REQUEST", format, args...)
}

func invalidVideoPricingConfig(format string, args ...any) error {
	return infraerrors.Newf(http.StatusBadRequest, "VIDEO_TASK_PRICING_INVALID_CONFIG", format, args...)
}

func videoTaskQuoteFromMetadata(metadata map[string]any) (VideoTaskQuote, bool) {
	if metadata == nil {
		return VideoTaskQuote{}, false
	}
	requestMetadata, ok := videoTaskMetadataMap(metadata["request_metadata"])
	if !ok {
		return VideoTaskQuote{}, false
	}
	value, ok := requestMetadata["video_pricing_snapshot"]
	if !ok || value == nil {
		return VideoTaskQuote{}, false
	}
	if quote, ok := value.(VideoTaskQuote); ok {
		return quote, validVideoTaskQuote(&quote)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return VideoTaskQuote{}, false
	}
	var quote VideoTaskQuote
	if err := json.Unmarshal(raw, &quote); err != nil || !validVideoTaskQuote(&quote) {
		return VideoTaskQuote{}, false
	}
	return quote, true
}

func videoTaskMetadataMap(value any) (map[string]any, bool) {
	if metadata, ok := value.(map[string]any); ok {
		return metadata, true
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return nil, false
	}
	return metadata, true
}

func validVideoTaskQuote(quote *VideoTaskQuote) bool {
	if quote == nil || quote.Effective.VideoCount <= 0 || quote.Effective.VideoCount > VideoTaskMaxOutputs ||
		!validVideoPriceMultiplier(quote.UnitPriceUSD) || !validVideoPriceMultiplier(quote.GrossCostUSD) ||
		!validVideoPriceMultiplier(quote.ActualCostUSD) || !validVideoPriceMultiplier(quote.AccountUnitPriceUSD) ||
		!validVideoPriceMultiplier(quote.AccountBaseCostUSD) || !validVideoPriceMultiplier(quote.AccountCostUSD) ||
		!validVideoPriceMultiplier(quote.RateMultiplier) || !validVideoPriceMultiplier(quote.AccountRateMultiplier) {
		return false
	}
	count := decimal.NewFromInt(int64(quote.Effective.VideoCount))
	var gross, accountBase decimal.Decimal
	switch quote.BillingMode {
	case BillingModeVideo:
		if quote.Effective.Seconds <= 0 || quote.Effective.Seconds > videoTaskMaxSeconds || strings.TrimSpace(quote.Effective.Resolution) == "" {
			return false
		}
		seconds := decimal.NewFromInt(int64(quote.Effective.Seconds))
		gross = decimal.NewFromFloat(quote.UnitPriceUSD).Mul(seconds).Mul(count).Round(10)
		accountBase = decimal.NewFromFloat(quote.AccountUnitPriceUSD).Mul(seconds).Mul(count).Round(10)
	case BillingModePerRequest:
		if quote.Effective.Seconds < 0 || quote.Effective.Seconds > videoTaskMaxSeconds {
			return false
		}
		gross = decimal.NewFromFloat(quote.UnitPriceUSD).Mul(count).Round(10)
		accountBase = decimal.NewFromFloat(quote.AccountUnitPriceUSD).Mul(count).Round(10)
	default:
		return false
	}
	expectedActual := decimal.NewFromFloat(quote.GrossCostUSD).Mul(decimal.NewFromFloat(quote.RateMultiplier)).Round(8)
	expectedAccount := decimal.NewFromFloat(quote.AccountBaseCostUSD).Mul(decimal.NewFromFloat(quote.AccountRateMultiplier)).Round(8)
	return gross.Equal(decimal.NewFromFloat(quote.GrossCostUSD)) && accountBase.Equal(decimal.NewFromFloat(quote.AccountBaseCostUSD)) && expectedActual.Equal(decimal.NewFromFloat(quote.ActualCostUSD)) && expectedAccount.Equal(decimal.NewFromFloat(quote.AccountCostUSD))
}
