package repository

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/dujiao-next/internal/models"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ResellerRepository 分销商数据访问接口。
type ResellerRepository interface {
	Transaction(fn func(tx *gorm.DB) error) error
	WithTx(tx *gorm.DB) ResellerRepository
	CreateProfile(profile *models.ResellerProfile) error
	GetProfileByID(id uint) (*models.ResellerProfile, error)
	GetProfileByUserID(userID uint) (*models.ResellerProfile, error)
	UpsertDomain(domain models.ResellerDomain) (*models.ResellerDomain, error)
	FindDomainByHost(host string) (*models.ResellerDomain, error)
	FindActiveVerifiedDomain(host string) (*models.ResellerDomain, error)
	UpsertSiteConfig(config models.ResellerSiteConfig) (*models.ResellerSiteConfig, error)
	ListProductSettingsForPricing(resellerID uint, productIDs []uint, skuIDs []uint) ([]models.ResellerProductSetting, error)
	ListHiddenProductIDs(resellerID uint) ([]uint, error)
	IsActiveRelatedAccount(resellerID uint, userID uint) (bool, error)
	CreateOrderSnapshot(snapshot *models.ResellerOrderSnapshot) error
	GetOrderSnapshotByOrderID(orderID uint) (*models.ResellerOrderSnapshot, error)
	CreateLedgerEntryIfNotExists(entry *models.ResellerLedgerEntry) (bool, error)
	GetLedgerEntryByIdempotencyKey(key string) (*models.ResellerLedgerEntry, error)
	MarkDueLedgerEntriesAvailable(now time.Time) (int64, error)
	ListLedgerEntries(filter ResellerLedgerListFilter) ([]models.ResellerLedgerEntry, int64, error)
	SumLedgerAmount(resellerID uint, currency string, statuses []string) (decimal.Decimal, error)
	GetOrCreateBalanceAccountForUpdate(resellerID uint, currency string) (*models.ResellerBalanceAccount, error)
	UpdateBalanceAccount(account *models.ResellerBalanceAccount) error
	ListAvailableLedgerEntriesForUpdate(resellerID uint, currency string) ([]models.ResellerLedgerEntry, error)
	UpdateLedgerEntry(entry *models.ResellerLedgerEntry) error
	BatchUpdateLedgerEntries(ids []uint, updates map[string]interface{}) error
	BatchUpdateLedgerEntriesByWithdrawID(withdrawID uint, updates map[string]interface{}) error
	CreateWithdrawRequest(req *models.ResellerWithdrawRequest) error
	GetWithdrawRequestByID(id uint) (*models.ResellerWithdrawRequest, error)
	GetWithdrawRequestByIDForUpdate(id uint) (*models.ResellerWithdrawRequest, error)
	UpdateWithdrawRequest(req *models.ResellerWithdrawRequest) error
	ListWithdrawRequests(filter ResellerWithdrawListFilter) ([]models.ResellerWithdrawRequest, int64, error)
	ListAdminResellerLedgerEntries(filter ResellerAdminLedgerListFilter) ([]models.ResellerLedgerEntry, int64, error)
	ListAdminResellerBalanceAccounts(filter ResellerAdminBalanceAccountListFilter) ([]models.ResellerBalanceAccount, int64, error)
	ListAdminResellerWithdrawRequests(filter ResellerAdminWithdrawListFilter) ([]models.ResellerWithdrawRequest, int64, error)
}

// GormResellerRepository GORM 分销商仓储。
type GormResellerRepository struct {
	BaseRepository
}

// NewResellerRepository 创建分销商仓储。
func NewResellerRepository(db *gorm.DB) *GormResellerRepository {
	return &GormResellerRepository{BaseRepository: BaseRepository{db: db}}
}

// WithTx 绑定事务。
func (r *GormResellerRepository) WithTx(tx *gorm.DB) ResellerRepository {
	if tx == nil {
		return r
	}
	return &GormResellerRepository{BaseRepository: BaseRepository{db: tx}}
}

// CreateProfile 创建分销商资料。
func (r *GormResellerRepository) CreateProfile(profile *models.ResellerProfile) error {
	if profile == nil {
		return errors.New("reseller profile is nil")
	}
	return r.db.Create(profile).Error
}

