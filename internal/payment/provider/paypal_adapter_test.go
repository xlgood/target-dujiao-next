package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/payment/paypal"

	"github.com/shopspring/decimal"
)

func TestPaypalAdapter_Type(t *testing.T) {
	a := NewPaypalAdapter()
	want := constants.PaymentProviderOfficial + ":" + constants.PaymentChannelTypePaypal
	if got := a.Type(); got != want {
		t.Fatalf("Type() = %q, want %q", got, want)
	}
}

func TestPaypalAdapter_ValidateConfig_EmptyRejected(t *testing.T) {
	a := NewPaypalAdapter()
	err := a.ValidateConfig(models.JSON{}, "")
	if err == nil {
		t.Fatalf("expected error from empty config")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("expected wrapped ErrConfigInvalid, got %v", err)
	}
}

func TestPaypalAdapter_CreatePayment_ConfigInvalidMapped(t *testing.T) {
	a := NewPaypalAdapter()
	_, err := a.CreatePayment(context.Background(), models.JSON{}, CreateInput{
		OrderNo:  "ORDER_1",
		Currency: "USD",
	})
	if err == nil {
		t.Fatalf("expected error from empty config")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("expected wrapped ErrConfigInvalid, got %v", err)
	}
}

// TestPaypalAdapter_CreatePayment_ExchangeRate_AuditFields 守护 P1.2c audit
// 字段写入回归。模式见 stripe_adapter_test.go 同名测试。
//
// 注意:paypal 用自己定义的 Config.ConvertAmount(amount, currency)(2 参数),
// 不走 common.ExchangeRateConfig 的 3 参数版本,本测试单独守护该路径。
func TestPaypalAdapter_CreatePayment_ExchangeRate_AuditFields(t *testing.T) {
	// paypal 流程:OAuth /v1/oauth2/token → POST /v2/checkout/orders
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/oauth2/token":
			_, _ = w.Write([]byte(`{"access_token":"paypal-test-token","token_type":"Bearer","expires_in":3600}`))
		case "/v2/checkout/orders":
			_, _ = w.Write([]byte(`{
				"id":"PP-ORDER-AUDIT-001",
				"status":"CREATED",
				"links":[
					{"rel":"approve","href":"https://www.paypal.com/checkoutnow?token=PP-ORDER-AUDIT-001","method":"GET"}
				]
			}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	a := NewPaypalAdapter()
	raw := models.JSON{
		"client_id":     "client-audit",
		"client_secret": "secret-audit",
		"base_url":      server.URL,
		"return_url":    "https://shop.example.com/paypal/return",
		"cancel_url":    "https://shop.example.com/paypal/cancel",
		"webhook_id":    "WH-AUDIT-001",
		// 跨币种:1 CNY → 7.2 USD (paypal Config 用自有 target_currency / exchange_rate)
		"target_currency": "USD",
		"exchange_rate":   "7.2",
	}

	input := CreateInput{
		OrderNo:  "ORDER-PP-AUDIT",
		Subject:  "audit field test",
		Currency: "CNY",
		Amount:   models.NewMoneyFromDecimal(decimal.NewFromInt(1)),
	}

	result, err := a.CreatePayment(context.Background(), raw, input)
	if err != nil {
		t.Fatalf("CreatePayment() failed: %v", err)
	}

	if result.CurrencySent != "USD" {
		t.Fatalf("CurrencySent = %q, want USD (converted target)", result.CurrencySent)
	}
	if result.AmountSent != "7.2" {
		t.Fatalf("AmountSent = %q, want 7.2 (1 CNY * 7.2)", result.AmountSent)
	}

	if got := result.Payload["exchange_rate"]; got != "7.2" {
		t.Fatalf("Payload[exchange_rate] = %v, want 7.2", got)
	}
	if got := result.Payload["original_amount"]; got != "1" {
		t.Fatalf("Payload[original_amount] = %v, want 1", got)
	}
	if got := result.Payload["original_currency"]; got != "CNY" {
		t.Fatalf("Payload[original_currency] = %v, want CNY", got)
	}
}

func TestPaypalAdapter_MapPaypalError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"config", paypal.ErrConfigInvalid, ErrConfigInvalid},
		{"auth", paypal.ErrAuthFailed, ErrAuthFailed},
		{"request", paypal.ErrRequestFailed, ErrRequestFailed},
		{"response", paypal.ErrResponseInvalid, ErrResponseInvalid},
		{"webhook→signature", paypal.ErrWebhookVerifyFailed, ErrSignatureInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapPaypalError(tc.in)
			if !errors.Is(got, tc.want) {
				t.Fatalf("mapPaypalError(%v) errors.Is %v = false, want true", tc.in, tc.want)
			}
		})
	}
}

func TestPaypalAdapter_QueryPayment_MapsCompletedCaptureToSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/oauth2/token":
			_, _ = w.Write([]byte(`{"access_token":"paypal-test-token","token_type":"Bearer","expires_in":3600}`))
		case "/v2/checkout/orders/PP-ORDER-1/capture":
			_, _ = w.Write([]byte(`{
				"id":"PP-ORDER-1",
				"status":"COMPLETED",
				"purchase_units":[{"payments":{"captures":[{
					"id":"PP-CAPTURE-1","status":"COMPLETED",
					"amount":{"currency_code":"USD","value":"1.00"}
				}]}}]
			}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	capturer, ok := NewPaypalAdapter().(Capturer)
	if !ok {
		t.Fatal("PayPal adapter must implement Capturer")
	}
	result, err := capturer.QueryPayment(context.Background(), models.JSON{
		"client_id": "client-test", "client_secret": "secret-test", "base_url": server.URL,
		"return_url": "https://shop.example.com/pay", "cancel_url": "https://shop.example.com/pay",
	}, "PP-ORDER-1")
	if err != nil {
		t.Fatalf("QueryPayment() failed: %v", err)
	}
	if result.Status != constants.PaymentStatusSuccess {
		t.Fatalf("Status = %q, want %q", result.Status, constants.PaymentStatusSuccess)
	}
}

func TestPaypalAdapter_CreatePayment_AppendsOrderQueryToCancelURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"paypal-test-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path != "/v2/checkout/orders" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		context, _ := payload["application_context"].(map[string]any)
		cancelURL, _ := context["cancel_url"].(string)
		if got := cancelURL; got != "https://shop.example.com/pay?biz_type=order&order_no=ORDER-1&paypal_cancel=1" {
			t.Fatalf("cancel_url = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"PP-ORDER-1","status":"CREATED","links":[{"rel":"approve","href":"https://paypal.example/approve"}]}`))
	}))
	defer server.Close()

	_, err := NewPaypalAdapter().CreatePayment(context.Background(), models.JSON{
		"client_id": "client-test", "client_secret": "secret-test", "base_url": server.URL,
		"return_url": "https://shop.example.com/pay", "cancel_url": "https://shop.example.com/pay",
	}, CreateInput{
		OrderNo: "ORDER-1", Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(1)), Currency: "USD",
		ReturnURLQuery: map[string]string{"biz_type": "order", "order_no": "ORDER-1", "paypal_return": "1"},
	})
	if err != nil {
		t.Fatalf("CreatePayment() failed: %v", err)
	}
}
