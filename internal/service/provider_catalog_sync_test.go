package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/upstream"
)

type fakeFansGurusCatalogClient struct {
	services []upstream.FansGurusService
	err      error
}

func (c fakeFansGurusCatalogClient) ListServices(ctx context.Context) ([]upstream.FansGurusService, error) {
	return c.services, c.err
}

type fakeTGXCatalogClient struct {
	items []upstream.TGXCommodity
	err   error
}

func (c fakeTGXCatalogClient) ListItems(ctx context.Context) (*upstream.TGXItemsResponse, error) {
	return &upstream.TGXItemsResponse{Items: c.items}, c.err
}

func TestSyncProviderCatalogWithClientsFiltersAndImports(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db),
		repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db),
		repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db),
		nil,
	)
	svc.SetProviderCatalogSyncRunRepository(repository.NewProviderCatalogSyncRunRepository(db))

	result, err := svc.SyncProviderCatalogWithClients(
		context.Background(),
		ProviderCatalogSyncInput{FansGurusConnectionID: 101, TGXConnectionID: 202},
		fakeFansGurusCatalogClient{services: []upstream.FansGurusService{
			{Service: 1, Name: "Instagram Followers", Category: "Instagram", Rate: "2.00", Min: 500, Max: 10000},
			{Service: 2, Name: "YouTube Views", Category: "YouTube", Rate: "1.00", Min: 100, Max: 10000},
			{Service: 3, Name: "Telegram Members", Category: "Telegram", Rate: "1.00", Min: 100, Max: 10000},
		}},
		fakeTGXCatalogClient{items: []upstream.TGXCommodity{
			{Code: "IG-001", Name: "IG Account", Description: "Instagram aged account", Price: "100.00", Minimum: 1},
			{Code: "FB-001", Name: "Facebook Account", Description: "Facebook account", Price: "80.00", Minimum: 1},
		}},
	)
	if err != nil {
		t.Fatalf("SyncProviderCatalogWithClients: %v", err)
	}
	if result.FansGurusPulled != 3 || result.TGXPulled != 2 {
		t.Fatalf("unexpected pull counts: %+v", result)
	}
	if len(result.SupportedPlatforms) != 3 {
		t.Fatalf("supported platforms=%v, want facebook, instagram, youtube", result.SupportedPlatforms)
	}
	if result.FilteredTelegram != 1 || result.FilteredPlatform != 0 {
		t.Fatalf("unexpected filter counts: %+v", result)
	}
	if result.Imported != 4 || result.Skipped != 0 {
		t.Fatalf("unexpected import counts: %+v", result)
	}
	if result.Updated != 0 {
		t.Fatalf("updated=%d, want 0", result.Updated)
	}
	if result.Deactivated != 0 {
		t.Fatalf("deactivated=%d, want 0", result.Deactivated)
	}

	var mappings []models.ProductMapping
	if err := db.Order("provider ASC").Find(&mappings).Error; err != nil {
		t.Fatalf("load mappings: %v", err)
	}
	if len(mappings) != 4 {
		t.Fatalf("mapping count=%d, want 4", len(mappings))
	}
	for _, mapping := range mappings {
		if mapping.Provider == upstream.CatalogProviderFansGurus && mapping.ConnectionID != 101 {
			t.Fatalf("unexpected fans mapping: %+v", mapping)
		}
		if mapping.Provider == upstream.CatalogProviderTGX && mapping.ConnectionID != 202 {
			t.Fatalf("unexpected tgx mapping: %+v", mapping)
		}
	}

	var run models.ProviderCatalogSyncRun
	if err := db.First(&run).Error; err != nil {
		t.Fatalf("load sync run: %v", err)
	}
	if run.Status != "success" || run.FansGurusPulled != 3 || run.TGXPulled != 2 || run.Imported != 4 {
		t.Fatalf("unexpected sync run: %+v", run)
	}
	if run.RawFansGurusJSON["services"] == nil || run.RawTGXJSON["items"] == nil {
		t.Fatalf("raw payloads should be stored: %+v", run)
	}
}

func TestSyncProviderCatalogWithClientsImportsSupportedFansGurusTypes(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db),
		repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db),
		repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db),
		nil,
	)
	result, err := svc.SyncProviderCatalogWithClients(
		context.Background(),
		ProviderCatalogSyncInput{FansGurusConnectionID: 101, TGXConnectionID: 202},
		fakeFansGurusCatalogClient{services: []upstream.FansGurusService{
			{Service: 1, Name: "Instagram Followers", Category: "Instagram", Type: "Default", Rate: "2.00"},
			{Service: 2, Name: "Instagram Comments", Category: "Instagram", Type: "Custom Comments", Rate: "2.00"},
		}},
		fakeTGXCatalogClient{items: []upstream.TGXCommodity{{Code: "IG-001", Name: "Instagram Account", Price: "100.00"}}},
	)
	if err != nil {
		t.Fatalf("SyncProviderCatalogWithClients: %v", err)
	}
	if result.Imported != 3 {
		t.Fatalf("imported=%d, want 3", result.Imported)
	}
	var count int64
	if err := db.Model(&models.ProductMapping{}).Where("upstream_product_code = ?", "2").Count(&count).Error; err != nil {
		t.Fatalf("count comments mapping: %v", err)
	}
	if count != 1 {
		t.Fatalf("custom comments service was not imported")
	}
}

