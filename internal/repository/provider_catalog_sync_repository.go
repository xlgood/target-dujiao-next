package repository

import (
	"github.com/dujiao-next/internal/models"

	"gorm.io/gorm"
)

type ProviderCatalogSyncRunRepository interface {
	Create(run *models.ProviderCatalogSyncRun) error
}

type GormProviderCatalogSyncRunRepository struct {
	db *gorm.DB
}

func NewProviderCatalogSyncRunRepository(db *gorm.DB) *GormProviderCatalogSyncRunRepository {
	return &GormProviderCatalogSyncRunRepository{db: db}
}

func (r *GormProviderCatalogSyncRunRepository) Create(run *models.ProviderCatalogSyncRun) error {
	return r.db.Create(run).Error
}
