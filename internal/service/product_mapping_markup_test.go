package service

import (
	"testing"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/upstream"
	"github.com/shopspring/decimal"
)

func TestProviderMappingLocalPriceUsesConnectionSettings(t *testing.T) {
	conn := &models.SiteConnection{
		ExchangeRate:       decimal.RequireFromString("0.14"),
		PriceMarkupPercent: decimal.NewFromInt(20),
	}
	price := decimal.NewFromInt(2)
	if got := providerMappingLocalPrice(upstream.CatalogProviderFansGurus, price, conn); !got.Equal(decimal.RequireFromString("0.34")) {
		t.Fatalf("fansgurus price=%s, want 0.34", got)
	}
	if got := providerMappingLocalPrice(upstream.CatalogProviderTGX, price, conn); !got.Equal(decimal.RequireFromString("0.34")) {
		t.Fatalf("tgx price=%s, want 0.34", got)
	}
	if got := providerMappingLocalPrice("", price, conn); !got.Equal(decimal.RequireFromString("0.34")) {
		t.Fatalf("generic price=%s, want 0.34", got)
	}
}
