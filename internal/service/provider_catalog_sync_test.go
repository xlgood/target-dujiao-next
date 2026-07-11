package service

import (
	"context"
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
	if len(result.SupportedPlatforms) != 1 || result.SupportedPlatforms[0] != "instagram" {
		t.Fatalf("supported platforms=%v, want [instagram]", result.SupportedPlatforms)
	}
	if result.FilteredTelegram != 1 || result.FilteredPlatform != 2 {
		t.Fatalf("unexpected filter counts: %+v", result)
	}
	if result.Imported != 2 || result.Skipped != 0 {
		t.Fatalf("unexpected import counts: %+v", result)
	}
	if result.Deactivated != 0 {
		t.Fatalf("deactivated=%d, want 0", result.Deactivated)
	}

	var mappings []models.ProductMapping
	if err := db.Order("provider ASC").Find(&mappings).Error; err != nil {
		t.Fatalf("load mappings: %v", err)
	}
	if len(mappings) != 2 {
		t.Fatalf("mapping count=%d, want 2", len(mappings))
	}
	for _, mapping := range mappings {
		switch mapping.Provider {
		case upstream.CatalogProviderFansGurus:
			if mapping.ConnectionID != 101 || mapping.UpstreamProductCode != "1" {
				t.Fatalf("unexpected fans mapping: %+v", mapping)
			}
		case upstream.CatalogProviderTGX:
			if mapping.ConnectionID != 202 || mapping.UpstreamProductCode != "IG-001" {
				t.Fatalf("unexpected tgx mapping: %+v", mapping)
			}
		default:
			t.Fatalf("unexpected provider mapping: %+v", mapping)
		}
	}

	var run models.ProviderCatalogSyncRun
	if err := db.First(&run).Error; err != nil {
		t.Fatalf("load sync run: %v", err)
	}
	if run.Status != "success" || run.FansGurusPulled != 3 || run.TGXPulled != 2 || run.Imported != 2 {
		t.Fatalf("unexpected sync run: %+v", run)
	}
	if run.RawFansGurusJSON["services"] == nil || run.RawTGXJSON["items"] == nil {
		t.Fatalf("raw payloads should be stored: %+v", run)
	}
}

func TestSyncProviderCatalogWithClientsSkipsUnsupportedFansGurusTypes(t *testing.T) {
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
	if result.Imported != 2 {
		t.Fatalf("imported=%d, want 2", result.Imported)
	}
	var unsupported int64
	if err := db.Model(&models.ProductMapping{}).Where("upstream_product_code = ?", "2").Count(&unsupported).Error; err != nil {
		t.Fatalf("count unsupported mapping: %v", err)
	}
	if unsupported != 0 {
		t.Fatalf("unsupported service was imported")
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

	activeMapping := models.ProductMapping{
		ConnectionID:        101,
		LocalProductID:      999,
		UpstreamProductCode: "stale",
		Provider:            upstream.CatalogProviderFansGurus,
		Platform:            "instagram",
		UpstreamStatus:      models.UpstreamStatusActive,
		IsActive:            true,
	}
	if err := mappingRepo.Create(&activeMapping); err != nil {
		t.Fatalf("seed mapping: %v", err)
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
}
