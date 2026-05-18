package dto

import (
	"fmt"
	"strings"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
)

// ExtractUSDTWalletInfo 从 Payment.ProviderPayload 中提取 USDT 收款钱包地址和链上实付金额。
// 只在 interactionMode == "qr" 且 providerType 为 USDT 类网关时返回非空值；
// 其他情况返回 ("", "")，由调用方决定是否输出 omitempty 字段。
//
// 字段名按各 provider 原生响应：
//   - bepusdt:   data.token            (地址)  / data.actual_amount
//   - epusdt:    data.receive_address  (地址)  / data.actual_amount
//   - tokenpay:  暂不支持（包未解析地址），保留扩展位
func ExtractUSDTWalletInfo(providerType, interactionMode string, payload models.JSON) (address, chainAmount string) {
	if strings.ToLower(strings.TrimSpace(interactionMode)) != constants.PaymentInteractionQR {
		return "", ""
	}
	pt := strings.ToLower(strings.TrimSpace(providerType))
	switch pt {
	case constants.PaymentProviderBepusdt:
		return readPayloadString(payload, "data", "token"), readPayloadString(payload, "data", "actual_amount")
	case constants.PaymentProviderEpusdt:
		return readPayloadString(payload, "data", "receive_address"), readPayloadString(payload, "data", "actual_amount")
	default:
		return "", ""
	}
}

// readPayloadString 沿 keys 路径在 payload 内取字符串值，支持 string / json.Number / 数值（fmt 转换）。
func readPayloadString(payload models.JSON, keys ...string) string {
	if payload == nil || len(keys) == 0 {
		return ""
	}
	var cur any = map[string]any(payload)
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			if mj, ok2 := cur.(models.JSON); ok2 {
				m = map[string]any(mj)
			} else {
				return ""
			}
		}
		v, ok := m[k]
		if !ok {
			return ""
		}
		cur = v
	}
	switch v := cur.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.8f", v), "0"), ".")
	case int, int32, int64:
		return strings.TrimSpace(fmt.Sprintf("%d", v))
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}
