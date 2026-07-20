package repository

import (
	"github.com/dujiao-next/internal/models"

	"gorm.io/gorm"
)

type ProviderCatalogContentSyncRunRepository interface {
	Create(run *models.ProviderCatalogContentSyncRun) error
	GetByID(id uint) (*models.ProviderCatalogContentSyncRun, error)
	Update(run *models.ProviderCatalogContentSyncRun) error
	List(filter ProviderCatalogContentSyncRunListFilter) ([]models.ProviderCatalogContentSyncRun, int64, error)
}

type ProviderCatalogContentSyncRunListFilter struct {
	Status string
	Pagination
}

type GormProviderCatalogContentSyncRunRepository struct {
	db *gorm.DB
}

func NewProviderCatalogContentSyncRunRepository(db *gorm.DB) *GormProviderCatalogContentSyncRunRepository {
	return &GormProviderCatalogContentSyncRunRepository{db: db}
}

func (r *GormProviderCatalogContentSyncRunRepository) Create(run *models.ProviderCatalogContentSyncRun) error {
	return r.db.Create(run).Error
}

func (r *GormProviderCatalogContentSyncRunRepository) GetByID(id uint) (*models.ProviderCatalogContentSyncRun, error) {
	var run models.ProviderCatalogContentSyncRun
	if err := r.db.First(&run, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

func (r *GormProviderCatalogContentSyncRunRepository) Update(run *models.ProviderCatalogContentSyncRun) error {
	return r.db.Save(run).Error
}

func (r *GormProviderCatalogContentSyncRunRepository) List(filter ProviderCatalogContentSyncRunListFilter) ([]models.ProviderCatalogContentSyncRun, int64, error) {
	var runs []models.ProviderCatalogContentSyncRun
	query := r.db.Model(&models.ProviderCatalogContentSyncRun{})
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := applyPagination(query.Order("created_at DESC"), filter.Page, filter.PageSize).Find(&runs).Error; err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}
