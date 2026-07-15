package repository

import (
	"errors"

	"github.com/dujiao-next/internal/models"
	"gorm.io/gorm"
)

type ProviderBalanceSnapshotRepository interface {
	Create(snapshot *models.ProviderBalanceSnapshot) error
	Latest(connectionID uint) (*models.ProviderBalanceSnapshot, error)
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
