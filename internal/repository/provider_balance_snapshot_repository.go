package repository

import (
	"errors"

	"github.com/dujiao-next/internal/models"
	"gorm.io/gorm"
)

type ProviderBalanceSnapshotRepository interface {
	Create(snapshot *models.ProviderBalanceSnapshot) error
	Latest(connectionID uint) (*models.ProviderBalanceSnapshot, error)
	List(filter ProviderBalanceSnapshotListFilter) ([]models.ProviderBalanceSnapshot, int64, error)
}

type ProviderBalanceSnapshotListFilter struct {
	ConnectionID uint
	Status       string
	Pagination
}

func (r *GormProviderBalanceSnapshotRepository) Latest(connectionID uint) (*models.ProviderBalanceSnapshot, error) {
	var snapshot models.ProviderBalanceSnapshot
	if err := r.db.Where("connection_id = ?", connectionID).Order("checked_at DESC").First(&snapshot).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &snapshot, nil
}

type GormProviderBalanceSnapshotRepository struct{ db *gorm.DB }

func NewProviderBalanceSnapshotRepository(db *gorm.DB) *GormProviderBalanceSnapshotRepository {
	return &GormProviderBalanceSnapshotRepository{db: db}
}

func (r *GormProviderBalanceSnapshotRepository) Create(snapshot *models.ProviderBalanceSnapshot) error {
	return r.db.Create(snapshot).Error
}

func (r *GormProviderBalanceSnapshotRepository) List(filter ProviderBalanceSnapshotListFilter) ([]models.ProviderBalanceSnapshot, int64, error) {
	var snapshots []models.ProviderBalanceSnapshot
	q := r.db.Model(&models.ProviderBalanceSnapshot{})
	if filter.ConnectionID > 0 {
		q = q.Where("connection_id = ?", filter.ConnectionID)
	}
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := applyPagination(q.Order("checked_at DESC"), filter.Page, filter.PageSize).Find(&snapshots).Error; err != nil {
		return nil, 0, err
	}
	return snapshots, total, nil
}
