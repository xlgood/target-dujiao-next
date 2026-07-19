package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/provider"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupAdminProductHandlerTest(t *testing.T) (*Handler, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:admin_product_handler_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Category{},
		&models.Product{},
		&models.ProductSKU{},
		&models.CardSecret{},
		&models.CardSecretBatch{},
		&models.MemberLevelPrice{},
		&models.CartItem{},
		&models.ProductMapping{},
		&models.SKUMapping{},
		&models.Order{},
		&models.OrderItem{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	productService := service.NewProductService(
		repository.NewProductRepository(db),
		repository.NewProductSKURepository(db),
		repository.NewCardSecretRepository(db),
		repository.NewCardSecretBatchRepository(db),
		repository.NewCategoryRepository(db),
		repository.NewMemberLevelPriceRepository(db),
		repository.NewCartRepository(db),
		repository.NewProductMappingRepository(db),
		repository.NewOrderRepository(db),
		repository.NewPaymentChannelRepository(db),
	)

	h := &Handler{Container: &provider.Container{
		ProductService: productService,
	}}
	return h, db
}

func TestBatchUpdateProductStatusReturnsFailureReasons(t *testing.T) {
	h, db := setupAdminProductHandlerTest(t)

	product := models.Product{
		CategoryID:      0,
		Slug:            "batch-uncategorized-product",
		TitleJSON:       models.JSON{"zh-CN": "batch-uncategorized-product"},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
		FulfillmentType: constants.FulfillmentTypeUpstream,
		IsMapped:        true,
		IsActive:        false,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create uncategorized product failed: %v", err)
	}

	body := fmt.Sprintf(`{"ids":[%d],"is_active":true}`, product.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/products/batch-status", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.BatchUpdateProductStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Total        int `json:"total"`
			SuccessCount int `json:"success_count"`
			FailedItems  []struct {
				ID        uint   `json:"id"`
				ErrorCode string `json:"error_code"`
				Message   string `json:"message"`
			} `json:"failed_items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, w.Body.String())
	}
	if resp.Data.Total != 1 || resp.Data.SuccessCount != 0 {
		t.Fatalf("unexpected counts: total=%d success=%d", resp.Data.Total, resp.Data.SuccessCount)
	}
	if len(resp.Data.FailedItems) != 1 {
		t.Fatalf("expected one failed item, got %+v", resp.Data.FailedItems)
	}
	if resp.Data.FailedItems[0].ID != product.ID {
		t.Fatalf("expected failed product id %d, got %d", product.ID, resp.Data.FailedItems[0].ID)
	}
	if resp.Data.FailedItems[0].ErrorCode != "product_category_invalid" {
		t.Fatalf("expected product_category_invalid, got %q", resp.Data.FailedItems[0].ErrorCode)
	}
}

func TestQuickUpdateProductRequiresCatalogReview(t *testing.T) {
	h, db := setupAdminProductHandlerTest(t)

	category := models.Category{
		Slug:     "catalog-review-category",
		NameJSON: models.JSON{"zh-CN": "catalog-review-category"},
		IsActive: true,
	}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category failed: %v", err)
	}
	product := models.Product{
		CategoryID:      category.ID,
		Slug:            "pending-provider-catalog-product",
		TitleJSON:       models.JSON{"zh-CN": "Pending provider catalog product"},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
		FulfillmentType: constants.FulfillmentTypeUpstream,
		IsMapped:        true,
		IsActive:        false,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}
	mapping := models.ProductMapping{
		ConnectionID:        1,
		LocalProductID:      product.ID,
		Provider:            "fansgurus",
		CatalogReviewStatus: models.CatalogReviewPending,
		IsActive:            true,
	}
	if err := db.Create(&mapping).Error; err != nil {
		t.Fatalf("create product mapping failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/products/1", strings.NewReader(`{"is_active":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", product.ID)}}

	h.QuickUpdateProduct(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 envelope, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		StatusCode int    `json:"status_code"`
		Msg        string `json:"msg"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, w.Body.String())
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected business status 400, got %d body=%s", resp.StatusCode, w.Body.String())
	}
	if resp.Msg != "供应商目录商品需先在“商品映射”中审核后才能上架" {
		t.Fatalf("unexpected review message: %q", resp.Msg)
	}
}

func TestUpdateProductWholesalePricesHandlerUpdatesTiers(t *testing.T) {
	h, db := setupAdminProductHandlerTest(t)

	product := models.Product{
		CategoryID:  1,
		Slug:        "handler-wholesale-product",
		TitleJSON:   models.JSON{"zh-CN": "handler-wholesale-product"},
		PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		IsActive:    true,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	body := `{"wholesale_prices":[{"min_quantity":10,"unit_price":70},{"min_quantity":5,"unit_price":80}]}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/products/1/wholesale-prices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", product.ID)}}

	h.UpdateProductWholesalePrices(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data models.Product `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, w.Body.String())
	}
	if len(resp.Data.WholesalePrices) != 2 {
		t.Fatalf("expected 2 wholesale tiers, got %+v", resp.Data.WholesalePrices)
	}
	if resp.Data.WholesalePrices[0].MinQuantity != 5 || resp.Data.WholesalePrices[0].UnitPrice.String() != "80.00" {
		t.Fatalf("expected sorted first tier min=5 price=80.00, got %+v", resp.Data.WholesalePrices[0])
	}
}

func TestUpdateProductWholesalePricesHandlerAllowsClear(t *testing.T) {
	h, db := setupAdminProductHandlerTest(t)

	product := models.Product{
		CategoryID:  1,
		Slug:        "handler-wholesale-clear",
		TitleJSON:   models.JSON{"zh-CN": "handler-wholesale-clear"},
		PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		WholesalePrices: models.WholesalePriceTiers{
			{MinQuantity: 5, UnitPrice: models.NewMoneyFromDecimal(decimal.NewFromInt(80))},
		},
		IsActive: true,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/products/1/wholesale-prices", strings.NewReader(`{"wholesale_prices":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", product.ID)}}

	h.UpdateProductWholesalePrices(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got models.Product
	if err := db.First(&got, product.ID).Error; err != nil {
		t.Fatalf("reload product failed: %v", err)
	}
	if len(got.WholesalePrices) != 0 {
		t.Fatalf("expected wholesale prices cleared, got %+v", got.WholesalePrices)
	}
}

func TestUpdateProductWholesalePricesHandlerRejectsInvalidTier(t *testing.T) {
	h, db := setupAdminProductHandlerTest(t)

	product := models.Product{
		CategoryID:  1,
		Slug:        "handler-wholesale-invalid",
		TitleJSON:   models.JSON{"zh-CN": "handler-wholesale-invalid"},
		PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		IsActive:    true,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/products/1/wholesale-prices", strings.NewReader(`{"wholesale_prices":[{"min_quantity":0,"unit_price":80}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", product.ID)}}

	h.UpdateProductWholesalePrices(c)

	if w.Code != http.StatusOK {
		t.Fatalf("project response wrapper should still return HTTP 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		StatusCode int    `json:"status_code"`
		Msg        string `json:"msg"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, w.Body.String())
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected wholesale invalid response, got status_code=%d body=%s", resp.StatusCode, w.Body.String())
	}
}

func TestUpdateProductWholesalePricesHandlerReturnsNotFound(t *testing.T) {
	h, _ := setupAdminProductHandlerTest(t)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/products/999999/wholesale-prices", strings.NewReader(`{"wholesale_prices":[{"min_quantity":5,"unit_price":80}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "999999"}}

	h.UpdateProductWholesalePrices(c)

	if w.Code != http.StatusOK {
		t.Fatalf("project response wrapper should still return HTTP 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		StatusCode int `json:"status_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, w.Body.String())
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected product not found response, got body=%s", w.Body.String())
	}
}

func TestApplyUpstreamDisplayTypesKeepsUpstreamStockSeparateFromManualStock(t *testing.T) {
	h, db := setupAdminProductHandlerTest(t)
	h.ProductMappingRepo = repository.NewProductMappingRepository(db)
	h.SKUMappingRepo = repository.NewSKUMappingRepository(db)

	product := models.Product{
		CategoryID: 1, Slug: "tgx-stock-product", TitleJSON: models.JSON{"zh-CN": "TGX"},
		PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(10)), FulfillmentType: constants.FulfillmentTypeUpstream,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}
	sku := models.ProductSKU{ProductID: product.ID, SKUCode: "TGX-1", PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(10)), ManualStockTotal: 0, IsActive: true}
	if err := db.Create(&sku).Error; err != nil {
		t.Fatalf("create sku: %v", err)
	}
	mapping := models.ProductMapping{ConnectionID: 1, LocalProductID: product.ID, Provider: "tgx", UpstreamFulfillmentType: constants.FulfillmentTypeManual, IsActive: true}
	if err := db.Create(&mapping).Error; err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	now := time.Now()
	if err := db.Create(&models.SKUMapping{ProductMappingID: mapping.ID, LocalSKUID: sku.ID, UpstreamStock: 27, UpstreamIsActive: true, StockSyncedAt: &now}).Error; err != nil {
		t.Fatalf("create sku mapping: %v", err)
	}

	product.SKUs = []models.ProductSKU{sku}
	products := []models.Product{product}
	h.applyUpstreamDisplayTypes(products)
	got := products[0]
	if !got.UpstreamStockManaged || got.UpstreamStockAvailable != 27 || got.ManualStockTotal != 0 || got.SKUs[0].ManualStockTotal != 0 {
		t.Fatalf("upstream stock must not masquerade as manual stock: %+v", got)
	}
}
