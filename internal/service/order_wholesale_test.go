package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type wholesaleOrderFixture struct {
	db      *gorm.DB
	svc     *OrderService
	product models.Product
	sku     models.ProductSKU
	user    models.User
}

func setupWholesaleOrderFixture(t *testing.T, name string, wholesalePrices models.WholesalePriceTiers, promotionPercent *decimal.Decimal, memberRate *decimal.Decimal) wholesaleOrderFixture {
	t.Helper()

	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", name, time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Category{},
		&models.Product{},
		&models.ProductSKU{},
		&models.Promotion{},
		&models.User{},
		&models.MemberLevel{},
		&models.MemberLevelPrice{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	now := time.Now()
	category := models.Category{
		Slug:      name + "-category",
		NameJSON:  models.JSON{"zh-CN": "测试分类"},
		CreatedAt: now,
	}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category failed: %v", err)
	}

	var user models.User
	if memberRate != nil {
		level := models.MemberLevel{
			NameJSON:     models.JSON{"zh-CN": "批发会员"},
			Slug:         name + "-level",
			DiscountRate: models.NewMoneyFromDecimal(*memberRate),
			IsActive:     true,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := db.Create(&level).Error; err != nil {
			t.Fatalf("create member level failed: %v", err)
		}
		user = models.User{
			Email:         name + "@example.com",
			PasswordHash:  "hash",
			Status:        "active",
			MemberLevelID: level.ID,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create user failed: %v", err)
		}
	}

	product := models.Product{
		CategoryID:      category.ID,
		Slug:            name + "-product",
		TitleJSON:       models.JSON{"zh-CN": "批发测试商品"},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		WholesalePrices: wholesalePrices,
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeAuto,
		IsActive:        true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	sku := models.ProductSKU{
		ProductID:   product.ID,
		SKUCode:     models.DefaultSKUCode,
		PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		IsActive:    true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.Create(&sku).Error; err != nil {
		t.Fatalf("create sku failed: %v", err)
	}

	if promotionPercent != nil {
		promotion := models.Promotion{
			Name:       name + "-promotion",
			ScopeType:  constants.ScopeTypeProduct,
			ScopeRefID: product.ID,
			Type:       constants.PromotionTypePercent,
			Value:      models.NewMoneyFromDecimal(*promotionPercent),
			MinAmount:  models.NewMoneyFromDecimal(decimal.Zero),
			IsActive:   true,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := db.Create(&promotion).Error; err != nil {
			t.Fatalf("create promotion failed: %v", err)
		}
	}

	userRepo := repository.NewUserRepository(db)
	levelRepo := repository.NewMemberLevelRepository(db)
	priceRepo := repository.NewMemberLevelPriceRepository(db)
	svc := NewOrderService(OrderServiceOptions{
		UserRepo:           userRepo,
		ProductRepo:        repository.NewProductRepository(db),
		ProductSKURepo:     repository.NewProductSKURepository(db),
		PromotionRepo:      repository.NewPromotionRepository(db),
		MemberLevelService: NewMemberLevelService(levelRepo, priceRepo, userRepo),
		ExpireMinutes:      15,
	})

	return wholesaleOrderFixture{db: db, svc: svc, product: product, sku: sku, user: user}
}

func TestBuildOrderResultPrefersWholesaleOverPromotion(t *testing.T) {
	wholesalePrices := models.WholesalePriceTiers{
		{MinQuantity: 5, UnitPrice: models.NewMoneyFromDecimal(decimal.NewFromInt(80))},
	}
	promotionPercent := decimal.NewFromInt(10) // 活动价 90，批发价 80 更便宜。
	fixture := setupWholesaleOrderFixture(t, "wholesale_over_promotion", wholesalePrices, &promotionPercent, nil)

	result, err := fixture.svc.buildOrderResult(orderCreateParams{
		Items: []CreateOrderItem{{ProductID: fixture.product.ID, SKUID: fixture.sku.ID, Quantity: 5}},
	})
	if err != nil {
		t.Fatalf("buildOrderResult failed: %v", err)
	}

	if !result.OriginalAmount.Equal(decimal.NewFromInt(500)) {
		t.Fatalf("expected original amount 500, got %s", result.OriginalAmount.String())
	}
	if !result.WholesaleDiscountAmount.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("expected wholesale discount 100, got %s", result.WholesaleDiscountAmount.String())
	}
	if !result.PromotionDiscountAmount.IsZero() {
		t.Fatalf("expected promotion discount 0, got %s", result.PromotionDiscountAmount.String())
	}
	if !result.TotalAmount.Equal(decimal.NewFromInt(400)) {
		t.Fatalf("expected total 400, got %s", result.TotalAmount.String())
	}
	item := result.Plans[0].Item
	if item.UnitPrice.String() != "80.00" || item.WholesaleDiscount.String() != "100.00" || item.PromotionDiscount.String() != "0.00" {
		t.Fatalf("unexpected item price result: unit=%s wholesale=%s promotion=%s", item.UnitPrice.String(), item.WholesaleDiscount.String(), item.PromotionDiscount.String())
	}
}

func TestBuildOrderResultPrefersPromotionOverWholesale(t *testing.T) {
	wholesalePrices := models.WholesalePriceTiers{
		{MinQuantity: 5, UnitPrice: models.NewMoneyFromDecimal(decimal.NewFromInt(80))},
	}
	promotionPercent := decimal.NewFromInt(30) // 活动价 70，批发价 80 不生效。
	fixture := setupWholesaleOrderFixture(t, "promotion_over_wholesale", wholesalePrices, &promotionPercent, nil)

	result, err := fixture.svc.buildOrderResult(orderCreateParams{
		Items: []CreateOrderItem{{ProductID: fixture.product.ID, SKUID: fixture.sku.ID, Quantity: 5}},
	})
	if err != nil {
		t.Fatalf("buildOrderResult failed: %v", err)
	}

	if !result.PromotionDiscountAmount.Equal(decimal.NewFromInt(150)) {
		t.Fatalf("expected promotion discount 150, got %s", result.PromotionDiscountAmount.String())
	}
	if !result.WholesaleDiscountAmount.IsZero() {
		t.Fatalf("expected wholesale discount 0, got %s", result.WholesaleDiscountAmount.String())
	}
	if !result.TotalAmount.Equal(decimal.NewFromInt(350)) {
		t.Fatalf("expected total 350, got %s", result.TotalAmount.String())
	}
	item := result.Plans[0].Item
	if item.UnitPrice.String() != "70.00" || item.PromotionDiscount.String() != "150.00" || item.WholesaleDiscount.String() != "0.00" {
		t.Fatalf("unexpected item price result: unit=%s wholesale=%s promotion=%s", item.UnitPrice.String(), item.WholesaleDiscount.String(), item.PromotionDiscount.String())
	}
}

func TestBuildOrderResultAppliesMemberDiscountAfterWholesale(t *testing.T) {
	wholesalePrices := models.WholesalePriceTiers{
		{MinQuantity: 5, UnitPrice: models.NewMoneyFromDecimal(decimal.NewFromInt(80))},
	}
	memberRate := decimal.NewFromInt(80) // 批发价 80 后再打 8 折，最终 64。
	fixture := setupWholesaleOrderFixture(t, "member_after_wholesale", wholesalePrices, nil, &memberRate)

	result, err := fixture.svc.buildOrderResult(orderCreateParams{
		UserID: fixture.user.ID,
		Items:  []CreateOrderItem{{ProductID: fixture.product.ID, SKUID: fixture.sku.ID, Quantity: 5}},
	})
	if err != nil {
		t.Fatalf("buildOrderResult failed: %v", err)
	}

	if !result.WholesaleDiscountAmount.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("expected wholesale discount 100, got %s", result.WholesaleDiscountAmount.String())
	}
	if !result.MemberDiscountAmount.Equal(decimal.NewFromInt(80)) {
		t.Fatalf("expected member discount 80, got %s", result.MemberDiscountAmount.String())
	}
	if !result.TotalAmount.Equal(decimal.NewFromInt(320)) {
		t.Fatalf("expected total 320, got %s", result.TotalAmount.String())
	}
	item := result.Plans[0].Item
	if item.UnitPrice.String() != "64.00" || item.WholesaleDiscount.String() != "100.00" || item.MemberDiscount.String() != "80.00" {
		t.Fatalf("unexpected item price result: unit=%s wholesale=%s member=%s", item.UnitPrice.String(), item.WholesaleDiscount.String(), item.MemberDiscount.String())
	}
}

func TestBuildOrderResultMatchesWholesaleByProductQuantityAcrossSKUs(t *testing.T) {
	wholesalePrices := models.WholesalePriceTiers{
		{MinQuantity: 10, UnitPrice: models.NewMoneyFromDecimal(decimal.NewFromInt(80))},
	}
	fixture := setupWholesaleOrderFixture(t, "wholesale_across_skus", wholesalePrices, nil, nil)

	skuB := models.ProductSKU{
		ProductID:   fixture.product.ID,
		SKUCode:     "SKU-B",
		PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := fixture.db.Create(&skuB).Error; err != nil {
		t.Fatalf("create second sku failed: %v", err)
	}

	result, err := fixture.svc.buildOrderResult(orderCreateParams{
		Items: []CreateOrderItem{
			{ProductID: fixture.product.ID, SKUID: fixture.sku.ID, Quantity: 6},
			{ProductID: fixture.product.ID, SKUID: skuB.ID, Quantity: 6},
		},
	})
	if err != nil {
		t.Fatalf("buildOrderResult failed: %v", err)
	}

	if !result.OriginalAmount.Equal(decimal.NewFromInt(1200)) {
		t.Fatalf("expected original amount 1200, got %s", result.OriginalAmount.String())
	}
	if !result.WholesaleDiscountAmount.Equal(decimal.NewFromInt(240)) {
		t.Fatalf("expected wholesale discount 240, got %s", result.WholesaleDiscountAmount.String())
	}
	if !result.TotalAmount.Equal(decimal.NewFromInt(960)) {
		t.Fatalf("expected total 960, got %s", result.TotalAmount.String())
	}
	if len(result.Plans) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(result.Plans))
	}
	for _, plan := range result.Plans {
		if plan.Item.UnitPrice.String() != "80.00" || plan.Item.WholesaleDiscount.String() != "120.00" {
			t.Fatalf("unexpected item price result: sku=%d unit=%s wholesale=%s", plan.Item.SKUID, plan.Item.UnitPrice.String(), plan.Item.WholesaleDiscount.String())
		}
	}
}
