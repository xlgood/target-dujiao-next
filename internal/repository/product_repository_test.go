package repository

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupProductRepositoryTest(t *testing.T) (*GormProductRepository, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:product_repository_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Category{},
		&models.Product{},
		&models.ProductSKU{},
		&models.CardSecret{},
		&models.SiteConnection{},
		&models.ProductMapping{},
		&models.SKUMapping{},
	); err != nil {
		t.Fatalf("migrate product/sku/card_secret/mappings failed: %v", err)
	}
	defaultCategory := models.Category{
		ID:       1,
		Slug:     "default-test-category",
		NameJSON: models.JSON{"zh-CN": "default"},
		IsActive: true,
	}
	if err := db.Create(&defaultCategory).Error; err != nil {
		t.Fatalf("seed default category failed: %v", err)
	}
	return NewProductRepository(db), db
}

func createManualProduct(t *testing.T, repo *GormProductRepository, slug string, total int, locked int, sold int) *models.Product {
	t.Helper()
	product := &models.Product{
		CategoryID:        1,
		Slug:              slug,
		TitleJSON:         models.JSON{"zh-CN": "测试商品"},
		PriceAmount:       models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		PurchaseType:      constants.ProductPurchaseMember,
		FulfillmentType:   constants.FulfillmentTypeManual,
		ManualStockTotal:  total,
		ManualStockLocked: locked,
		ManualStockSold:   sold,
		IsActive:          true,
	}
	if err := repo.Create(product); err != nil {
		t.Fatalf("create product failed: %v", err)
	}
	return product
}

func createManualSKU(t *testing.T, db *gorm.DB, productID uint, code string, total int, locked int, sold int, isActive bool) *models.ProductSKU {
	t.Helper()
	sku := &models.ProductSKU{
		ProductID:         productID,
		SKUCode:           code,
		PriceAmount:       models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		ManualStockTotal:  total,
		ManualStockLocked: locked,
		ManualStockSold:   sold,
		IsActive:          true,
		SortOrder:         0,
	}
	if err := db.Create(sku).Error; err != nil {
		t.Fatalf("create sku failed: %v", err)
	}
	if !isActive {
		sku.IsActive = false
		if err := db.Save(sku).Error; err != nil {
			t.Fatalf("update inactive sku failed: %v", err)
		}
	}
	return sku
}

