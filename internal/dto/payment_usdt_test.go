package dto

import (
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
)

func TestExtractUSDTWalletInfo(t *testing.T) {
	tests := []struct {
		name            string
		providerType    string
		interactionMode string
		payload         models.JSON
		wantAddress     string
		wantChainAmount string
	}{
		{
			name:            "bepusdt qr with token in data",
			providerType:    constants.PaymentProviderBepusdt,
			interactionMode: constants.PaymentInteractionQR,
			payload: models.JSON{
				"data": map[string]any{
					"token":         "TRX_ADDR_XYZ",
					"actual_amount": "13.45",
				},
			},
			wantAddress:     "TRX_ADDR_XYZ",
			wantChainAmount: "13.45",
		},
		{
			name:            "bepusdt redirect mode returns empty",
			providerType:    constants.PaymentProviderBepusdt,
			interactionMode: constants.PaymentInteractionRedirect,
			payload: models.JSON{
				"data": map[string]any{"token": "TRX_ADDR_XYZ", "actual_amount": "13.45"},
			},
			wantAddress:     "",
			wantChainAmount: "",
		},
		{
			name:            "epusdt qr without receive_address yields empty",
			providerType:    constants.PaymentProviderEpusdt,
			interactionMode: constants.PaymentInteractionQR,
			payload: models.JSON{
				"data": map[string]any{"trade_id": "T1", "actual_amount": "5.00"},
			},
			wantAddress:     "",
			wantChainAmount: "5.00",
		},
		{
			name:            "epusdt qr with receive_address",
			providerType:    constants.PaymentProviderEpusdt,
			interactionMode: constants.PaymentInteractionQR,
			payload: models.JSON{
				"data": map[string]any{"receive_address": "TGRC_ADDR", "actual_amount": "9.99"},
			},
			wantAddress:     "TGRC_ADDR",
			wantChainAmount: "9.99",
		},
		{
			name:            "non-usdt provider returns empty",
			providerType:    constants.PaymentProviderOfficial,
			interactionMode: constants.PaymentInteractionQR,
			payload:         models.JSON{"data": map[string]any{"token": "X"}},
			wantAddress:     "",
			wantChainAmount: "",
		},
		{
			name:            "nil payload safe",
			providerType:    constants.PaymentProviderBepusdt,
			interactionMode: constants.PaymentInteractionQR,
			payload:         nil,
			wantAddress:     "",
			wantChainAmount: "",
		},
		{
			name:            "bepusdt qr with data scalar instead of map",
			providerType:    constants.PaymentProviderBepusdt,
			interactionMode: constants.PaymentInteractionQR,
			payload:         models.JSON{"data": "oops"},
			wantAddress:     "",
			wantChainAmount: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotAddr, gotAmount := ExtractUSDTWalletInfo(tc.providerType, tc.interactionMode, tc.payload)
			if gotAddr != tc.wantAddress {
				t.Errorf("address: got %q want %q", gotAddr, tc.wantAddress)
			}
			if gotAmount != tc.wantChainAmount {
				t.Errorf("chain amount: got %q want %q", gotAmount, tc.wantChainAmount)
			}
		})
	}
}