func TestSyncProviderCatalogWithClientsDeactivatesStaleMappings(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	mappingRepo := repository.NewProductMappingRepository(db)
	svc := NewProductMappingService(
		mappingRepo,
		repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db),
		repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db),
		nil,
	)

	legacyCategory := models.Category{Slug: "platform-x", NameJSON: models.JSON{"zh-CN": "x"}, IsActive: true}
	if err := db.Create(&legacyCategory).Error; err != nil {
		t.Fatalf("seed category: %v", err)
	}
	legacyProduct := models.Product{
		CategoryID:      legacyCategory.ID,
		Slug:            "tgx-x-stale",
		TitleJSON:       models.JSON{"zh-CN": "Gmail account"},
		DescriptionJSON: models.JSON{},
		ContentJSON:     models.JSON{},
		SeoMetaJSON:     models.JSON{},
		Tags:            models.StringArray{"x", "tgx"},
		IsMapped:        true,
		IsActive:        true,
	}
	if err := db.Create(&legacyProduct).Error; err != nil {
		t.Fatalf("seed product: %v", err)
	}
	legacySKU := models.ProductSKU{ProductID: legacyProduct.ID, SKUCode: "tgx-x-stale", SpecValuesJSON: models.JSON{"provider": "tgx", "platform": "x"}, IsActive: true}
	if err := db.Create(&legacySKU).Error; err != nil {
		t.Fatalf("seed sku: %v", err)
	}
	activeMapping := models.ProductMapping{
		ConnectionID:        101,
		LocalProductID:      legacyProduct.ID,
		UpstreamProductCode: "stale",
		Provider:            upstream.CatalogProviderFansGurus,
		Platform:            "instagram",
		UpstreamStatus:      models.UpstreamStatusActive,
		IsActive:            true,
	}
	if err := mappingRepo.Create(&activeMapping); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	legacyProduct2 := legacyProduct
	legacyProduct2.ID = 0
	legacyProduct2.Slug = "tgx-youtube-already-inactive"
	legacyProduct2.Tags = models.StringArray{"youtube", "tgx"}
	legacyProduct2.IsActive = false
	if err := db.Create(&legacyProduct2).Error; err != nil {
		t.Fatalf("seed inactive product: %v", err)
	}
	inactiveMapping := models.ProductMapping{
		ConnectionID:        101,
		LocalProductID:      legacyProduct2.ID,
		UpstreamProductCode: "already-inactive",
		Provider:            upstream.CatalogProviderFansGurus,
		Platform:            "youtube",
		UpstreamStatus:      models.UpstreamStatusInactive,
		IsActive:            false,
	}
	if err := mappingRepo.Create(&inactiveMapping); err != nil {
		t.Fatalf("seed inactive mapping: %v", err)
	}
	if err := db.Model(&models.ProductMapping{}).Where("id = ?", inactiveMapping.ID).Updates(map[string]interface{}{
		"is_active":       false,
		"upstream_status": models.UpstreamStatusInactive,
	}).Error; err != nil {
		t.Fatalf("force inactive mapping state: %v", err)
	}

	result, err := svc.SyncProviderCatalogWithClients(
		context.Background(),
		ProviderCatalogSyncInput{FansGurusConnectionID: 101, TGXConnectionID: 202},
		fakeFansGurusCatalogClient{services: []upstream.FansGurusService{
			{Service: 1, Name: "Instagram Followers", Category: "Instagram", Rate: "2.00", Min: 500, Max: 10000},
		}},
		fakeTGXCatalogClient{items: []upstream.TGXCommodity{
			{Code: "IG-001", Name: "IG Account", Description: "Instagram aged account", Price: "100.00", Minimum: 1},
		}},
	)
	if err != nil {
		t.Fatalf("SyncProviderCatalogWithClients: %v", err)
	}
	if result.Deactivated != 1 {
		t.Fatalf("deactivated=%d, want 1", result.Deactivated)
	}

	var got models.ProductMapping
	if err := db.First(&got, activeMapping.ID).Error; err != nil {
		t.Fatalf("load stale mapping: %v", err)
	}
	if got.IsActive || got.UpstreamStatus != models.UpstreamStatusInactive {
		t.Fatalf("stale mapping was not deactivated: %+v", got)
	}
	var cleaned models.Product
	if err := db.Preload("Category").Preload("SKUs").First(&cleaned, legacyProduct.ID).Error; err != nil {
		t.Fatalf("load cleaned product: %v", err)
	}
	if cleaned.IsActive || cleaned.Category.Slug != "provider-catalog-excluded" || cleaned.Category.IsActive {
		t.Fatalf("excluded product was not isolated: %+v category=%+v", cleaned, cleaned.Category)
	}
	if len(cleaned.Tags) != 0 || cleaned.Slug != fmt.Sprintf("catalog-excluded-%d", legacyProduct.ID) {
		t.Fatalf("excluded product metadata was not cleaned: slug=%q tags=%v", cleaned.Slug, cleaned.Tags)
	}
	var alreadyInactive models.Product
	if err := db.Preload("Category").First(&alreadyInactive, legacyProduct2.ID).Error; err != nil {
		t.Fatalf("load already inactive product: %v", err)
	}
	if len(alreadyInactive.Tags) != 0 || alreadyInactive.Category.Slug != "provider-catalog-excluded" || alreadyInactive.Slug != fmt.Sprintf("catalog-excluded-%d", legacyProduct2.ID) {
		t.Fatalf("already inactive product was not cleaned: %+v category=%+v", alreadyInactive, alreadyInactive.Category)
	}
}
