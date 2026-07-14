package service

import (
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/upstream"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestImportProviderCatalogCreatesProductsAndMappings(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db),
		repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db),
		repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db),
		nil,
	)

	catalog := upstream.BuildFilteredCatalog(
		[]upstream.ProviderCatalogItem{
			{
				Provider:      upstream.CatalogProviderFansGurus,
				Code:          "16252",
				Name:          "Instagram Followers",
				Category:      "Instagram",
				UpstreamPrice: "2.00",
				TargetPrice:   "10.00000000",
				MinQuantity:   500,
				MaxQuantity:   10000,
				Active:        true,
			},
			{
				Provider:      upstream.CatalogProviderFansGurus,
				Code:          "tg-1",
				Name:          "Telegram Members",
				Category:      "Telegram",
				UpstreamPrice: "1.00",
				TargetPrice:   "5.00000000",
				Active:        true,
			},
		},
		[]upstream.ProviderCatalogItem{
			{
				Provider:      upstream.CatalogProviderTGX,
				Code:          "IG-001",
				Name:          "IG Account",
				Description:   "Instagram aged account",
				UpstreamPrice: "100.00",
				TargetPrice:   "120.00000000",
				MinQuantity:   1,
				Active:        true,
			},
			{
				Provider:      upstream.CatalogProviderTGX,
				Code:          "FB-001",
				Name:          "Facebook Account",
				Description:   "Facebook account",
				UpstreamPrice: "80.00",
				TargetPrice:   "96.00000000",
				Active:        true,
			},
		},
	)

	result, err := svc.ImportProviderCatalog(10, catalog)
	if err != nil {
		t.Fatalf("ImportProviderCatalog: %v", err)
	}
	if result.Imported != 2 || result.Skipped != 0 {
		t.Fatalf("result=%+v, want imported=2 skipped=0", result)
	}

	var categories []models.Category
	if err := db.Find(&categories).Error; err != nil {
		t.Fatalf("load categories: %v", err)
	}
	if len(categories) != 1 || categories[0].Slug != "platform-instagram" {
		t.Fatalf("categories=%+v, want only platform-instagram", categories)
	}

	var products []models.Product
	if err := db.Preload("SKUs").Order("slug ASC").Find(&products).Error; err != nil {
		t.Fatalf("load products: %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("product count=%d, want 2", len(products))
	}
	for _, product := range products {
		if product.FulfillmentType != constants.FulfillmentTypeUpstream || !product.IsMapped || product.IsActive {
			t.Fatalf("unexpected product flags: %+v", product)
		}
		if len(product.SKUs) != 1 {
			t.Fatalf("product %s sku count=%d, want 1", product.Slug, len(product.SKUs))
		}
	}

	var fansMapping models.ProductMapping
	if err := db.Where("upstream_product_code = ?", "16252").First(&fansMapping).Error; err != nil {
		t.Fatalf("load fans mapping: %v", err)
	}
	if fansMapping.Provider != upstream.CatalogProviderFansGurus || fansMapping.Platform != "instagram" {
		t.Fatalf("unexpected fans mapping: %+v", fansMapping)
	}
	var fansProduct models.Product
	if err := db.Preload("SKUs").First(&fansProduct, fansMapping.LocalProductID).Error; err != nil {
		t.Fatalf("load fans product: %v", err)
	}
	if fansProduct.PriceQuantityBasis != 1000 || len(fansProduct.SKUs) != 1 || fansProduct.SKUs[0].PriceQuantityBasis != 1000 {
		t.Fatalf("fans price basis product=%d skus=%+v, want 1000", fansProduct.PriceQuantityBasis, fansProduct.SKUs)
	}

	var tgxMapping models.ProductMapping
	if err := db.Where("upstream_product_code = ?", "IG-001").First(&tgxMapping).Error; err != nil {
		t.Fatalf("load tgx mapping: %v", err)
	}
	if tgxMapping.Provider != upstream.CatalogProviderTGX || tgxMapping.Platform != "instagram" {
		t.Fatalf("unexpected tgx mapping: %+v", tgxMapping)
	}

	var skuMappings []models.SKUMapping
	if err := db.Order("upstream_sku_code ASC").Find(&skuMappings).Error; err != nil {
		t.Fatalf("load sku mappings: %v", err)
	}
	if len(skuMappings) != 2 {
		t.Fatalf("sku mapping count=%d, want 2", len(skuMappings))
	}
	if skuMappings[0].UpstreamSKUCode == "" || skuMappings[1].UpstreamSKUCode == "" {
		t.Fatalf("sku mappings should keep upstream sku codes: %+v", skuMappings)
	}
}

