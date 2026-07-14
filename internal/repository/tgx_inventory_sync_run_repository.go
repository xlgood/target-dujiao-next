package repository

import (
	"github.com/dujiao-next/internal/models"
	"gorm.io/gorm"
)

type TGXInventorySyncRunRepository interface {
	Create(run *models.TGXInventorySyncRun) error
	Latest(connectionID uint) (*models.TGXInventorySyncRun, error)
}

type GormTGXInventorySyncRunRepository struct{ db *gorm.DB }

func NewTGXInventorySyncRunRepository(db *gorm.DB) *GormTGXInventorySyncRunRepository {
	return &GormTGXInventorySyncRunRepository{db: db}
}

func (r *GormTGXInventorySyncRunRepository) Create(run *models.TGXInventorySyncRun) error {
	return r.db.Create(run).Error
}

func (r *GormTGXInventorySyncRunRepository) Latest(connectionID uint) (*models.TGXInventorySyncRun, error) {
	var run models.TGXInventorySyncRun
	q := r.db.Order("started_at DESC")
	if connectionID > 0 {
		q = q.Where("connection_id = ?", connectionID)
	}
	if err := q.First(&run).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}
