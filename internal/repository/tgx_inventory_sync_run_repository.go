package repository

import (
	"time"

	"github.com/dujiao-next/internal/models"
	"gorm.io/gorm"
)

type TGXInventorySyncRunRepository interface {
	Create(run *models.TGXInventorySyncRun) error
	Update(run *models.TGXInventorySyncRun) error
	ListRunningBefore(cutoff time.Time) ([]models.TGXInventorySyncRun, error)
	Latest(connectionID uint) (*models.TGXInventorySyncRun, error)
	GetByID(id uint) (*models.TGXInventorySyncRun, error)
	List(filter TGXInventorySyncRunListFilter) ([]models.TGXInventorySyncRun, int64, error)
}

func (r *GormTGXInventorySyncRunRepository) GetByID(id uint) (*models.TGXInventorySyncRun, error) {
	var run models.TGXInventorySyncRun
	if err := r.db.First(&run, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

type TGXInventorySyncRunListFilter struct {
	ConnectionID uint
	Status       string
	Pagination
}

type GormTGXInventorySyncRunRepository struct{ db *gorm.DB }

func NewTGXInventorySyncRunRepository(db *gorm.DB) *GormTGXInventorySyncRunRepository {
	return &GormTGXInventorySyncRunRepository{db: db}
}

func (r *GormTGXInventorySyncRunRepository) Create(run *models.TGXInventorySyncRun) error {
	return r.db.Create(run).Error
}

func (r *GormTGXInventorySyncRunRepository) Update(run *models.TGXInventorySyncRun) error {
	return r.db.Save(run).Error
}

func (r *GormTGXInventorySyncRunRepository) ListRunningBefore(cutoff time.Time) ([]models.TGXInventorySyncRun, error) {
	var runs []models.TGXInventorySyncRun
	if err := r.db.Where("status = ? AND started_at < ?", "running", cutoff).Order("started_at ASC").Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
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

func (r *GormTGXInventorySyncRunRepository) List(filter TGXInventorySyncRunListFilter) ([]models.TGXInventorySyncRun, int64, error) {
	var runs []models.TGXInventorySyncRun
	q := r.db.Model(&models.TGXInventorySyncRun{})
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
	if err := applyPagination(q.Order("started_at DESC"), filter.Page, filter.PageSize).Find(&runs).Error; err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}
