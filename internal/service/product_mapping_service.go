package service

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/upstream"
)

// 文件组织约定:
//   product_mapping_service.go        — 核心:struct/ctor/setters + 基础查询/CRUD + 错误定义
//   product_mapping_import.go         — 单品导入 + 上游元数据列表 + 图片下载
//   product_mapping_sync.go           — 同步流程(单品 / 全量库存)
//   product_mapping_markup.go         — 加价重算
//   product_mapping_batch_import.go   — 按上游分类批量导入

var (
	ErrMappingNotFound         = errors.New("product mapping not found")
	ErrMappingAlreadyExists    = errors.New("product mapping already exists for this upstream product")
	ErrUpstreamProductNotFound = errors.New("upstream product not found")
	ErrMappingInactive         = errors.New("product mapping is inactive")
	// Provider catalog records are refreshed as one filtered catalog so the
	// cross-provider platform policy remains consistent.
	ErrProviderCatalogSyncRequired = errors.New("provider catalog mappings must be refreshed through catalog sync")
)

// ProductMappingService 商品映射业务服务
type ProductMappingService struct {
	mappingRepo        repository.ProductMappingRepository
	skuMappingRepo     repository.SKUMappingRepository
	productRepo        repository.ProductRepository
	productSKURepo     repository.ProductSKURepository
	categoryRepo       repository.CategoryRepository
	connService        *SiteConnectionService
	categoryService    *CategoryService
	mediaService       *MediaService
	settingService     *SettingService
	syncRunRepo        repository.ProviderCatalogSyncRunRepository
	contentSyncRunRepo repository.ProviderCatalogContentSyncRunRepository
	tgxSyncRunRepo     repository.TGXInventorySyncRunRepository
	notificationSvc    *NotificationService
	tgxAlertMu         sync.Mutex
	tgxAlertUntil      map[uint]time.Time
}

// NewProductMappingService 创建商品映射服务
func NewProductMappingService(
	mappingRepo repository.ProductMappingRepository,
	skuMappingRepo repository.SKUMappingRepository,
	productRepo repository.ProductRepository,
	productSKURepo repository.ProductSKURepository,
	categoryRepo repository.CategoryRepository,
	connService *SiteConnectionService,
) *ProductMappingService {
	return &ProductMappingService{
		mappingRepo:    mappingRepo,
		skuMappingRepo: skuMappingRepo,
		productRepo:    productRepo,
		productSKURepo: productSKURepo,
		categoryRepo:   categoryRepo,
		connService:    connService,
		tgxAlertUntil:  make(map[uint]time.Time),
	}
}

// SetCategoryService 设置分类服务（避免循环依赖）
func (s *ProductMappingService) SetCategoryService(cs *CategoryService) {
	s.categoryService = cs
}

// SetMediaService 设置素材服务（避免循环依赖）
func (s *ProductMappingService) SetMediaService(ms *MediaService) {
	s.mediaService = ms
}

// SetSettingService 注入设置服务（用于读取上游同步动态配置）
func (s *ProductMappingService) SetSettingService(ss *SettingService) {
	s.settingService = ss
}

func (s *ProductMappingService) SetProviderCatalogSyncRunRepository(repo repository.ProviderCatalogSyncRunRepository) {
	s.syncRunRepo = repo
}

func (s *ProductMappingService) SetTGXInventorySyncRunRepository(repo repository.TGXInventorySyncRunRepository) {
	s.tgxSyncRunRepo = repo
}

func (s *ProductMappingService) SetNotificationService(svc *NotificationService) {
	s.notificationSvc = svc
}

func (s *ProductMappingService) LatestTGXInventorySyncRun(connectionID uint) (*models.TGXInventorySyncRun, error) {
	if s == nil || s.tgxSyncRunRepo == nil {
		return nil, nil
	}
	s.cleanupTimedOutTGXInventorySyncRuns()
	run, err := s.tgxSyncRunRepo.Latest(connectionID)
	return run, err
}

func (s *ProductMappingService) ListTGXInventorySyncRuns(filter repository.TGXInventorySyncRunListFilter) ([]models.TGXInventorySyncRun, int64, error) {
	if s == nil || s.tgxSyncRunRepo == nil {
		return []models.TGXInventorySyncRun{}, 0, nil
	}
	s.cleanupTimedOutTGXInventorySyncRuns()
	return s.tgxSyncRunRepo.List(filter)
}

func (s *ProductMappingService) cleanupTimedOutTGXInventorySyncRuns() {
	if s == nil || s.tgxSyncRunRepo == nil {
		return
	}
	runs, err := s.tgxSyncRunRepo.ListRunningBefore(time.Now().Add(-tgxInventorySyncTimeout))
	if err != nil {
		logger.Warnw("tgx_inventory_sync_timeout_lookup_failed", "error", err)
		return
	}
	for i := range runs {
		s.markTGXInventorySyncTimedOut(&runs[i])
	}
}

