package public

import (
	"testing"

	"github.com/dujiao-next/internal/models"
	"github.com/shopspring/decimal"
)

func TestPublicProductResponseRedactsLegacyProviderMetadata(t *testing.T) {
	product := &models.Product{
		Tags: models.StringArray{"facebook", "tgx", "Fans Gurus"},
		SKUs: []models.ProductSKU{{
			ID:             1,
			SKUCode:        "tgx-facebook-043f2a392fce5ef8",
			IsActive:       true,
			SpecValuesJSON: models.JSON{"provider": "tgx", "race": "standard"},
		}},
		WholesalePrices: models.WholesalePriceTiers{{
			SKUID:       1,
			MinQuantity: 2,
			UnitPrice:   models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
		}},
	}

	resp, err := (&Handler{}).decoratePublicProduct(product, nil)
	if err != nil {
		t.Fatalf("decoratePublicProduct failed: %v", err)
	}
	if len(resp.Tags) != 1 || resp.Tags[0] != "facebook" {
		t.Fatalf("legacy provider tags leaked: %v", resp.Tags)
	}
	if _, exists := resp.SKUs[0].SpecValues["provider"]; exists {
		t.Fatalf("legacy provider SKU metadata leaked: %v", resp.SKUs[0].SpecValues)
	}
	if resp.SKUs[0].SpecValues["race"] != "standard" {
		t.Fatalf("public SKU option was removed: %v", resp.SKUs[0].SpecValues)
	}
	if resp.SKUs[0].SKUCode != "option-1" {
		t.Fatalf("legacy upstream SKU code leaked: %q", resp.SKUs[0].SKUCode)
	}
}