// GetProfileByID 按 ID 获取分销商资料。
func (r *GormResellerRepository) GetProfileByID(id uint) (*models.ResellerProfile, error) {
	if id == 0 {
		return nil, nil
	}
	var profile models.ResellerProfile
	if err := r.db.Preload("User").First(&profile, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &profile, nil
}

// GetProfileByUserID 按用户 ID 获取分销商资料。
func (r *GormResellerRepository) GetProfileByUserID(userID uint) (*models.ResellerProfile, error) {
	if userID == 0 {
		return nil, nil
	}
	var profile models.ResellerProfile
	if err := r.db.Preload("User").Where("user_id = ?", userID).First(&profile).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &profile, nil
}

// UpsertDomain 创建域名，或恢复同域名的软删除记录。
func (r *GormResellerRepository) UpsertDomain(input models.ResellerDomain) (*models.ResellerDomain, error) {
	input.Domain = normalizeDomainForRepository(input.Domain)
	if input.ResellerID == 0 || input.Domain == "" {
		return nil, errors.New("invalid reseller domain")
	}
	now := time.Now()
	var existing models.ResellerDomain
	err := r.db.Unscoped().Where("domain = ?", input.Domain).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		input.CreatedAt = now
		input.UpdatedAt = now
		if err := r.db.Create(&input).Error; err != nil {
			return nil, err
		}
		return &input, nil
	}
	if !existing.DeletedAt.Valid {
		return nil, errors.New("reseller domain already exists")
	}
	existing.ResellerID = input.ResellerID
	existing.Type = input.Type
	existing.VerificationToken = input.VerificationToken
	existing.VerificationStatus = input.VerificationStatus
	existing.Status = input.Status
	existing.IsPrimary = input.IsPrimary
	existing.VerifiedAt = input.VerifiedAt
	existing.DeletedAt = gorm.DeletedAt{}
	existing.UpdatedAt = now
	if err := r.db.Unscoped().Save(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

// FindDomainByHost 按域名获取未删除域名记录。
func (r *GormResellerRepository) FindDomainByHost(host string) (*models.ResellerDomain, error) {
	domain := normalizeDomainForRepository(host)
	if domain == "" {
		return nil, nil
	}
	var row models.ResellerDomain
	err := r.db.Preload("Profile").Where("domain = ?", domain).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// FindActiveVerifiedDomain 按域名获取已验证且启用的分销域名。
func (r *GormResellerRepository) FindActiveVerifiedDomain(host string) (*models.ResellerDomain, error) {
	domain := normalizeDomainForRepository(host)
	if domain == "" {
		return nil, nil
	}
	var row models.ResellerDomain
	err := r.db.Preload("Profile").
		Where("domain = ? AND status = ? AND verification_status = ?", domain, models.ResellerDomainStatusActive, models.ResellerDomainVerificationVerified).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// UpsertSiteConfig 创建或恢复分销站点配置。
func (r *GormResellerRepository) UpsertSiteConfig(input models.ResellerSiteConfig) (*models.ResellerSiteConfig, error) {
	if input.ResellerID == 0 {
		return nil, errors.New("invalid reseller site config")
	}
	now := time.Now()
	var existing models.ResellerSiteConfig
	err := r.db.Unscoped().Where("reseller_id = ?", input.ResellerID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		input.CreatedAt = now
		input.UpdatedAt = now
		if err := r.db.Create(&input).Error; err != nil {
			return nil, err
		}
		return &input, nil
	}
	existing.SiteName = input.SiteName
	existing.Logo = input.Logo
	existing.Favicon = input.Favicon
	existing.AnnouncementJSON = input.AnnouncementJSON
	existing.SupportJSON = input.SupportJSON
	existing.SEOJSON = input.SEOJSON
	existing.FooterLinksJSON = input.FooterLinksJSON
	existing.NavConfigJSON = input.NavConfigJSON
	existing.ThemeJSON = input.ThemeJSON
	existing.DeletedAt = gorm.DeletedAt{}
	existing.UpdatedAt = now
	if err := r.db.Unscoped().Save(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

// ListProductSettingsForPricing 批量获取分销定价所需的商品级与 SKU 级配置。
func (r *GormResellerRepository) ListProductSettingsForPricing(resellerID uint, productIDs []uint, skuIDs []uint) ([]models.ResellerProductSetting, error) {
	if resellerID == 0 || len(productIDs) == 0 {
		return []models.ResellerProductSetting{}, nil
	}
	productIDs = uniqueUintSlice(productIDs)
	skuIDs = uniqueUintSlice(skuIDs)

	query := r.db.Where("reseller_id = ? AND product_id IN ?", resellerID, productIDs)
	if len(skuIDs) > 0 {
		query = query.Where("(sku_id = 0 OR sku_id IN ?)", skuIDs)
	} else {
		query = query.Where("sku_id = 0")
	}

	var rows []models.ResellerProductSetting
	if err := query.Order("product_id ASC, sku_id ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListHiddenProductIDs 返回分销前台列表应在查询层排除的商品 ID。
func (r *GormResellerRepository) ListHiddenProductIDs(resellerID uint) ([]uint, error) {
	if resellerID == 0 {
		return []uint{}, nil
	}

	hidden := map[uint]struct{}{}
	var productHidden []uint
	if err := r.db.Model(&models.ResellerProductSetting{}).
		Where("reseller_id = ? AND sku_id = 0 AND is_listed = ?", resellerID, false).
		Pluck("product_id", &productHidden).Error; err != nil {
		return nil, err
	}
	for _, id := range productHidden {
		if id != 0 {
			hidden[id] = struct{}{}
		}
	}

	var skuHidden []uint
	if err := r.db.Model(&models.ProductSKU{}).
		Select("product_skus.product_id").
		Joins(
			"JOIN reseller_product_settings rps ON rps.product_id = product_skus.product_id AND rps.sku_id = product_skus.id AND rps.reseller_id = ? AND rps.is_listed = ? AND rps.deleted_at IS NULL",
			resellerID,
			false,
		).
		Where("product_skus.is_active = ? AND product_skus.deleted_at IS NULL", true).
		Group("product_skus.product_id").
		Having("COUNT(product_skus.id) = (SELECT COUNT(1) FROM product_skus ps2 WHERE ps2.product_id = product_skus.product_id AND ps2.is_active = ? AND ps2.deleted_at IS NULL)", true).
		Pluck("product_skus.product_id", &skuHidden).Error; err != nil {
		return nil, err
	}
	for _, id := range skuHidden {
		if id != 0 {
			hidden[id] = struct{}{}
		}
	}

	ids := make([]uint, 0, len(hidden))
	for id := range hidden {
		ids = append(ids, id)
	}
	return ids, nil
}

// IsActiveRelatedAccount 判断用户是否为分销商已启用的关联账号。
func (r *GormResellerRepository) IsActiveRelatedAccount(resellerID uint, userID uint) (bool, error) {
	if resellerID == 0 || userID == 0 {
		return false, nil
	}
	var count int64
	if err := r.db.Model(&models.ResellerRelatedAccount{}).
		Where("reseller_id = ? AND user_id = ? AND status = ?", resellerID, userID, models.ResellerRelatedAccountStatusActive).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateOrderSnapshot 创建订单分销快照。
func (r *GormResellerRepository) CreateOrderSnapshot(snapshot *models.ResellerOrderSnapshot) error {
	if snapshot == nil || snapshot.OrderID == 0 || snapshot.ResellerID == 0 {
		return errors.New("invalid reseller order snapshot")
	}
	profitEligible := snapshot.ProfitEligible
	if err := r.db.Create(snapshot).Error; err != nil {
		return err
	}
	if !profitEligible {
		if err := r.db.Model(&models.ResellerOrderSnapshot{}).
			Where("id = ?", snapshot.ID).
			Update("profit_eligible", false).Error; err != nil {
			return err
		}
		snapshot.ProfitEligible = false
	}
	return nil
}

// GetOrderSnapshotByOrderID 按订单 ID 获取订单分销快照。
func (r *GormResellerRepository) GetOrderSnapshotByOrderID(orderID uint) (*models.ResellerOrderSnapshot, error) {
	if orderID == 0 {
		return nil, nil
	}
	var snapshot models.ResellerOrderSnapshot
	if err := r.db.Where("order_id = ?", orderID).First(&snapshot).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &snapshot, nil
}

// CreateLedgerEntryIfNotExists 按幂等键创建分销账务流水。
func (r *GormResellerRepository) CreateLedgerEntryIfNotExists(entry *models.ResellerLedgerEntry) (bool, error) {
	if entry == nil {
		return false, errors.New("reseller ledger entry is nil")
	}
	key := strings.TrimSpace(entry.IdempotencyKey)
	if key == "" {
		return false, errors.New("reseller ledger idempotency key is empty")
	}
	existing, err := r.GetLedgerEntryByIdempotencyKey(key)
	if err != nil {
		return false, err
	}
	if existing != nil {
		return false, nil
	}
	now := time.Now()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now
	if err := r.db.Create(entry).Error; err != nil {
		var again models.ResellerLedgerEntry
		if lookupErr := r.db.Where("idempotency_key = ?", key).First(&again).Error; lookupErr == nil {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetLedgerEntryByIdempotencyKey 按幂等键获取分销账务流水。
func (r *GormResellerRepository) GetLedgerEntryByIdempotencyKey(key string) (*models.ResellerLedgerEntry, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	var row models.ResellerLedgerEntry
	if err := r.db.Where("idempotency_key = ?", key).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// MarkDueLedgerEntriesAvailable 将到期确认流水转为可提现。
func (r *GormResellerRepository) MarkDueLedgerEntriesAvailable(now time.Time) (int64, error) {
	res := r.db.Model(&models.ResellerLedgerEntry{}).
		Where("status = ? AND available_at IS NOT NULL AND available_at <= ?", models.ResellerLedgerStatusPendingConfirm, now).
		Updates(map[string]interface{}{
			"status":     models.ResellerLedgerStatusAvailable,
			"updated_at": now,
		})
	return res.RowsAffected, res.Error
}

// ListLedgerEntries 分页列出分销账务流水。
func (r *GormResellerRepository) ListLedgerEntries(filter ResellerLedgerListFilter) ([]models.ResellerLedgerEntry, int64, error) {
	rows := make([]models.ResellerLedgerEntry, 0)
	query := r.db.Model(&models.ResellerLedgerEntry{})
	if filter.ResellerID != 0 {
		query = query.Where("reseller_id = ?", filter.ResellerID)
	}
	if currency := strings.TrimSpace(filter.Currency); currency != "" {
		query = query.Where("currency = ?", currency)
	}
	if typ := strings.TrimSpace(filter.Type); typ != "" {
		query = query.Where("type = ?", typ)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("status = ?", status)
	}
	if filter.OrderID != 0 {
		query = query.Where("order_id = ?", filter.OrderID)
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := applyPagination(query.Session(&gorm.Session{}), filter.Page, filter.PageSize).
		Order("id DESC").
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// SumLedgerAmount 汇总指定状态的分销账务金额。
func (r *GormResellerRepository) SumLedgerAmount(resellerID uint, currency string, statuses []string) (decimal.Decimal, error) {
	currency = strings.TrimSpace(currency)
	if resellerID == 0 || currency == "" || len(statuses) == 0 {
		return decimal.Zero, nil
	}
	var total decimal.Decimal
	err := r.db.Model(&models.ResellerLedgerEntry{}).
		Where("reseller_id = ? AND currency = ? AND status IN ?", resellerID, currency, statuses).
		Select("COALESCE(SUM(amount), 0)").
		Scan(&total).Error
	if err != nil {
		return decimal.Zero, err
	}
	return total.Round(2), nil
}

// GetOrCreateBalanceAccountForUpdate 获取或创建并锁定同币种余额账户。
func (r *GormResellerRepository) GetOrCreateBalanceAccountForUpdate(resellerID uint, currency string) (*models.ResellerBalanceAccount, error) {
	currency = strings.TrimSpace(currency)
	if resellerID == 0 || currency == "" {
		return nil, errors.New("invalid reseller balance account")
	}
	var row models.ResellerBalanceAccount
	err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("reseller_id = ? AND currency = ?", resellerID, currency).
		First(&row).Error
	if err == nil {
		return &row, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := time.Now()
	row = models.ResellerBalanceAccount{
		ResellerID: resellerID,
		Currency:   currency,
		Status:     models.ResellerBalanceStatusNormal,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := r.db.Create(&row).Error; err != nil {
		return nil, err
	}
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, row.ID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// UpdateBalanceAccount 更新分销余额账户缓存。
func (r *GormResellerRepository) UpdateBalanceAccount(account *models.ResellerBalanceAccount) error {
	if account == nil {
		return errors.New("reseller balance account is nil")
	}
	account.UpdatedAt = time.Now()
	return r.db.Save(account).Error
}

// ListAvailableLedgerEntriesForUpdate 锁定指定币种可提现正向流水。
func (r *GormResellerRepository) ListAvailableLedgerEntriesForUpdate(resellerID uint, currency string) ([]models.ResellerLedgerEntry, error) {
	rows := make([]models.ResellerLedgerEntry, 0)
	currency = strings.TrimSpace(currency)
	if resellerID == 0 || currency == "" {
		return rows, nil
	}
	err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("reseller_id = ? AND currency = ? AND status = ? AND withdraw_request_id IS NULL AND amount > 0",
			resellerID,
			currency,
			models.ResellerLedgerStatusAvailable,
		).
		Order("available_at ASC, id ASC").
		Find(&rows).Error
	return rows, err
}

// UpdateLedgerEntry 更新单条分销账务流水。
func (r *GormResellerRepository) UpdateLedgerEntry(entry *models.ResellerLedgerEntry) error {
	if entry == nil {
		return errors.New("reseller ledger entry is nil")
	}
	entry.UpdatedAt = time.Now()
	return r.db.Save(entry).Error
}

// BatchUpdateLedgerEntries 批量更新分销账务流水。
func (r *GormResellerRepository) BatchUpdateLedgerEntries(ids []uint, updates map[string]interface{}) error {
	if len(ids) == 0 {
		return nil
	}
	if updates == nil {
		updates = map[string]interface{}{}
	}
	updates["updated_at"] = time.Now()
	return r.db.Model(&models.ResellerLedgerEntry{}).Where("id IN ?", ids).Updates(updates).Error
}

// BatchUpdateLedgerEntriesByWithdrawID 按提现单 ID 批量更新分销账务流水。
func (r *GormResellerRepository) BatchUpdateLedgerEntriesByWithdrawID(withdrawID uint, updates map[string]interface{}) error {
	if withdrawID == 0 {
		return nil
	}
	if updates == nil {
		updates = map[string]interface{}{}
	}
	updates["updated_at"] = time.Now()
	return r.db.Model(&models.ResellerLedgerEntry{}).Where("withdraw_request_id = ?", withdrawID).Updates(updates).Error
}

// CreateWithdrawRequest 创建分销提现申请。
func (r *GormResellerRepository) CreateWithdrawRequest(req *models.ResellerWithdrawRequest) error {
	if req == nil {
		return errors.New("reseller withdraw request is nil")
	}
	now := time.Now()
	if req.CreatedAt.IsZero() {
		req.CreatedAt = now
	}
	req.UpdatedAt = now
	return r.db.Create(req).Error
}

// GetWithdrawRequestByID 按 ID 获取分销提现申请。
func (r *GormResellerRepository) GetWithdrawRequestByID(id uint) (*models.ResellerWithdrawRequest, error) {
	if id == 0 {
		return nil, nil
	}
	var row models.ResellerWithdrawRequest
	if err := r.db.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// GetWithdrawRequestByIDForUpdate 按 ID 获取并锁定分销提现申请。
func (r *GormResellerRepository) GetWithdrawRequestByIDForUpdate(id uint) (*models.ResellerWithdrawRequest, error) {
	if id == 0 {
		return nil, nil
	}
	var row models.ResellerWithdrawRequest
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// UpdateWithdrawRequest 更新分销提现申请。
func (r *GormResellerRepository) UpdateWithdrawRequest(req *models.ResellerWithdrawRequest) error {
	if req == nil {
		return errors.New("reseller withdraw request is nil")
	}
	req.UpdatedAt = time.Now()
	return r.db.Save(req).Error
}

// ListWithdrawRequests 分页列出分销提现申请。
func (r *GormResellerRepository) ListWithdrawRequests(filter ResellerWithdrawListFilter) ([]models.ResellerWithdrawRequest, int64, error) {
	rows := make([]models.ResellerWithdrawRequest, 0)
	query := r.db.Model(&models.ResellerWithdrawRequest{})
	if filter.ResellerID != 0 {
		query = query.Where("reseller_id = ?", filter.ResellerID)
	}
	if currency := strings.TrimSpace(filter.Currency); currency != "" {
		query = query.Where("currency = ?", currency)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := applyPagination(query.Session(&gorm.Session{}), filter.Page, filter.PageSize).
		Order("id DESC").
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListAdminResellerLedgerEntries 分页列出管理端分销账务流水。
func (r *GormResellerRepository) ListAdminResellerLedgerEntries(filter ResellerAdminLedgerListFilter) ([]models.ResellerLedgerEntry, int64, error) {
	rows := make([]models.ResellerLedgerEntry, 0)
	query := r.db.Model(&models.ResellerLedgerEntry{}).
		Preload("Profile").
		Preload("Profile.User").
		Preload("Order")

	query = r.applyAdminResellerProfileFilters(query, "reseller_ledger_entries", filter.ResellerID, filter.UserID, filter.Keyword, "")
	if currency := strings.TrimSpace(filter.Currency); currency != "" {
		query = query.Where("reseller_ledger_entries.currency = ?", currency)
	}
	if typ := strings.TrimSpace(filter.Type); typ != "" {
		query = query.Where("reseller_ledger_entries.type = ?", typ)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("reseller_ledger_entries.status = ?", status)
	}
	if filter.OrderID != 0 {
		query = query.Where("reseller_ledger_entries.order_id = ?", filter.OrderID)
	}
	if orderNo := strings.TrimSpace(filter.OrderNo); orderNo != "" {
		query = query.Joins("LEFT JOIN orders o_filter ON o_filter.id = reseller_ledger_entries.order_id").
			Where("o_filter.order_no = ?", orderNo)
	}
	if filter.CreatedFrom != nil {
		query = query.Where("reseller_ledger_entries.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where("reseller_ledger_entries.created_at <= ?", *filter.CreatedTo)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := applyPagination(query.Session(&gorm.Session{}), filter.Page, filter.PageSize).
		Order("reseller_ledger_entries.id DESC").
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListAdminResellerBalanceAccounts 分页列出管理端分销余额账户。
func (r *GormResellerRepository) ListAdminResellerBalanceAccounts(filter ResellerAdminBalanceAccountListFilter) ([]models.ResellerBalanceAccount, int64, error) {
	rows := make([]models.ResellerBalanceAccount, 0)
	query := r.db.Model(&models.ResellerBalanceAccount{}).
		Preload("Profile").
		Preload("Profile.User")

	query = r.applyAdminResellerProfileFilters(query, "reseller_balance_accounts", filter.ResellerID, filter.UserID, filter.Keyword, "")
	if currency := strings.TrimSpace(filter.Currency); currency != "" {
		query = query.Where("reseller_balance_accounts.currency = ?", currency)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("reseller_balance_accounts.status = ?", status)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := applyPagination(query.Session(&gorm.Session{}), filter.Page, filter.PageSize).
		Order("reseller_balance_accounts.id DESC").
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListAdminResellerWithdrawRequests 分页列出管理端分销提现申请。
func (r *GormResellerRepository) ListAdminResellerWithdrawRequests(filter ResellerAdminWithdrawListFilter) ([]models.ResellerWithdrawRequest, int64, error) {
	rows := make([]models.ResellerWithdrawRequest, 0)
	query := r.db.Model(&models.ResellerWithdrawRequest{}).
		Preload("Profile").
		Preload("Profile.User").
		Preload("Processor")

	query = r.applyAdminResellerProfileFilters(query, "reseller_withdraw_requests", filter.ResellerID, filter.UserID, filter.Keyword, "reseller_withdraw_requests.account")
	if currency := strings.TrimSpace(filter.Currency); currency != "" {
		query = query.Where("reseller_withdraw_requests.currency = ?", currency)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("reseller_withdraw_requests.status = ?", status)
	}
	if filter.CreatedFrom != nil {
		query = query.Where("reseller_withdraw_requests.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where("reseller_withdraw_requests.created_at <= ?", *filter.CreatedTo)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := applyPagination(query.Session(&gorm.Session{}), filter.Page, filter.PageSize).
		Order("reseller_withdraw_requests.id DESC").
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *GormResellerRepository) applyAdminResellerProfileFilters(query *gorm.DB, table string, resellerID uint, userID uint, keyword string, accountColumn string) *gorm.DB {
	if resellerID != 0 {
		query = query.Where(table+".reseller_id = ?", resellerID)
	}
	keyword = strings.TrimSpace(keyword)
	if userID == 0 && keyword == "" {
		return query
	}

	query = query.
		Joins("LEFT JOIN reseller_profiles rp_filter ON rp_filter.id = " + table + ".reseller_id").
		Joins("LEFT JOIN users u_filter ON u_filter.id = rp_filter.user_id")
	if userID != 0 {
		query = query.Where("rp_filter.user_id = ?", userID)
	}
	if keyword == "" {
		return query
	}

	like := "%" + keyword + "%"
	conditions := []string{"u_filter.email LIKE ?", "u_filter.display_name LIKE ?"}
	args := []interface{}{like, like}
	if id, err := strconv.ParseUint(keyword, 10, 64); err == nil && id > 0 {
		conditions = append(conditions, "rp_filter.id = ?")
		args = append(args, uint(id))
	}
	if accountColumn != "" {
		conditions = append(conditions, accountColumn+" LIKE ?")
		args = append(args, like)
	}
	return query.Where("("+strings.Join(conditions, " OR ")+")", args...)
}

func normalizeDomainForRepository(raw string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
}

func uniqueUintSlice(values []uint) []uint {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(values))
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