func (s *ProductMappingService) GetTGXInventorySyncRun(id uint) (*models.TGXInventorySyncRun, error) {
	if s == nil || s.tgxSyncRunRepo == nil {
		return nil, nil
	}
	return s.tgxSyncRunRepo.GetByID(id)
}

func (s *ProductMappingService) ListProviderCatalogSyncRuns(filter repository.ProviderCatalogSyncRunListFilter) ([]models.ProviderCatalogSyncRun, int64, error) {
	if s == nil || s.syncRunRepo == nil {
		return []models.ProviderCatalogSyncRun{}, 0, nil
	}
	return s.syncRunRepo.List(filter)
}

func (s *ProductMappingService) GetProviderCatalogSyncRun(id uint) (*models.ProviderCatalogSyncRun, error) {
	if s == nil || s.syncRunRepo == nil {
		return nil, nil
	}
	return s.syncRunRepo.GetByID(id)
}

// GetByID 获取映射详情
func (s *ProductMappingService) GetByID(id uint) (*models.ProductMapping, error) {
	return s.mappingRepo.GetByID(id)
}

// List 列表查询映射
func (s *ProductMappingService) List(filter repository.ProductMappingListFilter) ([]models.ProductMapping, int64, error) {
	return s.mappingRepo.List(filter)
}

// SetActive 启用/禁用映射
func (s *ProductMappingService) SetActive(id uint, active bool) error {
	mapping, err := s.mappingRepo.GetByID(id)
	if err != nil {
		return err
	}
	if mapping == nil {
		return ErrMappingNotFound
	}
	mapping.IsActive = active
	if active && isProviderCatalogMapping(mapping) && mapping.CatalogReviewStatus != models.CatalogReviewApproved {
		return ErrCatalogReviewRequired
	}
	if err := s.mappingRepo.Update(mapping); err != nil {
		return err
	}
	if !active {
		return s.deactivateMappedProduct(mapping)
	}
	return s.publishMappedProduct(mapping)
}

var ErrCatalogReviewRequired = errors.New("provider catalog product must be approved before publishing")

func isProviderCatalogMapping(mapping *models.ProductMapping) bool {
	if mapping == nil {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(mapping.Provider))
	return provider == "fansgurus" || provider == "tgx"
}

// ApproveProviderCatalogMappings stages selected catalog items for storefront
// publication. A later catalog refresh may update metadata but never resets an
// operator's approval decision.
func (s *ProductMappingService) ApproveProviderCatalogMappings(ids []uint) (int, error) {
	approved := 0
	for _, id := range ids {
		mapping, err := s.mappingRepo.GetByID(id)
		if err != nil {
			return approved, err
		}
		if mapping == nil || !isProviderCatalogMapping(mapping) {
			continue
		}
		mapping.CatalogReviewStatus = models.CatalogReviewApproved
		mapping.IsActive = true
		if err := s.mappingRepo.Update(mapping); err != nil {
			return approved, err
		}
		if err := s.publishMappedProduct(mapping); err != nil {
			return approved, err
		}
		approved++
	}
	return approved, nil
}

// CorrectProviderCatalogPlatform applies the operator-selected platform to
// category, shared image, and SKU label together.
func (s *ProductMappingService) CorrectProviderCatalogPlatform(id uint, platform string) error {
	mapping, err := s.mappingRepo.GetByID(id)
	if err != nil {
		return err
	}
	if mapping == nil {
		return ErrMappingNotFound
	}
	platform = strings.TrimSpace(platform)
	if !isProviderCatalogMapping(mapping) || !isManualProviderPlatformAllowed(mapping.Provider, platform) {
		return ErrCatalogPlatformInvalid
	}
	product, err := s.productRepo.GetByID(strconv.FormatUint(uint64(mapping.LocalProductID), 10))
	if err != nil || product == nil {
		return ErrMappingNotFound
	}
	category, err := s.findOrCreateProviderCategory(platform)
	if err != nil {
		return err
	}
	product.CategoryID = category.ID
	product.Images = models.StringArray{models.ProviderCatalogImagePath(platform)}
	if err := s.productRepo.Update(product); err != nil {
		return err
	}
	mapping.Platform = platform
	mapping.PlatformLocked = true
	if err := s.mappingRepo.Update(mapping); err != nil {
		return err
	}
	skuMappings, err := s.skuMappingRepo.ListByProductMapping(mapping.ID)
	if err != nil {
		return err
	}
	for _, skuMapping := range skuMappings {
		sku, err := s.productSKURepo.GetByID(skuMapping.LocalSKUID)
		if err != nil || sku == nil {
			if err != nil {
				return err
			}
			continue
		}
		variantName := ""
		if parts := strings.SplitN(skuMapping.UpstreamSKUCode, "|", 2); len(parts) == 2 {
			variantName = strings.TrimSpace(parts[1])
		}
		sku.SpecValuesJSON = providerVariantSpecValues(mapping.Provider, platform, upstream.ProviderCatalogVariant{Name: variantName})
		if err := s.productSKURepo.Update(sku); err != nil {
			return err
		}
	}
	return nil
}

