package public

import (
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/provider"
	"github.com/dujiao-next/internal/repository"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestDecorateProductStock_AutoSkipsInactiveSKUs(t *testing.T) {
	h := &Handler{}
	product := &models.Product{
		ID:              1,
		FulfillmentType: constants.FulfillmentTypeAuto,
		SKUs: []models.ProductSKU{
			{
				ID:                 11,
				SKUCode:            models.DefaultSKUCode,
				IsActive:           true,
				AutoStockAvailable: 2,
				AutoStockTotal:     3,
				AutoStockLocked:    1,
				AutoStockSold:      4,
			},
			{
				ID:                 12,
				SKUCode:            "DISABLED",
				IsActive:           false,
				AutoStockAvailable: 100,
				AutoStockTotal:     120,
				AutoStockLocked:    20,
				AutoStockSold:      50,
			},
		},
	}

	item := publicProductView{Product: *product}
	h.decorateProductStock(product, &item)

	if item.AutoStockAvailable != 2 {
		t.Fatalf("expected auto_stock_available=2, got %d", item.AutoStockAvailable)
	}
	if item.AutoStockTotal != 3 {
		t.Fatalf("expected auto_stock_total=3, got %d", item.AutoStockTotal)
	}
	if item.AutoStockLocked != 1 {
		t.Fatalf("expected auto_stock_locked=1, got %d", item.AutoStockLocked)
	}
	if item.AutoStockSold != 4 {
		t.Fatalf("expected auto_stock_sold=4, got %d", item.AutoStockSold)
	}
	if item.IsSoldOut {
		t.Fatalf("expected product not sold out when active sku has stock")
	}
}

func TestDecoratePublicProductTGXPendingStockIsNotUnlimited(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:public_tgx_stock_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.Product{}, &models.ProductSKU{}, &models.ProductMapping{}, &models.SKUMapping{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	product := &models.Product{
		Slug:            "tgx-pending-stock",
		TitleJSON:       models.JSON{"zh-CN": "TGX pending"},
		FulfillmentType: constants.FulfillmentTypeUpstream,
		IsActive:        true,
	}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}
	sku := &models.ProductSKU{ProductID: product.ID, SKUCode: models.DefaultSKUCode, IsActive: true}
	if err := db.Create(sku).Error; err != nil {
		t.Fatalf("create SKU: %v", err)
	}
	mapping := &models.ProductMapping{LocalProductID: product.ID, ConnectionID: 1, Provider: "tgx", UpstreamFulfillmentType: constants.FulfillmentTypeManual, IsActive: true}
	if err := db.Create(mapping).Error; err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	if err := db.Create(&models.SKUMapping{ProductMappingID: mapping.ID, LocalSKUID: sku.ID, UpstreamStock: -1, UpstreamIsActive: true}).Error; err != nil {
		t.Fatalf("create SKU mapping: %v", err)
	}
	product.SKUs = []models.ProductSKU{*sku}

	h := &Handler{Container: &provider.Container{
		ProductMappingRepo: repository.NewProductMappingRepository(db),
		SKUMappingRepo:     repository.NewSKUMappingRepository(db),
	}}
	resp, err := h.decoratePublicProduct(product, nil)
	if err != nil {
		t.Fatalf("decorate product: %v", err)
	}
	if resp.StockStatus != constants.ProductStockStatusPending || !resp.UpstreamStockUnknown {
		t.Fatalf("unexpected product stock state: status=%q unknown=%t", resp.StockStatus, resp.UpstreamStockUnknown)
	}
	if resp.StockDisplay != constants.ProductStockStatusPending {
		t.Fatalf("expected pending display, got %q", resp.StockDisplay)
	}
	if !resp.UpstreamFulfillment || resp.FulfillmentType != constants.FulfillmentTypeManual {
		t.Fatalf("expected upstream fulfillment display, got upstream=%t type=%q", resp.UpstreamFulfillment, resp.FulfillmentType)
	}
	if len(resp.SKUs) != 1 {
		t.Fatalf("expected one SKU, got %d", len(resp.SKUs))
	}
	if resp.SKUs[0].StockStatus != constants.ProductStockStatusPending || !resp.SKUs[0].UpstreamStockUnknown || resp.SKUs[0].UpstreamStock != 0 {
		t.Fatalf("unexpected SKU stock state: %+v", resp.SKUs[0])
	}
}

func TestPublicProductResponseStatusModeMasksExactStock(t *testing.T) {
	h := &Handler{}
	product := &models.Product{
		ID:               1,
		FulfillmentType:  constants.FulfillmentTypeManual,
		StockDisplayMode: constants.ProductStockDisplayStatus,
		SKUs: []models.ProductSKU{
			{
				ID:               11,
				SKUCode:          models.DefaultSKUCode,
				IsActive:         true,
				ManualStockTotal: 37,
			},
		},
	}

	resp, err := h.decoratePublicProduct(product, nil)
	if err != nil {
		t.Fatalf("decoratePublicProduct failed: %v", err)
	}

	if resp.StockDisplayMode != constants.ProductStockDisplayStatus {
		t.Fatalf("expected stock_display_mode=status, got %q", resp.StockDisplayMode)
	}
	if !resp.StockQuantityHidden {
		t.Fatalf("expected product stock quantity to be hidden")
	}
	if resp.ManualStockAvailable == 37 {
		t.Fatalf("expected product manual stock to be masked, got exact value %d", resp.ManualStockAvailable)
	}
	if resp.StockDisplay != constants.ProductStockStatusInStock {
		t.Fatalf("expected product stock display in_stock, got %q", resp.StockDisplay)
	}
	if len(resp.SKUs) != 1 {
		t.Fatalf("expected one sku, got %d", len(resp.SKUs))
	}
	sku := resp.SKUs[0]
	if !sku.StockQuantityHidden {
		t.Fatalf("expected sku stock quantity to be hidden")
	}
	if sku.ManualStockTotal == 37 {
		t.Fatalf("expected sku manual stock to be masked, got exact value %d", sku.ManualStockTotal)
	}
	if sku.StockStatus != constants.ProductStockStatusInStock {
		t.Fatalf("expected sku stock status in_stock, got %q", sku.StockStatus)
	}
	if sku.IsSoldOut {
		t.Fatalf("expected sku to remain purchasable")
	}
}

func TestPublicProductResponseRangeModeReturnsBucketOnly(t *testing.T) {
	h := &Handler{}
	product := &models.Product{
		ID:               1,
		FulfillmentType:  constants.FulfillmentTypeManual,
		StockDisplayMode: constants.ProductStockDisplayRange,
		SKUs: []models.ProductSKU{
			{
				ID:               11,
				SKUCode:          models.DefaultSKUCode,
				IsActive:         true,
				ManualStockTotal: 42,
			},
		},
	}

	resp, err := h.decoratePublicProduct(product, nil)
	if err != nil {
		t.Fatalf("decoratePublicProduct failed: %v", err)
	}

	if resp.StockDisplay != constants.ProductStockDisplayRange21To50 {
		t.Fatalf("expected product stock range 21-50, got %q", resp.StockDisplay)
	}
	if resp.StockRangeMin == nil || *resp.StockRangeMin != 21 {
		t.Fatalf("expected range min 21, got %+v", resp.StockRangeMin)
	}
	if resp.StockRangeMax == nil || *resp.StockRangeMax != 50 {
		t.Fatalf("expected range max 50, got %+v", resp.StockRangeMax)
	}
	if resp.ManualStockAvailable == 42 {
		t.Fatalf("expected product manual stock to be masked, got exact value %d", resp.ManualStockAvailable)
	}
	if len(resp.SKUs) != 1 {
		t.Fatalf("expected one sku, got %d", len(resp.SKUs))
	}
	sku := resp.SKUs[0]
	if sku.StockDisplay != constants.ProductStockDisplayRange21To50 {
		t.Fatalf("expected sku stock range 21-50, got %q", sku.StockDisplay)
	}
	if sku.StockRangeMin == nil || *sku.StockRangeMin != 21 {
		t.Fatalf("expected sku range min 21, got %+v", sku.StockRangeMin)
	}
	if sku.StockRangeMax == nil || *sku.StockRangeMax != 50 {
		t.Fatalf("expected sku range max 50, got %+v", sku.StockRangeMax)
	}
	if sku.ManualStockTotal == 42 {
		t.Fatalf("expected sku manual stock to be masked, got exact value %d", sku.ManualStockTotal)
	}
}
