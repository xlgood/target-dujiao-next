package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupCategoryRepositoryTest(t *testing.T) *GormCategoryRepository {
	t.Helper()
	dsn := fmt.Sprintf("file:category_repository_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&models.Category{}, &models.Product{}, &models.SiteConnection{}, &models.ProductMapping{}); err != nil {
		t.Fatalf("migrate category failed: %v", err)
	}
	return NewCategoryRepository(db)
}

func TestCategoryRepositoryListActiveByCatalog(t *testing.T) {
	repo := setupCategoryRepositoryTest(t)
	db := repo.db

	accounts := &models.Category{Slug: "accounts", NameJSON: models.JSON{"zh-CN": "账号"}, IsActive: true}
	services := &models.Category{Slug: "services", NameJSON: models.JSON{"zh-CN": "服务"}, IsActive: true}
	if err := db.Create(accounts).Error; err != nil {
		t.Fatalf("create accounts category: %v", err)
	}
	if err := db.Create(services).Error; err != nil {
		t.Fatalf("create services category: %v", err)
	}

	accountProduct := &models.Product{CategoryID: accounts.ID, Slug: "account-product", TitleJSON: models.JSON{"zh-CN": "账号商品"}, IsActive: true}
	serviceProduct := &models.Product{CategoryID: services.ID, Slug: "service-product", TitleJSON: models.JSON{"zh-CN": "服务商品"}, IsActive: true}
	for _, product := range []*models.Product{accountProduct, serviceProduct} {
		if err := db.Create(product).Error; err != nil {
			t.Fatalf("create product: %v", err)
		}
	}
	for _, mapping := range []models.ProductMapping{
		{LocalProductID: accountProduct.ID, ConnectionID: 1, Provider: "tgx", IsActive: true},
		{LocalProductID: serviceProduct.ID, ConnectionID: 1, Provider: "fansgurus", IsActive: true},
	} {
		if err := db.Create(&mapping).Error; err != nil {
			t.Fatalf("create mapping: %v", err)
		}
	}

	accountCategories, err := repo.ListActiveByCatalog("accounts")
	if err != nil {
		t.Fatalf("list account categories: %v", err)
	}
	if len(accountCategories) != 1 || accountCategories[0].ID != accounts.ID {
		t.Fatalf("account categories=%+v, want only %d", accountCategories, accounts.ID)
	}

	serviceCategories, err := repo.ListActiveByCatalog("services")
	if err != nil {
		t.Fatalf("list service categories: %v", err)
	}
	if len(serviceCategories) != 1 || serviceCategories[0].ID != services.ID {
		t.Fatalf("service categories=%+v, want only %d", serviceCategories, services.ID)
	}
}

func TestCategoryRepositoryListSortOrderDescending(t *testing.T) {
	repo := setupCategoryRepositoryTest(t)

	high := &models.Category{
		Slug:      "high",
		NameJSON:  models.JSON{"zh-CN": "high"},
		SortOrder: 100,
	}
	low := &models.Category{
		Slug:      "low",
		NameJSON:  models.JSON{"zh-CN": "low"},
		SortOrder: 1,
	}
	if err := repo.Create(high); err != nil {
		t.Fatalf("create high sort category failed: %v", err)
	}
	if err := repo.Create(low); err != nil {
		t.Fatalf("create low sort category failed: %v", err)
	}

	rows, err := repo.List()
	if err != nil {
		t.Fatalf("list categories failed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 categories, got %d", len(rows))
	}
	if rows[0].Slug != "high" || rows[1].Slug != "low" {
		t.Fatalf("expected high sort_order first, got %s then %s", rows[0].Slug, rows[1].Slug)
	}
}
