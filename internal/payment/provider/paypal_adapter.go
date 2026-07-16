package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/payment/paypal"

	"github.com/shopspring/decimal"
)

// paypalAdapter 是 paypal 网关的 Provider/Capturer/Webhooker 实现。
// 模式与 stripeAdapter 一致：仅做参数适配 + 错误映射，业务逻辑委托给 internal/payment/paypal/ 包级函数。
type paypalAdapter struct{}

// NewPaypalAdapter 实例化 paypal adapter。
func NewPaypalAdapter() Provider { return &paypalAdapter{} }

// 编译期断言 paypalAdapter 实现了三个 capability interface。
var (
	_ Provider  = (*paypalAdapter)(nil)
	_ Capturer  = (*paypalAdapter)(nil)
	_ Webhooker = (*paypalAdapter)(nil)
)

// Type 返回 provider 标识。
func (a *paypalAdapter) Type() string {
	return constants.PaymentProviderOfficial + ":" + constants.PaymentChannelTypePaypal
}

// parseConfig 解析并验证 paypal Config，把 paypal.ErrConfigInvalid 等映射为 provider.ErrXxx。
// 4 个公开方法共用，避免每个都重复 6 行样板。
func (a *paypalAdapter) parseConfig(raw models.JSON) (*paypal.Config, error) {
	cfg, err := paypal.ParseConfig(raw)
	if err != nil {
		return nil, mapPaypalError(err)
	}
	if err := paypal.ValidateConfig(cfg); err != nil {
		return nil, mapPaypalError(err)
	}
	return cfg, nil
}

// ValidateConfig 验证 channel.ConfigJSON。
func (a *paypalAdapter) ValidateConfig(raw models.JSON, _ string) error {
	_, err := a.parseConfig(raw)
	return err
}

// CreatePayment 创建支付。
func (a *paypalAdapter) CreatePayment(ctx context.Context, raw models.JSON, input CreateInput) (*CreateResult, error) {
	cfg, err := a.parseConfig(raw)
	if err != nil {
		return nil, err
	}

	// P1.2c: wrapper 内做 currency conversion + audit 字段写入。
	// exchange_rate / original_amount / original_currency 保留到 result.Payload，
	// 供运营/财务跨币种对账追溯实际收费 vs 原始金额。
	// result.AmountSent/CurrencySent 反映实际发给网关的金额/币种，
	// 让 service 层据此更新 payment.Amount/Currency，保持记录与实际收费一致。
	originalAmount := input.Amount.Decimal.String()
	originalCurrency := input.Currency
	payAmount := originalAmount
	payCurrency := originalCurrency
	converted := false
	if cfg.NeedsCurrencyConversion() {
		convAmount, convCurrency, convErr := cfg.ConvertAmount(payAmount, payCurrency)
		if convErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrConfigInvalid, convErr)
		}
		payAmount = convAmount
		payCurrency = convCurrency
		converted = true
	}

	// P1.2c Task 3: 先 fallback 到 cfg.ReturnURL，再 append tracking marker。
	returnURL := strings.TrimSpace(input.ReturnURL)
	if returnURL == "" {
		returnURL = strings.TrimSpace(cfg.ReturnURL)
	}
	returnURL = appendQueryParams(returnURL, input.ReturnURLQuery)

	cancelURL, _ := input.Extra["cancel_url"].(string)
	native := paypal.CreateInput{
		OrderNo:     input.OrderNo,
		Amount:      payAmount,
		Currency:    payCurrency,
		Description: input.Subject,
		ReturnURL:   returnURL,
		CancelURL:   cancelURL,
	}
	result, err := paypal.CreateOrder(ctx, cfg, native)
	if err != nil {
		return nil, mapPaypalError(err)
	}

	payload := models.JSON{}
	if result.Raw != nil {
		payload = models.JSON(result.Raw)
	}
	if converted {
		payload["exchange_rate"] = strings.TrimSpace(cfg.ExchangeRate)
		payload["original_amount"] = originalAmount
		payload["original_currency"] = originalCurrency
	}

	return &CreateResult{
		ProviderRef:  result.OrderID,
		RedirectURL:  result.ApprovalURL,
		Payload:      payload,
		AmountSent:   payAmount,
		CurrencySent: payCurrency,
	}, nil
}

