package models

import (
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
