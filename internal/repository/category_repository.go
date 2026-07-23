package repository

import (
	"errors"

	"github.com/dujiao-next/internal/models"

	"gorm.io/gorm"
)

// CategoryRepository 分类数据访问接口
type CategoryRepository interface {
	List() ([]models.Category, error)
	ListActive() ([]models.Category, error)
	ListActiveByCatalog(catalog string) ([]models.Category, error)
	GetByID(id string) (*models.Category, error)
	Create(category *models.Category) error
	Update(category *models.Category) error
	UpdateActive(id string, active bool) error
	Delete(id string) error
	CountBySlug(slug string, excludeID *string) (int64, error)
	CountChildren(categoryID string) (int64, error)
	CountProducts(categoryID string) (int64, error)
	CountActiveProducts(categoryID string) (int64, error)
	GetBySlug(slug string) (*models.Category, error)
	GetBySlugUnscoped(slug string) (*models.Category, error)
	Restore(category *models.Category) error
}

// GormCategoryRepository GORM 实现
type GormCategoryRepository struct {
	db *gorm.DB
}

// NewCategoryRepository 创建分类仓库
func NewCategoryRepository(db *gorm.DB) *GormCategoryRepository {
	return &GormCategoryRepository{db: db}
}

// List 分类列表
func (r *GormCategoryRepository) List() ([]models.Category, error) {
	var categories []models.Category
	if err := r.db.Order("sort_order DESC, id ASC").Find(&categories).Error; err != nil {
		return nil, err
	}
	return categories, nil
}

// ListActive 启用的分类列表
func (r *GormCategoryRepository) ListActive() ([]models.Category, error) {
	var categories []models.Category
	if err := r.db.Where("is_active = ?", true).Order("sort_order DESC, id ASC").Find(&categories).Error; err != nil {
		return nil, err
	}
	return categories, nil
}

// ListActiveByCatalog limits the visible taxonomy to categories that have a
// published item in the requested customer-facing catalog lane.
func (r *GormCategoryRepository) ListActiveByCatalog(catalog string) ([]models.Category, error) {
	provider := catalogProvider(catalog)
	if provider == "" {
		return r.ListActive()
	}
	var categories []models.Category
	query := r.db.Where("categories.is_active = ?", true).
		Where(
			"EXISTS (SELECT 1 FROM products p JOIN product_mappings pm ON pm.local_product_id = p.id AND pm.deleted_at IS NULL AND pm.is_active = ? AND pm.provider = ? WHERE p.category_id = categories.id AND p.deleted_at IS NULL AND p.is_active = ?)",
			true,
			provider,
			true,
		).
		Order("sort_order DESC, id ASC")
	if err := query.Find(&categories).Error; err != nil {
		return nil, err
	}
	return categories, nil
}

// GetByID 根据 ID 获取分类
func (r *GormCategoryRepository) GetByID(id string) (*models.Category, error) {
	var category models.Category
	if err := r.db.First(&category, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &category, nil
}

// Create 创建分类
func (r *GormCategoryRepository) Create(category *models.Category) error {
	return r.db.Create(category).Error
}

// Update 更新分类
func (r *GormCategoryRepository) Update(category *models.Category) error {
	return r.db.Save(category).Error
}

// UpdateActive 更新启用状态
func (r *GormCategoryRepository) UpdateActive(id string, active bool) error {
	return r.db.Model(&models.Category{}).Where("id = ?", id).Update("is_active", active).Error
}

// Delete 删除分类
func (r *GormCategoryRepository) Delete(id string) error {
	return r.db.Delete(&models.Category{}, id).Error
}

// CountBySlug 统计 slug 数量
func (r *GormCategoryRepository) CountBySlug(slug string, excludeID *string) (int64, error) {
	var count int64
	query := r.db.Model(&models.Category{}).Where("slug = ?", slug)
	if excludeID != nil {
		query = query.Where("id != ?", *excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CountChildren 统计某分类的子分类数量
func (r *GormCategoryRepository) CountChildren(categoryID string) (int64, error) {
	var count int64
	if err := r.db.Model(&models.Category{}).Where("parent_id = ?", categoryID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CountProducts 统计某分类下商品数
func (r *GormCategoryRepository) CountProducts(categoryID string) (int64, error) {
	var count int64
	if err := r.db.Model(&models.Product{}).Where("category_id = ?", categoryID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// GetBySlug 根据 slug 获取分类
func (r *GormCategoryRepository) GetBySlug(slug string) (*models.Category, error) {
	var category models.Category
	if err := r.db.Where("slug = ?", slug).First(&category).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &category, nil
}

// GetBySlugUnscoped 根据 slug 获取分类，包含软删除记录。
func (r *GormCategoryRepository) GetBySlugUnscoped(slug string) (*models.Category, error) {
	var category models.Category
	if err := r.db.Unscoped().Where("slug = ?", slug).First(&category).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &category, nil
}

// Restore 恢复软删除分类并刷新展示信息。
func (r *GormCategoryRepository) Restore(category *models.Category) error {
	return r.db.Unscoped().Model(&models.Category{}).Where("id = ?", category.ID).Updates(map[string]interface{}{
		"parent_id":  category.ParentID,
		"name_json":  category.NameJSON,
		"icon":       category.Icon,
		"sort_order": category.SortOrder,
		"is_active":  category.IsActive,
		"deleted_at": nil,
	}).Error
}

// CountActiveProducts 统计某分类下已上架商品数
func (r *GormCategoryRepository) CountActiveProducts(categoryID string) (int64, error) {
	var count int64
	if err := r.db.Model(&models.Product{}).Where("category_id = ? AND is_active = ?", categoryID, true).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
