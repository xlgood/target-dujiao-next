package repository

import (
	"github.com/dujiao-next/internal/models"

	"gorm.io/gorm"
)

type ProviderCatalogSyncRunRepository interface {
	Create(run *models.ProviderCatalogSyncRun) error
	GetByID(id uint) (*models.ProviderCatalogSyncRun, error)
	List(filter ProviderCatalogSyncRunListFilter) ([]models.ProviderCatalogSyncRun, int64, error)
}

func (r *GormProviderCatalogSyncRunRepository) GetByID(id uint) (*models.ProviderCatalogSyncRun, error) {
	var run models.ProviderCatalogSyncRun
	if err := r.db.First(&run, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

type ProviderCatalogSyncRunListFilter struct {
	Status string
	Pagination
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

func (r *GormProviderCatalogSyncRunRepository) List(filter ProviderCatalogSyncRunListFilter) ([]models.ProviderCatalogSyncRun, int64, error) {
	var runs []models.ProviderCatalogSyncRun
	q := r.db.Model(&models.ProviderCatalogSyncRun{})
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := applyPagination(q.Order("started_at DESC"), filter.Page, filter.PageSize).Find(&runs).Error; err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}