func TestListExcludesProviderCatalogExcludedCategoryWhenRequested(t *testing.T) {
	repo, db := setupProductRepositoryTest(t)
	excludedCategory := &models.Category{Slug: "provider-catalog-excluded", NameJSON: models.JSON{"zh-CN": "已排除目录"}, IsActive: false}
	if err := db.Create(excludedCategory).Error; err != nil {
		t.Fatalf("create excluded category: %v", err)
	}

	visible := createManualProduct(t, repo, "visible-product", 1, 0, 0)
	excluded := createManualProduct(t, repo, "excluded-product", 1, 0, 0)
	excluded.CategoryID = excludedCategory.ID
	if err := repo.Update(excluded); err != nil {
		t.Fatalf("move excluded product: %v", err)
	}

	rows, total, err := repo.List(ProductListFilter{Page: 1, PageSize: 20, ExcludeProviderCatalogExcluded: true})
	if err != nil {
		t.Fatalf("list products: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != visible.ID {
		t.Fatalf("rows=%+v total=%d, want only visible product %d", rows, total, visible.ID)
	}
}

func TestListFiltersProductsByPublicCatalog(t *testing.T) {
	repo, db := setupProductRepositoryTest(t)
	account := createManualProduct(t, repo, "account-catalog-product", 1, 0, 0)
	service := createManualProduct(t, repo, "service-catalog-product", 1, 0, 0)

	for _, mapping := range []models.ProductMapping{
		{LocalProductID: account.ID, ConnectionID: 1, Provider: "tgx", IsActive: true},
		{LocalProductID: service.ID, ConnectionID: 1, Provider: "fansgurus", IsActive: true},
	} {
		if err := db.Create(&mapping).Error; err != nil {
			t.Fatalf("create product mapping: %v", err)
		}
	}

	accounts, accountTotal, err := repo.List(ProductListFilter{Catalog: "accounts", OnlyActive: true, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list account catalog: %v", err)
	}
	if accountTotal != 1 || len(accounts) != 1 || accounts[0].ID != account.ID {
		t.Fatalf("accounts=%+v total=%d, want only product %d", accounts, accountTotal, account.ID)
	}

	services, serviceTotal, err := repo.List(ProductListFilter{Catalog: "services", OnlyActive: true, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list service catalog: %v", err)
	}
	if serviceTotal != 1 || len(services) != 1 || services[0].ID != service.ID {
		t.Fatalf("services=%+v total=%d, want only product %d", services, serviceTotal, service.ID)
	}
}

func createAutoProduct(t *testing.T, repo *GormProductRepository, slug string) *models.Product {
	t.Helper()
	product := &models.Product{
		CategoryID:      1,
		Slug:            slug,
		TitleJSON:       models.JSON{"zh-CN": "自动发货商品"},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeAuto,
		IsActive:        true,
	}
	if err := repo.Create(product); err != nil {
		t.Fatalf("create auto product failed: %v", err)
	}
	return product
}

func createAvailableCardSecrets(t *testing.T, db *gorm.DB, productID uint, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		secret := &models.CardSecret{
			ProductID: productID,
			SKUID:     0,
			Secret:    fmt.Sprintf("AUTO-SECRET-%d-%d", productID, i),
			Status:    models.CardSecretStatusAvailable,
		}
		if err := db.Create(secret).Error; err != nil {
			t.Fatalf("create card secret failed: %v", err)
		}
	}
}

func TestManualStockReserveReleaseConsumeLifecycle(t *testing.T) {
	repo, db := setupProductRepositoryTest(t)
	product := createManualProduct(t, repo, "manual-stock-lifecycle", 10, 0, 0)

	affected, err := repo.ReserveManualStock(product.ID, 3)
	if err != nil {
		t.Fatalf("reserve stock failed: %v", err)
	}
	if affected != 1 {
		t.Fatalf("reserve affected want 1 got %d", affected)
	}

	affected, err = repo.ConsumeManualStock(product.ID, 2)
	if err != nil {
		t.Fatalf("consume stock failed: %v", err)
	}
	if affected != 1 {
		t.Fatalf("consume affected want 1 got %d", affected)
	}

	affected, err = repo.ReleaseManualStock(product.ID, 1)
	if err != nil {
		t.Fatalf("release stock failed: %v", err)
	}
	if affected != 1 {
		t.Fatalf("release affected want 1 got %d", affected)
	}

	var got models.Product
	if err := db.First(&got, product.ID).Error; err != nil {
		t.Fatalf("reload product failed: %v", err)
	}
	if got.ManualStockTotal != 8 {
		t.Fatalf("total want 8 got %d", got.ManualStockTotal)
	}
	if got.ManualStockLocked != 0 {
		t.Fatalf("locked want 0 got %d", got.ManualStockLocked)
	}
	if got.ManualStockSold != 2 {
		t.Fatalf("sold want 2 got %d", got.ManualStockSold)
	}

	affected, err = repo.ReserveManualStock(product.ID, 9)
	if err != nil {
		t.Fatalf("reserve over available failed: %v", err)
	}
	if affected != 0 {
		t.Fatalf("reserve over available affected want 0 got %d", affected)
	}

	affected, err = repo.ReserveManualStock(product.ID, 8)
	if err != nil {
		t.Fatalf("reserve exact available failed: %v", err)
	}
	if affected != 1 {
		t.Fatalf("reserve exact available affected want 1 got %d", affected)
	}
}

func TestManualStockConsumeWithLegacyUnreservedOrder(t *testing.T) {
	repo, db := setupProductRepositoryTest(t)
	product := createManualProduct(t, repo, "manual-stock-legacy", 5, 0, 1)

	affected, err := repo.ConsumeManualStock(product.ID, 2)
	if err != nil {
		t.Fatalf("consume stock failed: %v", err)
	}
	if affected != 1 {
		t.Fatalf("consume affected want 1 got %d", affected)
	}

	var got models.Product
	if err := db.First(&got, product.ID).Error; err != nil {
		t.Fatalf("reload product failed: %v", err)
	}
	if got.ManualStockTotal != 3 {
		t.Fatalf("total want 3 got %d", got.ManualStockTotal)
	}
	if got.ManualStockLocked != 0 {
		t.Fatalf("locked want 0 got %d", got.ManualStockLocked)
	}
	if got.ManualStockSold != 3 {
		t.Fatalf("sold want 3 got %d", got.ManualStockSold)
	}
}

func TestManualStockUnlimitedDoesNotReserve(t *testing.T) {
	repo, _ := setupProductRepositoryTest(t)
	product := createManualProduct(t, repo, "manual-stock-unlimited", constants.ManualStockUnlimited, 0, 0)

	affected, err := repo.ReserveManualStock(product.ID, 1)
	if err != nil {
		t.Fatalf("reserve unlimited stock failed: %v", err)
	}
	if affected != 0 {
		t.Fatalf("reserve unlimited affected want 0 got %d", affected)
	}

	affected, err = repo.ConsumeManualStock(product.ID, 1)
	if err != nil {
		t.Fatalf("consume unlimited stock failed: %v", err)
	}
	if affected != 0 {
		t.Fatalf("consume unlimited affected want 0 got %d", affected)
	}
}

func TestListManualStockStatusUsesActiveSKURemaining(t *testing.T) {
	repo, db := setupProductRepositoryTest(t)

	lowBySKU := createManualProduct(t, repo, "low-by-sku", 1, 0, 0)
	createManualSKU(t, db, lowBySKU.ID, "LOW", 0, 0, 1, true)

	normalBySKU := createManualProduct(t, repo, "normal-by-sku", 0, 0, 0)
	createManualSKU(t, db, normalBySKU.ID, "NORMAL", 2, 0, 0, true)

	unlimitedBySKU := createManualProduct(t, repo, "unlimited-by-sku", 0, 0, 0)
	createManualSKU(t, db, unlimitedBySKU.ID, "UNLIMITED", constants.ManualStockUnlimited, 0, 0, true)

	lowByFallback := createManualProduct(t, repo, "low-by-fallback", 0, 0, 0)
	createManualSKU(t, db, lowByFallback.ID, "INACTIVE-LOW", 5, 0, 0, false)

	normalByFallback := createManualProduct(t, repo, "normal-by-fallback", 3, 0, 0)
	createManualSKU(t, db, normalByFallback.ID, "INACTIVE-NORMAL", 0, 0, 0, false)

	unlimitedByFallback := createManualProduct(t, repo, "unlimited-by-fallback", constants.ManualStockUnlimited, 0, 0)
	createManualSKU(t, db, unlimitedByFallback.ID, "INACTIVE-UNLIMITED", 0, 0, 0, false)

	checkSlugs := func(status string, expected map[string]bool) {
		products, _, err := repo.List(ProductListFilter{
			Page:              1,
			PageSize:          100,
			StockStatus:       status,
			LowStockThreshold: 5,
		})
		if err != nil {
			t.Fatalf("list products by status=%s failed: %v", status, err)
		}
		got := make(map[string]bool, len(products))
		for _, item := range products {
			got[item.Slug] = true
		}
		for slug, want := range expected {
			if got[slug] != want {
				t.Fatalf("status=%s expect slug=%s present=%v got=%v", status, slug, want, got[slug])
			}
		}
	}

	checkSlugs("low", map[string]bool{
		lowBySKU.Slug:            true,
		lowByFallback.Slug:       true,
		normalBySKU.Slug:         false,
		normalByFallback.Slug:    false,
		unlimitedBySKU.Slug:      false,
		unlimitedByFallback.Slug: false,
	})

	checkSlugs("normal", map[string]bool{
		normalBySKU.Slug:         true,
		normalByFallback.Slug:    true,
		lowBySKU.Slug:            false,
		lowByFallback.Slug:       false,
		unlimitedBySKU.Slug:      false,
		unlimitedByFallback.Slug: false,
	})

	checkSlugs("unlimited", map[string]bool{
		unlimitedBySKU.Slug:      true,
		unlimitedByFallback.Slug: true,
		normalBySKU.Slug:         false,
		normalByFallback.Slug:    false,
		lowBySKU.Slug:            false,
		lowByFallback.Slug:       false,
	})
}

func TestListStockStatusAutoUsesLowStockThreshold(t *testing.T) {
	repo, db := setupProductRepositoryTest(t)

	createAutoProduct(t, repo, "auto-low-0")
	low3 := createAutoProduct(t, repo, "auto-low-3")
	normal6 := createAutoProduct(t, repo, "auto-normal-6")

	createAvailableCardSecrets(t, db, low3.ID, 3)
	createAvailableCardSecrets(t, db, normal6.ID, 6)

	checkSlugs := func(status string, expected map[string]bool) {
		products, _, err := repo.List(ProductListFilter{
			Page:              1,
			PageSize:          100,
			StockStatus:       status,
			LowStockThreshold: 5,
		})
		if err != nil {
			t.Fatalf("list products by status=%s failed: %v", status, err)
		}

		got := make(map[string]bool, len(products))
		for _, item := range products {
			got[item.Slug] = true
		}

		for slug, want := range expected {
			if got[slug] != want {
				t.Fatalf("status=%s expect slug=%s present=%v got=%v", status, slug, want, got[slug])
			}
		}
	}

	checkSlugs("low", map[string]bool{
		"auto-low-0":    true,
		"auto-low-3":    true,
		"auto-normal-6": false,
	})

	checkSlugs("normal", map[string]bool{
		"auto-low-0":    false,
		"auto-low-3":    false,
		"auto-normal-6": true,
	})
}

func TestProductRepositoryListSortOrderDescending(t *testing.T) {
	repo, _ := setupProductRepositoryTest(t)

	high := &models.Product{
		CategoryID:  1,
		Slug:        "high-sort-product",
		TitleJSON:   models.JSON{"zh-CN": "high"},
		PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		IsActive:    true,
		SortOrder:   100,
	}
	low := &models.Product{
		CategoryID:  1,
		Slug:        "low-sort-product",
		TitleJSON:   models.JSON{"zh-CN": "low"},
		PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		IsActive:    true,
		SortOrder:   1,
	}
	if err := repo.Create(high); err != nil {
		t.Fatalf("create high sort product failed: %v", err)
	}
	if err := repo.Create(low); err != nil {
		t.Fatalf("create low sort product failed: %v", err)
	}

	rows, total, err := repo.List(ProductListFilter{
		Page:       1,
		PageSize:   20,
		OnlyActive: true,
	})
	if err != nil {
		t.Fatalf("list products failed: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 products, got %d", len(rows))
	}
	if rows[0].Slug != "high-sort-product" || rows[1].Slug != "low-sort-product" {
		t.Fatalf("expected high sort_order first, got %s then %s", rows[0].Slug, rows[1].Slug)
	}
}

func TestProductRepositoryListSupportsNumericIDSearch(t *testing.T) {
	repo, _ := setupProductRepositoryTest(t)

	target := &models.Product{
		CategoryID:      1,
		Slug:            "numeric-id-search-target",
		TitleJSON:       models.JSON{"zh-CN": "数字搜索目标"},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeAuto,
		IsActive:        true,
	}
	if err := repo.Create(target); err != nil {
		t.Fatalf("create target product failed: %v", err)
	}

	other := &models.Product{
		CategoryID:      1,
		Slug:            "numeric-id-search-other",
		TitleJSON:       models.JSON{"zh-CN": "另一个商品"},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeAuto,
		IsActive:        true,
	}
	if err := repo.Create(other); err != nil {
		t.Fatalf("create other product failed: %v", err)
	}

	rows, total, err := repo.List(ProductListFilter{
		Page:       1,
		PageSize:   20,
		Search:     strconv.FormatUint(uint64(target.ID), 10),
		OnlyActive: true,
	})
	if err != nil {
		t.Fatalf("search by numeric product id failed: %v", err)
	}
	if total == 0 || len(rows) == 0 {
		t.Fatalf("search by numeric product id should return target product")
	}
	if rows[0].ID != target.ID {
		found := false
		for _, row := range rows {
			if row.ID == target.ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("search result missing target product id=%d rows=%+v", target.ID, rows)
		}
	}
}

func TestProductRepositoryListFiltersWholesalePrices(t *testing.T) {
	repo, _ := setupProductRepositoryTest(t)

	withWholesale := &models.Product{
		CategoryID:      1,
		Slug:            "with-wholesale",
		TitleJSON:       models.JSON{"zh-CN": "有批发价"},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		WholesalePrices: models.WholesalePriceTiers{{MinQuantity: 5, UnitPrice: models.NewMoneyFromDecimal(decimal.NewFromInt(80))}},
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeAuto,
		IsActive:        true,
	}
	if err := repo.Create(withWholesale); err != nil {
		t.Fatalf("create product with wholesale failed: %v", err)
	}

	withoutWholesale := &models.Product{
		CategoryID:      1,
		Slug:            "without-wholesale",
		TitleJSON:       models.JSON{"zh-CN": "无批发价"},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeAuto,
		IsActive:        true,
	}
	if err := repo.Create(withoutWholesale); err != nil {
		t.Fatalf("create product without wholesale failed: %v", err)
	}

	enabled := true
	rows, _, err := repo.List(ProductListFilter{Page: 1, PageSize: 20, HasWholesalePrices: &enabled})
	if err != nil {
		t.Fatalf("list products with wholesale failed: %v", err)
	}
	if len(rows) != 1 || rows[0].Slug != withWholesale.Slug {
		t.Fatalf("expected only product with wholesale, got %+v", rows)
	}

	disabled := false
	rows, _, err = repo.List(ProductListFilter{Page: 1, PageSize: 20, HasWholesalePrices: &disabled})
	if err != nil {
		t.Fatalf("list products without wholesale failed: %v", err)
	}
	got := make(map[string]bool, len(rows))
	for _, row := range rows {
		got[row.Slug] = true
	}
	if got[withWholesale.Slug] {
		t.Fatalf("product with wholesale should not be returned: %+v", rows)
	}
	if !got[withoutWholesale.Slug] {
		t.Fatalf("product without wholesale missing: %+v", rows)
	}
}
