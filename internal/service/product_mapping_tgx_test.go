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
			if got := r.FormValue("sharedCode"); got != "TGX-001" {
				t.Fatalf("sharedCode=%q, want TGX-001", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "data": map[string]interface{}{"count": 27}})
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

func TestRefreshTGXInventoryStoresZeroStockAsSoldOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "data": map[string]interface{}{"count": 0}})
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
	product := &models.Product{CategoryID: 1, Slug: "tgx-zero-stock", TitleJSON: models.JSON{"zh-CN": "TGX zero stock"}, IsMapped: true}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}
	sku := &models.ProductSKU{ProductID: product.ID, SKUCode: "TGX-002", IsActive: true}
	if err := db.Create(sku).Error; err != nil {
		t.Fatalf("create SKU: %v", err)
	}
	mapping := &models.ProductMapping{ConnectionID: conn.ID, LocalProductID: product.ID, Provider: upstream.CatalogProviderTGX, IsActive: true, UpstreamStatus: models.UpstreamStatusActive}
	if err := db.Create(mapping).Error; err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	skuMapping := &models.SKUMapping{ProductMappingID: mapping.ID, LocalSKUID: sku.ID, UpstreamSKUCode: "TGX-002", UpstreamStock: -1, UpstreamIsActive: true}
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
	if got.UpstreamStock != 0 || got.UpstreamIsActive || got.StockSyncedAt == nil {
		t.Fatalf("unexpected refreshed SKU mapping: %+v", got)
	}
}

func TestSyncTGXConnectionStockRefreshesAllMappedSKUs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		code := r.FormValue("sharedCode")
		count := 0
		switch code {
		case "TGX-ONE":
			count = 12
		case "TGX-TWO":
			count = 0
		default:
			t.Fatalf("unexpected sharedCode %q", code)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "data": map[string]interface{}{"count": count}})
	}))
	defer server.Close()

	db := setupProviderCatalogImportDB(t)
	if err := db.AutoMigrate(&models.SiteConnection{}); err != nil {
		t.Fatalf("migrate site connections: %v", err)
	}
	connService := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-encryption-key", t.TempDir())
	conn, err := connService.Create(CreateConnectionInput{Name: "TGX", BaseURL: server.URL, ApiKey: "app-id", ApiSecret: "app-key", Protocol: constants.ConnectionProtocolTGXAccount})
	if err != nil {
		t.Fatalf("create TGX connection: %v", err)
	}
	product := models.Product{CategoryID: 1, Slug: "tgx-bulk-stock", TitleJSON: models.JSON{"zh-CN": "TGX bulk"}, IsMapped: true}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}
	mapping := models.ProductMapping{ConnectionID: conn.ID, LocalProductID: product.ID, Provider: upstream.CatalogProviderTGX, IsActive: true, UpstreamStatus: models.UpstreamStatusActive}
	if err := db.Create(&mapping).Error; err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	for _, code := range []string{"TGX-ONE", "TGX-TWO"} {
		sku := models.ProductSKU{ProductID: product.ID, SKUCode: code, IsActive: true}
		if err := db.Create(&sku).Error; err != nil {
			t.Fatalf("create SKU: %v", err)
		}
		if err := db.Create(&models.SKUMapping{ProductMappingID: mapping.ID, LocalSKUID: sku.ID, UpstreamSKUCode: code, UpstreamStock: -1, UpstreamIsActive: true}).Error; err != nil {
			t.Fatalf("create mapping: %v", err)
		}
	}
	svc := NewProductMappingService(repository.NewProductMappingRepository(db), repository.NewSKUMappingRepository(db), repository.NewProductRepository(db), repository.NewProductSKURepository(db), repository.NewCategoryRepository(db), connService)
	if err := svc.syncTGXConnectionStock(conn, []models.ProductMapping{mapping}); err != nil {
		t.Fatalf("syncTGXConnectionStock: %v", err)
	}
	var got []models.SKUMapping
	if err := db.Order("upstream_sku_code ASC").Find(&got).Error; err != nil {
		t.Fatalf("load SKU mappings: %v", err)
	}
	if len(got) != 2 || got[0].UpstreamStock != 12 || !got[0].UpstreamIsActive || got[1].UpstreamStock != 0 || got[1].UpstreamIsActive || got[0].StockSyncedAt == nil || got[1].StockSyncedAt == nil {
		t.Fatalf("unexpected bulk stock state: %+v", got)
	}
}

func TestSyncTGXConnectionStockRetriesAndRecordsFailures(t *testing.T) {
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := r.FormValue("sharedCode")
		requests[code]++
		w.Header().Set("Content-Type", "application/json")
		if code == "TGX-RETRY" && requests[code] == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if code == "TGX-FAIL" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "data": map[string]interface{}{"count": 9}})
	}))
	defer server.Close()

	db := setupProviderCatalogImportDB(t)
	if err := db.AutoMigrate(&models.SiteConnection{}, &models.TGXInventorySyncRun{}); err != nil {
		t.Fatal(err)
	}
	connService := NewSiteConnectionService(repository.NewSiteConnectionRepository(db), "test-encryption-key", t.TempDir())
	conn, err := connService.Create(CreateConnectionInput{Name: "TGX", BaseURL: server.URL, ApiKey: "app-id", ApiSecret: "app-key", Protocol: constants.ConnectionProtocolTGXAccount})
	if err != nil {
		t.Fatal(err)
	}
	product := models.Product{CategoryID: 1, Slug: "tgx-retry", TitleJSON: models.JSON{"zh-CN": "TGX retry"}, IsMapped: true}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	mapping := models.ProductMapping{ConnectionID: conn.ID, LocalProductID: product.ID, Provider: upstream.CatalogProviderTGX, IsActive: true}
	if err := db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"TGX-RETRY", "TGX-FAIL"} {
		sku := models.ProductSKU{ProductID: product.ID, SKUCode: code, IsActive: true}
		if err := db.Create(&sku).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.SKUMapping{ProductMappingID: mapping.ID, LocalSKUID: sku.ID, UpstreamSKUCode: code, UpstreamStock: -1, UpstreamIsActive: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := NewProductMappingService(repository.NewProductMappingRepository(db), repository.NewSKUMappingRepository(db), repository.NewProductRepository(db), repository.NewProductSKURepository(db), repository.NewCategoryRepository(db), connService)
	svc.SetTGXInventorySyncRunRepository(repository.NewTGXInventorySyncRunRepository(db))
	cfg := DefaultUpstreamSyncConfig()
	cfg.TGXInventoryConcurrency = 1
	cfg.TGXInventoryRateLimit = 20
	cfg.TGXInventoryRetries = 1
	if err := svc.syncTGXConnectionStockWithConfig(conn, []models.ProductMapping{mapping}, cfg); err == nil {
		t.Fatal("expected partial sync error")
	}
	if requests["TGX-RETRY"] != 2 || requests["TGX-FAIL"] != 2 {
		t.Fatalf("request counts=%v", requests)
	}
	var success models.SKUMapping
	if err := db.Where("upstream_sku_code = ?", "TGX-RETRY").First(&success).Error; err != nil || success.UpstreamStock != 9 {
		t.Fatalf("retry SKU=%+v err=%v", success, err)
	}
	var run models.TGXInventorySyncRun
	if err := db.Order("id DESC").First(&run).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "partial" || run.Total != 2 || run.Succeeded != 1 || run.Failed != 1 {
		t.Fatalf("run=%+v", run)
	}
}
