package service

import (
	"context"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/upstream"
)

type fakeFansGurusCatalogContentClient struct {
	details []upstream.FansGurusCatalogDetail
	err     error
}

func (c fakeFansGurusCatalogContentClient) ListCatalogDetails(context.Context) ([]upstream.FansGurusCatalogDetail, error) {
	return c.details, c.err
}

func TestProviderCatalogContentSyncSanitizesFansGurusAndTGXCopy(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	if err := db.AutoMigrate(&models.ProviderCatalogContentSyncRun{}); err != nil {
		t.Fatalf("auto migrate content sync run: %v", err)
	}
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db), repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db), repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db), nil,
	)
	svc.SetProviderCatalogContentSyncRunRepository(repository.NewProviderCatalogContentSyncRunRepository(db))
	catalog := upstream.FilteredCatalog{
		FansGurus: []upstream.ProviderCatalogItem{{Provider: upstream.CatalogProviderFansGurus, Code: "14266", Name: "Instagram Followers", Category: "Instagram", UpstreamPrice: "1", TargetPrice: "1", Active: true}},
		TGX:       []upstream.ProviderCatalogItem{{Provider: upstream.CatalogProviderTGX, Code: "TGX-1", Name: "Outlook account", Category: "Outlook", UpstreamPrice: "1", TargetPrice: "1", Active: true}},
	}
	if _, err := svc.ImportProviderCatalogByProviderConnections(map[string]uint{upstream.CatalogProviderFansGurus: 10, upstream.CatalogProviderTGX: 20}, catalog); err != nil {
		t.Fatalf("import catalog: %v", err)
	}
	result, err := svc.SyncProviderCatalogContentWithClients(context.Background(), ProviderCatalogContentSyncInput{FansGurusConnectionID: 10, TGXConnectionID: 20},
		fakeFansGurusCatalogContentClient{details: []upstream.FansGurusCatalogDetail{{Service: 14266, Name: "Instagram Followers", Category: "Instagram 推荐", AverageTime: "4 hours", Description: "Quality: real<br>Source: our platform<br>Contact support at https://source.example/help<br>Start: 0-6 hours"}}},
		fakeTGXCatalogClient{items: []upstream.TGXCommodity{{Code: "TGX-1", Name: "Outlook account", Category: "Outlook", Description: "Account format: email----password<br>Contact platform merchant: https://tgx.example/help<br>Use Outlook after delivery", Price: "1"}}},
	)
	if err != nil {
		t.Fatalf("sync content: %v", err)
	}
	if result.Matched != 2 || result.Updated != 2 {
		t.Fatalf("result=%+v", result)
	}
	var mappings []models.ProductMapping
	if err := db.Order("provider ASC").Find(&mappings).Error; err != nil {
		t.Fatalf("mappings: %v", err)
	}
	if len(mappings) != 2 || mappings[0].CatalogSourceDescription == "" || mappings[1].CatalogSourceDescription == "" {
		t.Fatalf("source snapshots missing: %+v", mappings)
	}
	for _, mapping := range mappings {
		var product models.Product
		if err := db.First(&product, mapping.LocalProductID).Error; err != nil {
			t.Fatalf("product: %v", err)
		}
		content := product.ContentJSON["zh-CN"].(string)
		lower := strings.ToLower(content)
		for _, forbidden := range []string{"source.example", "tgx.example", "platform merchant", "our platform", "contact support", "供应商", "上游"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("customer content leaked %q: %s", forbidden, content)
			}
		}
	}
	var run models.ProviderCatalogContentSyncRun
	if err := db.First(&run).Error; err != nil || run.Status != "success" || run.Updated != 2 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
}

func TestProviderCatalogSyncKeepsSynchronizedContent(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db), repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db), repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db), nil,
	)
	catalog := upstream.FilteredCatalog{FansGurus: []upstream.ProviderCatalogItem{{Provider: upstream.CatalogProviderFansGurus, Code: "14266", Name: "Instagram Followers", Category: "Instagram", UpstreamPrice: "1", TargetPrice: "1", Active: true}}}
	if _, err := svc.ImportProviderCatalogByProviderConnections(map[string]uint{upstream.CatalogProviderFansGurus: 10}, catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncProviderCatalogContentWithClients(context.Background(), ProviderCatalogContentSyncInput{FansGurusConnectionID: 10}, fakeFansGurusCatalogContentClient{details: []upstream.FansGurusCatalogDetail{{Service: 14266, Name: "Instagram Followers", Description: "Verified detail"}}}, fakeTGXCatalogClient{}); err != nil {
		t.Fatal(err)
	}
	catalog.FansGurus[0].Name = "Instagram Followers Updated"
	if _, err := svc.ImportProviderCatalogByProviderConnections(map[string]uint{upstream.CatalogProviderFansGurus: 10}, catalog); err != nil {
		t.Fatal(err)
	}
	var product models.Product
	if err := db.First(&product).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(product.ContentJSON["zh-CN"].(string), "Verified detail") {
		t.Fatalf("catalog sync overwrote enriched content: %v", product.ContentJSON)
	}
}

func TestProviderCatalogCustomerContentUsesServiceTypeForCustomComments(t *testing.T) {
	content, _ := providerCatalogCustomerContent(providerCatalogContentSource{
		Provider:    upstream.CatalogProviderFansGurus,
		Name:        "Instagram comments",
		ServiceType: "Custom Comments",
		Description: "Quality: real",
	})
	zhCN, _ := content["zh-CN"].(string)
	if !strings.Contains(zhCN, "每行填写一条评论") {
		t.Fatalf("custom comments instruction missing: %s", zhCN)
	}
}

func TestSanitizeProviderCatalogLinesDropsExternalURLLines(t *testing.T) {
	lines := sanitizeProviderCatalogLines("Quality: real<br>Proof: https://example.com/image.jpg<br>Start: 0-6 hours")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Proof") || strings.Contains(joined, "example.com") {
		t.Fatalf("external URL line leaked: %q", joined)
	}
	if !strings.Contains(joined, "Quality: real") || !strings.Contains(joined, "Start: 0-6 hours") {
		t.Fatalf("safe lines lost: %q", joined)
	}
}

func TestProviderCatalogContentSyncSkipsInactiveMappings(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db), repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db), repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db), nil,
	)
	catalog := upstream.FilteredCatalog{FansGurus: []upstream.ProviderCatalogItem{{Provider: upstream.CatalogProviderFansGurus, Code: "14266", Name: "Instagram Followers", Category: "Instagram", UpstreamPrice: "1", TargetPrice: "1", Active: true}}}
	if _, err := svc.ImportProviderCatalogByProviderConnections(map[string]uint{upstream.CatalogProviderFansGurus: 10}, catalog); err != nil {
		t.Fatal(err)
	}
	var mapping models.ProductMapping
	if err := db.First(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	mapping.IsActive = false
	if err := db.Save(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	result, err := svc.SyncProviderCatalogContentWithClients(context.Background(), ProviderCatalogContentSyncInput{FansGurusConnectionID: 10}, fakeFansGurusCatalogContentClient{details: []upstream.FansGurusCatalogDetail{{Service: 14266, Name: "Instagram Followers", Description: "Should not apply"}}}, fakeTGXCatalogClient{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 0 || result.Skipped != 1 {
		t.Fatalf("result=%+v", result)
	}
}
