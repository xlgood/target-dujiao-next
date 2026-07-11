package service

import (
	"testing"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/upstream"
	"github.com/shopspring/decimal"
)

func TestProviderMappingLocalPricePreservesProviderStrategies(t *testing.T) {
	conn := &models.SiteConnection{
		ExchangeRate:       decimal.NewFromInt(3),
		PriceMarkupPercent: decimal.NewFromInt(100),
	}
	price := decimal.NewFromInt(2)
	if got := providerMappingLocalPrice(upstream.CatalogProviderFansGurus, price, conn); !got.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("fansgurus price=%s, want 10", got)
	}
	if got := providerMappingLocalPrice(upstream.CatalogProviderTGX, price, conn); !got.Equal(decimal.RequireFromString("2.40")) {
		t.Fatalf("tgx price=%s, want 2.40", got)
	}
	if got := providerMappingLocalPrice("", price, conn); !got.Equal(decimal.NewFromInt(12)) {
		t.Fatalf("generic price=%s, want 12", got)
	}
}