func TestImportProviderCatalogRefreshesExistingMapping(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db),
		repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db),
		repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db),
		nil,
	)

	catalog := upstream.BuildFilteredCatalog(
		[]upstream.ProviderCatalogItem{
			{
				Provider:      upstream.CatalogProviderFansGurus,
				Code:          "16252",
				Name:          "Instagram Followers",
				Category:      "Instagram",
				UpstreamPrice: "2.00",
				TargetPrice:   "10.00000000",
				Active:        true,
			},
		},
		[]upstream.ProviderCatalogItem{
			{
				Provider:      upstream.CatalogProviderTGX,
				Code:          "IG-001",
				Name:          "IG Account",
				Description:   "Instagram aged account",
				UpstreamPrice: "100.00",
				TargetPrice:   "120.00000000",
				Active:        true,
			},
		},
	)

	first, err := svc.ImportProviderCatalog(10, catalog)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	updatedCatalog := catalog
	updatedCatalog.FansGurus[0].TargetPrice = "12.00000000"
	updatedCatalog.FansGurus[0].UpstreamPrice = "2.40"
	updatedCatalog.FansGurus[0].MinQuantity = 1000
	second, err := svc.ImportProviderCatalog(10, updatedCatalog)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if first.Imported != 2 || second.Imported != 0 || second.Updated != 2 || second.Skipped != 0 {
		t.Fatalf("unexpected results: first=%+v second=%+v", first, second)
	}
	var mapping models.ProductMapping
	if err := db.Where("connection_id = ? AND upstream_product_code = ?", 10, "16252").First(&mapping).Error; err != nil {
		t.Fatalf("load mapping: %v", err)
	}
	var product models.Product
	if err := db.Preload("SKUs").First(&product, mapping.LocalProductID).Error; err != nil {
		t.Fatalf("load product: %v", err)
	}
	if product.PriceAmount.String() != "12.00" || product.MinPurchaseQuantity != 1000 || len(product.SKUs) != 1 || product.SKUs[0].PriceAmount.String() != "12.00" {
		t.Fatalf("existing catalog row was not refreshed: product=%+v skus=%+v", product, product.SKUs)
	}
	if len(product.Tags) != 0 {
		t.Fatalf("provider catalog products should not publish internal tags: %v", product.Tags)
	}
	if product.Slug != "catalog-instagram-28c0bd9bfab7eb10" {
		t.Fatalf("provider-neutral slug was not applied: %s", product.Slug)
	}
	if _, exists := product.SKUs[0].SpecValuesJSON["provider"]; exists {
		t.Fatalf("provider leaked into public SKU specs: %v", product.SKUs[0].SpecValuesJSON)
	}
}