// QueryPayment 调用 paypal.CaptureOrder 完成捕获并返回状态（实现 Capturer）。
func (a *paypalAdapter) QueryPayment(ctx context.Context, raw models.JSON, providerRef string) (*QueryResult, error) {
	cfg, err := a.parseConfig(raw)
	if err != nil {
		return nil, err
	}

	result, err := paypal.CaptureOrder(ctx, cfg, providerRef)
	if err != nil {
		return nil, mapPaypalError(err)
	}

	// amount 解析失败时返回零值：wrapper 仅做适配，金额异常的语义边界（对账失败 / 网关返回脏数据）
	// 留给上游业务层判定，wrapper 不擅自报错。
	amount := models.Money{}
	if s := strings.TrimSpace(result.Amount); s != "" {
		if parsed, parseErr := decimal.NewFromString(s); parseErr == nil {
			amount = models.NewMoneyFromDecimal(parsed)
		}
	}

	status, ok := paypal.ToPaymentStatus("", result.Status)
	if !ok {
		return nil, fmt.Errorf("%w: unsupported PayPal capture status %q", ErrResponseInvalid, result.Status)
	}

	return &QueryResult{
		ProviderRef: pickFirstNonEmpty(result.OrderID, providerRef),
		Status:      status,
		Amount:      amount,
		Currency:    strings.ToUpper(strings.TrimSpace(result.Currency)),
		PaidAt:      result.PaidAt,
		Payload:     models.JSON(result.Raw),
	}, nil
}

// ParseWebhook 合并 paypal 的 VerifyWebhookSignature + ParseWebhookEvent 两步（stripe 是一步）。
func (a *paypalAdapter) ParseWebhook(ctx context.Context, raw models.JSON, headers map[string]string, body []byte, _ time.Time) (*WebhookResult, error) {
	cfg, err := a.parseConfig(raw)
	if err != nil {
		return nil, err
	}

	var event map[string]interface{}
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("%w: webhook body not valid JSON: %v", ErrResponseInvalid, err)
	}

	httpHeaders := http.Header{}
	for k, v := range headers {
		httpHeaders.Set(k, v)
	}

	if err := paypal.VerifyWebhookSignature(ctx, cfg, httpHeaders, event); err != nil {
		return nil, mapPaypalError(err)
	}

	parsed, err := paypal.ParseWebhookEvent(body)
	if err != nil {
		return nil, mapPaypalError(err)
	}

	status, _ := paypal.ToPaymentStatus(parsed.EventType, parsed.ResourceStatus())

	// amount 解析失败时返回零值：wrapper 仅做适配，金额异常的语义边界（对账失败 / 网关返回脏数据）
	// 留给上游业务层判定，wrapper 不擅自报错。
	amount := models.Money{}
	rawAmount, currency := parsed.CaptureAmount()
	if s := strings.TrimSpace(rawAmount); s != "" {
		if d, parseErr := decimal.NewFromString(s); parseErr == nil {
			amount = models.NewMoneyFromDecimal(d)
		}
	}

	return &WebhookResult{
		OrderNo:     parsed.RelatedInvoiceID(),
		ProviderRef: parsed.RelatedOrderID(),
		Status:      status,
		Amount:      amount,
		Currency:    strings.ToUpper(strings.TrimSpace(currency)),
		PaidAt:      parsed.PaidAt(),
		Payload:     models.JSON(map[string]interface{}{"event": event}),
	}, nil
}

func mapPaypalError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, paypal.ErrConfigInvalid):
		return fmt.Errorf("%w: %v", ErrConfigInvalid, err)
	case errors.Is(err, paypal.ErrAuthFailed):
		return fmt.Errorf("%w: %v", ErrAuthFailed, err)
	case errors.Is(err, paypal.ErrRequestFailed):
		return fmt.Errorf("%w: %v", ErrRequestFailed, err)
	case errors.Is(err, paypal.ErrResponseInvalid):
		return fmt.Errorf("%w: %v", ErrResponseInvalid, err)
	case errors.Is(err, paypal.ErrWebhookVerifyFailed):
		return fmt.Errorf("%w: %v", ErrSignatureInvalid, err)
	default:
		return err
	}
}
