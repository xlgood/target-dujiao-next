package models

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestProviderCatalogReviewCorrectionKeepsOnlyPublishedProductsApproved(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:catalog_review_correction?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Product{}, &ProductMapping{}, &Setting{}); err != nil {
		t.Fatal(err)
	}
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	active := Product{Slug: "legacy-active", TitleJSON: JSON{"zh-CN": "active"}, IsActive: true}
	inactive := Product{Slug: "legacy-inactive", TitleJSON: JSON{"zh-CN": "inactive"}, IsActive: false}
	if err := db.Create(&active).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&inactive).Error; err != nil {
		t.Fatal(err)
	}
	mappings := []ProductMapping{
		{ConnectionID: 1, LocalProductID: active.ID, Provider: "tgx", CatalogReviewStatus: CatalogReviewApproved},
		{ConnectionID: 1, LocalProductID: inactive.ID, Provider: "fansgurus", CatalogReviewStatus: CatalogReviewApproved},
	}
	for i := range mappings {
		if err := db.Create(&mappings[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureProviderCatalogReviewCorrectionMigration(); err != nil {
		t.Fatal(err)
	}
	var activeMapping, inactiveMapping ProductMapping
	if err := db.First(&activeMapping, mappings[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&inactiveMapping, mappings[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if activeMapping.CatalogReviewStatus != CatalogReviewApproved || inactiveMapping.CatalogReviewStatus != CatalogReviewPending {
		t.Fatalf("active=%s inactive=%s", activeMapping.CatalogReviewStatus, inactiveMapping.CatalogReviewStatus)
	}
}

func TestProviderCatalogContentMigrationRepairsContentWithoutChangingPublication(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:catalog_content_migration?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Product{}, &ProductMapping{}, &Setting{}); err != nil {
		t.Fatal(err)
	}
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	tgx := Product{
		Slug:            "tgx-html-detail",
		TitleJSON:       JSON{"zh-CN": "TGX"},
		DescriptionJSON: JSON{"zh-CN": "<p>Rich <strong>detail</strong></p>"},
		IsActive:        true,
	}
	fans := Product{
		Slug:      "fans-empty-detail",
		TitleJSON: JSON{"zh-CN": "FansGurus"},
		IsActive:  true,
	}
	if err := db.Create(&tgx).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fans).Error; err != nil {
		t.Fatal(err)
	}
	for _, mapping := range []ProductMapping{
		{ConnectionID: 1, LocalProductID: tgx.ID, Provider: "tgx"},
		{ConnectionID: 1, LocalProductID: fans.ID, Provider: "fansgurus"},
	} {
		if err := db.Create(&mapping).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := ensureProviderCatalogContentMigration(); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&tgx, tgx.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&fans, fans.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !tgx.IsActive || !fans.IsActive {
		t.Fatal("content migration changed product publication state")
	}
	if got := tgx.DescriptionJSON["zh-CN"]; got != "Rich detail" {
		t.Fatalf("TGX description=%q, want plain text", got)
	}
	if got := tgx.ContentJSON["zh-CN"]; got != "<p>Rich <strong>detail</strong></p>" {
		t.Fatalf("TGX content=%q, want original rich text", got)
	}
	if strings.Contains(fans.DescriptionJSON["zh-CN"].(string), "上游") || !strings.Contains(fans.ContentJSON["zh-CN"].(string), "下单说明") {
		t.Fatalf("FansGurus baseline content missing: description=%v content=%v", fans.DescriptionJSON, fans.ContentJSON)
	}
}

func TestProviderCatalogCustomerCopyMigrationRemovesInternalWordingWithoutChangingPublication(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:catalog_customer_copy_migration?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Product{}, &ProductMapping{}, &Setting{}); err != nil {
		t.Fatal(err)
	}
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	product := Product{
		Slug:            "published-customer-copy",
		TitleJSON:       JSON{"zh-CN": "service"},
		DescriptionJSON: JSON{"zh-CN": "该服务由上游供应商处理。请在结算时准确填写服务所需资料。"},
		ContentJSON:     JSON{"zh-CN": "<p>本服务由上游供应商处理。付款后请在订单页查看处理进度和结果。</p>"},
		IsActive:        true,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&ProductMapping{ConnectionID: 1, LocalProductID: product.ID, Provider: "fansgurus"}).Error; err != nil {
		t.Fatal(err)
	}

	if err := ensureProviderCatalogCustomerCopyMigration(); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&product, product.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !product.IsActive {
		t.Fatal("customer-copy migration changed product publication state")
	}
	if strings.Contains(product.DescriptionJSON["zh-CN"].(string), "上游") || strings.Contains(product.ContentJSON["zh-CN"].(string), "上游") {
		t.Fatalf("internal wording remains: description=%v content=%v", product.DescriptionJSON, product.ContentJSON)
	}
}