// RestoreProviderCatalogPlatformAutoDetection unlocks a manually corrected platform.
func (s *ProductMappingService) RestoreProviderCatalogPlatformAutoDetection(id uint) error {
	mapping, err := s.mappingRepo.GetByID(id)
	if err != nil {
		return err
	}
	if mapping == nil {
		return ErrMappingNotFound
	}
	if !isProviderCatalogMapping(mapping) {
		return ErrCatalogPlatformInvalid
	}
	mapping.PlatformLocked = false
	return s.mappingRepo.Update(mapping)
}

var ErrCatalogPlatformInvalid = errors.New("invalid provider catalog platform")

func isManualProviderPlatformAllowed(provider, platform string) bool {
	allowed := map[string]map[string]bool{
		"fansgurus": {"x": true, "instagram": true, "facebook": true, "tiktok": true, "youtube": true, "vk": true, "spotify": true, "discord": true, "twitch": true, "reddit": true, "linkedin": true, "github": true, "quora": true, "whatsapp": true, "line-voom": true, "threads": true},
		"tgx":       {"x": true, "facebook": true, "instagram": true, "youtube": true, "tiktok": true, "gmail": true, "threads": true, "linkedin": true, "github": true, "reddit": true, "discord": true, "outlook": true, "hotmail": true, "overseas-email": true},
	}
	return allowed[strings.ToLower(strings.TrimSpace(provider))][platform]
}

func (s *ProductMappingService) findOrCreateProviderCategory(platform string) (*models.Category, error) {
	if s.categoryRepo == nil {
		return nil, errors.New("category repository dependency missing")
	}
	slug := "platform-" + normalizeProviderSlug(platform)
	category, err := s.categoryRepo.GetBySlug(slug)
	if err != nil || category != nil {
		return category, err
	}
	category = &models.Category{Slug: slug, NameJSON: localizedText(platform), IsActive: true}
	if err := s.categoryRepo.Create(category); err != nil {
		return nil, err
	}
	return category, nil
}

func (s *ProductMappingService) deactivateMappedProduct(mapping *models.ProductMapping) error {
	if mapping == nil || mapping.LocalProductID == 0 || s.productRepo == nil {
		return nil
	}
	product, err := s.productRepo.GetByID(strconv.FormatUint(uint64(mapping.LocalProductID), 10))
	if err != nil {
		return err
	}
	if product != nil && product.FulfillmentType == constants.FulfillmentTypeUpstream && product.IsActive {
		product.IsActive = false
		return s.productRepo.Update(product)
	}
	return nil
}

func (s *ProductMappingService) publishMappedProduct(mapping *models.ProductMapping) error {
	if mapping == nil || mapping.LocalProductID == 0 || s.productRepo == nil {
		return nil
	}
	product, err := s.productRepo.GetByID(strconv.FormatUint(uint64(mapping.LocalProductID), 10))
	if err != nil {
		return err
	}
	if product != nil && product.FulfillmentType == constants.FulfillmentTypeUpstream && !product.IsActive {
		product.IsActive = true
		return s.productRepo.Update(product)
	}
	return nil
}

// Delete 删除映射（不删除本地商品）
func (s *ProductMappingService) Delete(id uint) error {
	mapping, err := s.mappingRepo.GetByID(id)
	if err != nil {
		return err
	}
	if mapping == nil {
		return ErrMappingNotFound
	}

	// 删除 SKU 映射
	if err := s.skuMappingRepo.DeleteByProductMapping(id); err != nil {
		return err
	}

	// 还原本地商品状态：取消映射标记、交付类型改回 manual、自动下架
	if mapping.LocalProductID > 0 {
		localProduct, err := s.productRepo.GetByID(strconv.FormatUint(uint64(mapping.LocalProductID), 10))
		if err == nil && localProduct != nil {
			localProduct.IsMapped = false
			if localProduct.FulfillmentType == constants.FulfillmentTypeUpstream {
				localProduct.FulfillmentType = constants.FulfillmentTypeManual
				localProduct.IsActive = false // 下架，防止用户下单后无法交付
			}
			_ = s.productRepo.Update(localProduct)
		}
	}

	return s.mappingRepo.Delete(id)
}

// GetSKUMappings 获取映射的 SKU 映射列表
func (s *ProductMappingService) GetSKUMappings(mappingID uint) ([]models.SKUMapping, error) {
	return s.skuMappingRepo.ListByProductMapping(mappingID)
}

// GetMappedUpstreamIDs 获取指定连接下所有已映射的上游商品 ID
func (s *ProductMappingService) GetMappedUpstreamIDs(connectionID uint) ([]uint, error) {
	return s.mappingRepo.ListUpstreamIDsByConnection(connectionID)
}