func TestImportProviderCatalogCreatesTGXRaceSKUsAndWidgetSchema(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db),
		repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db),
		repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db),
		nil,
	)

	tgxItem, err := upstream.NewTGXCatalogItem(upstream.TGXCommodity{
		Code:        "IG-001",
		Name:        "IG Account",
		Description: "Instagram aged account",
		Price:       "100.00",
		Config:      []byte(`{"category[普通]":"100.00","category[高级]":"150.00"}`),
		Widget:      []byte(`[{"name":"email","label":"Email","type":"text","required":true}]`),
		Minimum:     1,
	})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem: %v", err)
	}
	catalog := upstream.FilteredCatalog{TGX: []upstream.ProviderCatalogItem{tgxItem}}

	result, err := svc.ImportProviderCatalog(10, catalog)
	if err != nil {
		t.Fatalf("ImportProviderCatalog: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("result=%+v, want imported=1", result)
	}

	var mapping models.ProductMapping
	if err := db.Where("upstream_product_code = ?", "IG-001").First(&mapping).Error; err != nil {
		t.Fatalf("load mapping: %v", err)
	}
	var product models.Product
	if err := db.Preload("SKUs").First(&product, mapping.LocalProductID).Error; err != nil {
		t.Fatalf("load product: %v", err)
	}
	if len(product.SKUs) != 2 {
		t.Fatalf("sku count=%d, want 2", len(product.SKUs))
	}
	fields, ok := product.ManualFormSchemaJSON["fields"].([]interface{})
	if !ok || len(fields) != 1 {
		t.Fatalf("unexpected manual form schema: %+v", product.ManualFormSchemaJSON)
	}

	var skuMappings []models.SKUMapping
	if err := db.Order("upstream_sku_code ASC").Find(&skuMappings).Error; err != nil {
		t.Fatalf("load sku mappings: %v", err)
	}
	if len(skuMappings) != 2 {
		t.Fatalf("sku mapping count=%d, want 2", len(skuMappings))
	}
	gotCodes := []string{skuMappings[0].UpstreamSKUCode, skuMappings[1].UpstreamSKUCode}
	if gotCodes[0] != "IG-001|普通" || gotCodes[1] != "IG-001|高级" {
		t.Fatalf("upstream sku codes=%v", gotCodes)
	}
}

func TestImportProviderCatalogRefreshPreservesTGXRealTimeStock(t *testing.T) {
	db := setupProviderCatalogImportDB(t)
	svc := NewProductMappingService(
		repository.NewProductMappingRepository(db),
		repository.NewSKUMappingRepository(db),
		repository.NewProductRepository(db),
		repository.NewProductSKURepository(db),
		repository.NewCategoryRepository(db),
		nil,
	)

	item, err := upstream.NewTGXCatalogItem(upstream.TGXCommodity{
		Code: "IG-001", Name: "Instagram Account", Category: "Instagram", Price: "100.00",
	})
	if err != nil {
		t.Fatalf("NewTGXCatalogItem: %v", err)
	}
	catalog := upstream.FilteredCatalog{TGX: []upstream.ProviderCatalogItem{item}}
	if _, err := svc.ImportProviderCatalog(10, catalog); err != nil {
		t.Fatalf("first import: %v", err)
	}

	var mapping models.ProductMapping
	if err := db.Where("upstream_product_code = ?", "IG-001").First(&mapping).Error; err != nil {
		t.Fatalf("load mapping: %v", err)
	}
	var skuMapping models.SKUMapping
	if err := db.Where("product_mapping_id = ?", mapping.ID).First(&skuMapping).Error; err != nil {
		t.Fatalf("load SKU mapping: %v", err)
	}
	if err := db.Model(&models.SKUMapping{}).Where("id = ?", skuMapping.ID).Updates(map[string]interface{}{
		"upstream_stock": 27, "upstream_is_active": true,
	}).Error; err != nil {
		t.Fatalf("store real-time stock: %v", err)
	}

	if _, err := svc.ImportProviderCatalog(10, catalog); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if err := db.First(&skuMapping, skuMapping.ID).Error; err != nil {
		t.Fatalf("reload SKU mapping: %v", err)
	}
	if skuMapping.UpstreamStock != 27 || !skuMapping.UpstreamIsActive {
		t.Fatalf("catalog refresh overwrote real-time TGX stock: %+v", skuMapping)
	}
}

func setupProviderCatalogImportDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+normalizeProviderSlug(t.Name())+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Category{},
		&models.Product{},
		&models.ProductSKU{},
		&models.ProductMapping{},
		&models.SKUMapping{},
		&models.ProviderCatalogSyncRun{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}
