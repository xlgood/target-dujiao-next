package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/upstream"

	"github.com/shopspring/decimal"
)

func TestProviderCatalogAmountsConvertsTGXCNYToUSD(t *testing.T) {
	conn := &models.SiteConnection{
		ExchangeRate:       decimal.RequireFromString("0.14"),
		PriceMarkupPercent: decimal.NewFromInt(20),
	}
	price, cost, err := providerCatalogAmounts(upstream.CatalogProviderTGX, "315.90", "315.90", conn)
	if err != nil {
		t.Fatalf("providerCatalogAmounts: %v", err)
	}
	if !cost.Equal(decimal.RequireFromString("44.23")) {
		t.Fatalf("cost=%s, want 44.23", cost)
	}
	if !price.Equal(decimal.RequireFromString("53.07")) {
		t.Fatalf("price=%s, want 53.07", price)
	}
}

func TestRefreshTGXInventoryStoresActualStock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/commodity/inventory":
			if got := r.FormValue("shared_code"); got != "TGX-001" {
				t.Fatalf("shared_code=%q, want TGX-001", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "data": map[string]interface{}{"stock": 27}})
		case "/commodity/inventoryState":
			if got := r.FormValue("quantity"); got != "1" {
				t.Fatalf("quantity=%q, want 1", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "data": map[string]interface{}{"available": true}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := setupProviderCatalogImportDB(t)
	if err := db.AutoMigrate(&models.SiteConnection{}); err != nil {
		t.Fatalf("migrate site connections: %v", err)
	}
	connService := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-encryption-key", t.TempDir())
	conn, err := connService.Create(CreateConnectionInput{
		Name: "TGX", BaseURL: server.URL, ApiKey: "app-id", ApiSecret: "app-key", Protocol: constants.ConnectionProtocolTGXAccount,
	})
	if err != nil {
		t.Fatalf("create TGX connection: %v", err)
	}
	product := &models.Product{CategoryID: 1, Slug: "tgx-test", TitleJSON: models.JSON{"zh-CN": "TGX test"}, IsMapped: true}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}
	sku := &models.ProductSKU{ProductID: product.ID, SKUCode: "TGX-001", IsActive: true}
	if err := db.Create(sku).Error; err != nil {
		t.Fatalf("create SKU: %v", err)
	}
	mapping := &models.ProductMapping{ConnectionID: conn.ID, LocalProductID: product.ID, Provider: upstream.CatalogProviderTGX, IsActive: true, UpstreamStatus: models.UpstreamStatusActive}
	if err := db.Create(mapping).Error; err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	skuMapping := &models.SKUMapping{ProductMappingID: mapping.ID, LocalSKUID: sku.ID, UpstreamSKUCode: "TGX-001", UpstreamStock: -1, UpstreamIsActive: true}
	if err := db.Create(skuMapping).Error; err != nil {
		t.Fatalf("create SKU mapping: %v", err)
	}

	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db), repository.NewSKUMappingRepository(db), repository.NewProductRepository(db), repository.NewProductSKURepository(db), repository.NewCategoryRepository(db), connService,
	)
	if err := svc.RefreshTGXInventory(mapping.ID); err != nil {
		t.Fatalf("RefreshTGXInventory: %v", err)
	}
	var got models.SKUMapping
	if err := db.First(&got, skuMapping.ID).Error; err != nil {
		t.Fatalf("reload SKU mapping: %v", err)
	}
	if got.UpstreamStock != 27 || !got.UpstreamIsActive || got.StockSyncedAt == nil {
		t.Fatalf("unexpected refreshed SKU mapping: %+v", got)
	}
}
