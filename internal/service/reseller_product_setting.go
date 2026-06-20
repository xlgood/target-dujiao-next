package service

import (
	"strconv"
	"strings"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type ResellerProductSettingService struct {
	settingRepo  repository.ResellerProductSettingRepository
	resellerRepo repository.ResellerRepository
	productRepo  repository.ProductRepository
}

func NewResellerProductSettingService(
	settingRepo repository.ResellerProductSettingRepository,
	resellerRepo repository.ResellerRepository,
	productRepo repository.ProductRepository,
) *ResellerProductSettingService {
	return &ResellerProductSettingService{settingRepo: settingRepo, resellerRepo: resellerRepo, productRepo: productRepo}
}

type ResellerProductSettingInput struct {
	SKUID             uint
	IsListed          bool
	PricingMode       string
	MarkupPercent     decimal.Decimal
	FixedMarkupAmount decimal.Decimal
	FixedPriceAmount  decimal.Decimal
	SortOrder         int
}

type ResellerProductSettingSaveInput struct {
	Settings []ResellerProductSettingInput
}

type ResellerProductSettingUserListInput struct {
	Page       int
	PageSize   int
	Keyword    string
	CategoryID uint
	Configured string
	Listed     string
}

type ResellerProductSettingAdminListInput struct {
	Page        int
	PageSize    int
	ResellerID  uint
	UserID      uint
	ProductID   uint
	Keyword     string
	PricingMode string
	Configured  string
	Listed      string
}

type ResellerProductSettingSummary = repository.ResellerProductSettingSummary

type ResellerProductSettingDetail struct {
	Profile          *models.ResellerProfile
	Product          models.Product
	Settings         []models.ResellerProductSetting
	EffectiveBySKUID map[uint]decimal.Decimal
	RuleBySKUID      map[uint]string
}

type ResellerProductSettingListRow struct {
	Profile          *models.ResellerProfile
	Product          models.Product
	Settings         []models.ResellerProductSetting
	EffectiveBySKUID map[uint]decimal.Decimal
	RuleBySKUID      map[uint]string
}

func (s *ResellerProductSettingService) ListUserProductSettings(userID uint, input ResellerProductSettingUserListInput) ([]ResellerProductSettingListRow, int64, error) {
	profile, err := s.requireActiveProfileByUser(userID)
	if err != nil {
		return nil, 0, err
	}
	rows, total, err := s.settingRepo.ListProductsWithSettings(repository.ResellerProductSettingListFilter{
		Page:       input.Page,
		PageSize:   input.PageSize,
		ResellerID: profile.ID,
		CategoryID: input.CategoryID,
		Keyword:    input.Keyword,
		Configured: input.Configured,
		Listed:     input.Listed,
		OnlyActive: true,
	})
	if err != nil {
		return nil, 0, err
	}
	return s.decorateRows(profile, rows), total, nil
}

func (s *ResellerProductSettingService) GetUserProductSetting(userID, productID uint) (*ResellerProductSettingDetail, error) {
	profile, err := s.requireActiveProfileByUser(userID)
	if err != nil {
		return nil, err
	}
	return s.getDetail(profile, productID)
}

func (s *ResellerProductSettingService) SaveUserProductSettings(userID, productID uint, input ResellerProductSettingSaveInput) (*ResellerProductSettingDetail, error) {
	profile, err := s.requireActiveProfileByUser(userID)
	if err != nil {
		return nil, err
	}
	if err := s.saveSettings(profile, productID, input); err != nil {
		return nil, err
	}
	return s.getDetail(profile, productID)
}

func (s *ResellerProductSettingService) ResetUserProductSetting(userID, productID, skuID uint) error {
	profile, err := s.requireActiveProfileByUser(userID)
	if err != nil {
		return err
	}
	return s.settingRepo.DeleteSetting(profile.ID, productID, skuID)
}

func (s *ResellerProductSettingService) ListAdminSettings(input ResellerProductSettingAdminListInput) ([]models.ResellerProductSetting, int64, error) {
	return s.settingRepo.ListAdminSettings(repository.ResellerProductSettingAdminListFilter{
		Page:        input.Page,
		PageSize:    input.PageSize,
		ResellerID:  input.ResellerID,
		UserID:      input.UserID,
		ProductID:   input.ProductID,
		Keyword:     input.Keyword,
		PricingMode: input.PricingMode,
		Configured:  input.Configured,
		Listed:      input.Listed,
	})
}

func (s *ResellerProductSettingService) SummarizeAdminSettings(resellerID uint) (ResellerProductSettingSummary, error) {
	if s == nil || s.settingRepo == nil || resellerID == 0 {
		return ResellerProductSettingSummary{}, nil
	}
	return s.settingRepo.SummarizeByResellerID(resellerID)
}

func (s *ResellerProductSettingService) GetAdminProductSetting(resellerID, productID uint) (*ResellerProductSettingDetail, error) {
	profile, err := s.requireActiveProfileByID(resellerID)
	if err != nil {
		return nil, err
	}
	return s.getDetail(profile, productID)
}

func (s *ResellerProductSettingService) SaveAdminProductSettings(resellerID, productID uint, input ResellerProductSettingSaveInput) (*ResellerProductSettingDetail, error) {
	profile, err := s.requireActiveProfileByID(resellerID)
	if err != nil {
		return nil, err
	}
	if err := s.saveSettings(profile, productID, input); err != nil {
		return nil, err
	}
	return s.getDetail(profile, productID)
}

func (s *ResellerProductSettingService) ResetAdminProductSetting(resellerID, productID, skuID uint) error {
	profile, err := s.requireActiveProfileByID(resellerID)
	if err != nil {
		return err
	}
	return s.settingRepo.DeleteSetting(profile.ID, productID, skuID)
}

func (s *ResellerProductSettingService) requireActiveProfileByUser(userID uint) (*models.ResellerProfile, error) {
	if s == nil || s.settingRepo == nil || s.resellerRepo == nil || s.productRepo == nil || userID == 0 {
		return nil, ErrNotFound
	}
	profile, err := s.resellerRepo.GetProfileByUserID(userID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, ErrResellerNotOpened
	}
	if profile.Status != models.ResellerProfileStatusActive {
		return nil, ErrResellerProfileInactive
	}
	return profile, nil
}

func (s *ResellerProductSettingService) requireActiveProfileByID(resellerID uint) (*models.ResellerProfile, error) {
	if s == nil || s.settingRepo == nil || s.resellerRepo == nil || s.productRepo == nil || resellerID == 0 {
		return nil, ErrNotFound
	}
	profile, err := s.resellerRepo.GetProfileByID(resellerID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, ErrNotFound
	}
	if profile.Status != models.ResellerProfileStatusActive {
		return nil, ErrResellerProfileInactive
	}
	return profile, nil
}

func (s *ResellerProductSettingService) getDetail(profile *models.ResellerProfile, productID uint) (*ResellerProductSettingDetail, error) {
	if profile == nil || productID == 0 {
		return nil, ErrNotFound
	}
	row, err := s.settingRepo.GetProductWithSettings(profile.ID, productID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	effective, rules, err := computeResellerProductEffectivePrices(profile, row.Product, row.Settings)
	if err != nil {
		return nil, err
	}
	return &ResellerProductSettingDetail{Profile: profile, Product: row.Product, Settings: row.Settings, EffectiveBySKUID: effective, RuleBySKUID: rules}, nil
}

func (s *ResellerProductSettingService) decorateRows(profile *models.ResellerProfile, rows []repository.ResellerProductSettingProductRow) []ResellerProductSettingListRow {
	out := make([]ResellerProductSettingListRow, 0, len(rows))
	for _, row := range rows {
		effective, rules, _ := computeResellerProductEffectivePrices(profile, row.Product, row.Settings)
		out = append(out, ResellerProductSettingListRow{Profile: profile, Product: row.Product, Settings: row.Settings, EffectiveBySKUID: effective, RuleBySKUID: rules})
	}
	return out
}

func (s *ResellerProductSettingService) saveSettings(profile *models.ResellerProfile, productID uint, input ResellerProductSettingSaveInput) error {
	if profile == nil || productID == 0 {
		return ErrNotFound
	}
	product, err := s.productRepo.GetAdminByID(strconv.FormatUint(uint64(productID), 10))
	if err != nil {
		return err
	}
	if product == nil {
		return ErrNotFound
	}
	normalizedSettings := make([]models.ResellerProductSetting, 0, len(input.Settings))
	for _, item := range input.Settings {
		normalized, err := normalizeResellerProductSettingInput(profile, product, item)
		if err != nil {
			return err
		}
		normalized.ResellerID = profile.ID
		normalized.ProductID = product.ID
		normalizedSettings = append(normalizedSettings, normalized)
	}
	return s.settingRepo.Transaction(func(tx *gorm.DB) error {
		repo := s.settingRepo.WithTx(tx)
		for _, setting := range normalizedSettings {
			if _, err := repo.UpsertSetting(setting); err != nil {
				return err
			}
		}
		return nil
	})
}

func normalizeResellerProductSettingInput(profile *models.ResellerProfile, product *models.Product, input ResellerProductSettingInput) (models.ResellerProductSetting, error) {
	if product == nil {
		return models.ResellerProductSetting{}, ErrNotFound
	}
	mode := strings.TrimSpace(input.PricingMode)
	if mode == "" {
		mode = models.ResellerPricingModeInherit
	}
	switch mode {
	case models.ResellerPricingModeInherit, models.ResellerPricingModeMarkupPercent, models.ResellerPricingModeFixedMarkup, models.ResellerPricingModeFixedPrice:
	default:
		return models.ResellerProductSetting{}, ErrResellerPricingModeInvalid
	}
	setting := models.ResellerProductSetting{
		SKUID:             input.SKUID,
		IsListed:          input.IsListed,
		PricingMode:       mode,
		MarkupPercent:     models.NewMoneyFromDecimal(input.MarkupPercent.Round(2)),
		FixedMarkupAmount: models.NewMoneyFromDecimal(input.FixedMarkupAmount.Round(2)),
		FixedPriceAmount:  models.NewMoneyFromDecimal(input.FixedPriceAmount.Round(2)),
		SortOrder:         input.SortOrder,
	}
	if !setting.IsListed {
		return setting, nil
	}
	if input.SKUID > 0 {
		sku := findResellerProductSKU(product.SKUs, input.SKUID)
		if sku == nil || !sku.IsActive {
			return models.ResellerProductSetting{}, ErrProductSKUInvalid
		}
		price, _, err := resolveResellerUnitAmount(profile, nil, &setting, sku.PriceAmount.Decimal.Round(2))
		if err != nil {
			return models.ResellerProductSetting{}, err
		}
		if err := validateResellerUnitAmount(profile, sku, sku.PriceAmount.Decimal.Round(2), price); err != nil {
			return models.ResellerProductSetting{}, err
		}
		return setting, nil
	}
	if len(product.SKUs) == 0 {
		basePrice := product.PriceAmount.Decimal.Round(2)
		price, _, err := resolveResellerUnitAmount(profile, &setting, nil, basePrice)
		if err != nil {
			return models.ResellerProductSetting{}, err
		}
		if err := validateResellerUnitAmount(profile, nil, basePrice, price); err != nil {
			return models.ResellerProductSetting{}, err
		}
		costPrice := product.CostPriceAmount.Decimal.Round(2)
		if costPrice.GreaterThan(decimal.Zero) && price.LessThan(costPrice) {
			return models.ResellerProductSetting{}, ErrResellerPriceBelowBase
		}
		return setting, nil
	}
	for i := range product.SKUs {
		sku := &product.SKUs[i]
		if !sku.IsActive {
			continue
		}
		price, _, err := resolveResellerUnitAmount(profile, &setting, nil, sku.PriceAmount.Decimal.Round(2))
		if err != nil {
			return models.ResellerProductSetting{}, err
		}
		if err := validateResellerUnitAmount(profile, sku, sku.PriceAmount.Decimal.Round(2), price); err != nil {
			return models.ResellerProductSetting{}, err
		}
	}
	return setting, nil
}

func computeResellerProductEffectivePrices(profile *models.ResellerProfile, product models.Product, settings []models.ResellerProductSetting) (map[uint]decimal.Decimal, map[uint]string, error) {
	effective := map[uint]decimal.Decimal{}
	rules := map[uint]string{}
	byProduct, bySKU := buildSettingIndexes(settings)
	productSetting := byProduct[product.ID]
	if productSetting != nil && productSetting.IsListed {
		price, rule, err := resolveResellerUnitAmount(profile, productSetting, nil, product.PriceAmount.Decimal.Round(2))
		if err != nil {
			return effective, rules, err
		}
		effective[0] = price.Round(2)
		rules[0] = rule.Source
	}
	for i := range product.SKUs {
		sku := &product.SKUs[i]
		if !sku.IsActive {
			continue
		}
		skuSetting := bySKU[resellerSettingKey{productID: product.ID, skuID: sku.ID}]
		if productSetting != nil && !productSetting.IsListed {
			continue
		}
		if skuSetting != nil && !skuSetting.IsListed {
			continue
		}
		price, rule, err := resolveResellerUnitAmount(profile, productSetting, skuSetting, sku.PriceAmount.Decimal.Round(2))
		if err != nil {
			return effective, rules, err
		}
		effective[sku.ID] = price.Round(2)
		rules[sku.ID] = rule.Source
	}
	return effective, rules, nil
}

func findResellerProductSKU(items []models.ProductSKU, skuID uint) *models.ProductSKU {
	for i := range items {
		if items[i].ID == skuID {
			return &items[i]
		}
	}
	return nil
}
