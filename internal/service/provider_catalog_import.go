package service

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/upstream"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type ProviderCatalogImportResult struct {
	Imported int
	Skipped  int
}

func (s *ProductMappingService) ImportProviderCatalog(connectionID uint, catalog upstream.FilteredCatalog) (*ProviderCatalogImportResult, error) {
	return s.ImportProviderCatalogByProviderConnections(map[string]uint{
		upstream.CatalogProviderFansGurus: connectionID,
		upstream.CatalogProviderTGX:       connectionID,
	}, catalog)
}

func (s *ProductMappingService) ImportProviderCatalogByProviderConnections(connectionIDs map[string]uint, catalog upstream.FilteredCatalog) (*ProviderCatalogImportResult, error) {
	result := &ProviderCatalogImportResult{}
	items := append([]upstream.ProviderCatalogItem{}, catalog.FansGurus...)
	items = append(items, catalog.TGX...)

	for _, item := range items {
		if strings.TrimSpace(item.Code) == "" {
			result.Skipped++
			continue
		}
		connectionID := connectionIDs[item.Provider]
		if connectionID == 0 {
			return result, fmt.Errorf("missing connection id for provider %s", item.Provider)
		}
		imported, err := s.importProviderCatalogItem(connectionID, item)
		if err != nil {
			return result, err
		}
		if imported {
			result.Imported++
		} else {
			result.Skipped++
		}
	}
	return result, nil
}

func (s *ProductMappingService) importProviderCatalogItem(connectionID uint, item upstream.ProviderCatalogItem) (bool, error) {
	if s == nil || s.mappingRepo == nil || s.productRepo == nil || s.productSKURepo == nil || s.categoryRepo == nil || s.skuMappingRepo == nil {
		return false, errorsProductMappingDependencyMissing()
	}
	if !item.Active || item.ContainsTelegram() {
		return false, nil
	}
	platform := item.Platform()
	if platform == "" {
		return false, nil
	}
	existing, err := s.mappingRepo.GetByConnectionAndUpstreamCode(connectionID, item.Code)
	if err != nil {
		return false, err
	}
	if existing != nil {
		return false, nil
	}

	price, err := decimal.NewFromString(item.TargetPrice)
	if err != nil {
		return false, fmt.Errorf("parse target price for %s:%s: %w", item.Provider, item.Code, err)
	}
	cost, err := decimal.NewFromString(item.UpstreamPrice)
	if err != nil {
		cost = decimal.Zero
	}

	var imported bool
	err = s.productRepo.Transaction(func(tx *gorm.DB) error {
		return s.importProviderCatalogItemInTx(tx, connectionID, item, platform, price, cost, &imported)
	})
	if err != nil {
		return false, err
	}
	return imported, nil
}

func (s *ProductMappingService) importProviderCatalogItemInTx(tx *gorm.DB, connectionID uint, item upstream.ProviderCatalogItem, platform string, price decimal.Decimal, cost decimal.Decimal, imported *bool) error {
	productRepo := s.productRepo.WithTx(tx)
	productSKURepo := s.productSKURepo.WithTx(tx)
	mappingRepo := s.mappingRepo.WithTx(tx)
	skuMappingRepo := s.skuMappingRepo.WithTx(tx)

	category, err := findOrCreateProviderCategoryTx(tx, platform)
	if err != nil {
		return err
	}

	slugBase := normalizeProviderSlug(fmt.Sprintf("%s-%s-%s", item.Provider, platform, item.Code))
	slug, err := s.uniqueProductSlug(slugBase)
	if err != nil {
		return err
	}

	product := models.Product{
		CategoryID:           category.ID,
		Slug:                 slug,
		TitleJSON:            localizedText(item.Name),
		DescriptionJSON:      localizedText(item.Description),
		ContentJSON:          localizedText(item.Description),
		SeoMetaJSON:          models.JSON{},
		ManualFormSchemaJSON: providerCatalogManualFormSchema(item),
		PriceAmount:          models.NewMoneyFromDecimal(price),
		CostPriceAmount:      models.NewMoneyFromDecimal(cost),
		Images:               models.StringArray{},
		Tags:                 models.StringArray{platform, item.Provider},
		PurchaseType:         constants.ProductPurchaseMember,
		MinPurchaseQuantity:  item.MinQuantity,
		MaxPurchaseQuantity:  item.MaxQuantity,
		FulfillmentType:      constants.FulfillmentTypeUpstream,
		ManualStockTotal:     0,
		IsMapped:             true,
		IsActive:             false,
	}
	if err := productRepo.Create(&product); err != nil {
		return fmt.Errorf("create provider product: %w", err)
	}

	now := time.Now()
	mapping := models.ProductMapping{
		ConnectionID:            connectionID,
		LocalProductID:          product.ID,
		UpstreamProductID:       0,
		UpstreamProductCode:     item.Code,
		Provider:                item.Provider,
		Platform:                platform,
		UpstreamFulfillmentType: constants.FulfillmentTypeManual,
		UpstreamStatus:          models.UpstreamStatusActive,
		IsActive:                true,
		LastSyncedAt:            &now,
	}
	if err := mappingRepo.Create(&mapping); err != nil {
		return fmt.Errorf("create provider mapping: %w", err)
	}

	variants := item.Variants
	if len(variants) == 0 {
		variants = []upstream.ProviderCatalogVariant{{
			Code:          item.Code,
			Name:          "default",
			UpstreamPrice: item.UpstreamPrice,
			TargetPrice:   item.TargetPrice,
			Stock:         -1,
			Active:        true,
		}}
	}
	for _, variant := range variants {
		if err := s.createProviderVariantSKU(productSKURepo, skuMappingRepo, product.ID, mapping.ID, item, variant, platform, now); err != nil {
			return err
		}
	}

	*imported = true
	return nil
}

func (s *ProductMappingService) createProviderVariantSKU(
	productSKURepo repository.ProductSKURepository,
	skuMappingRepo repository.SKUMappingRepository,
	productID uint,
	mappingID uint,
	item upstream.ProviderCatalogItem,
	variant upstream.ProviderCatalogVariant,
	platform string,
	now time.Time,
) error {
	skuPrice, err := decimal.NewFromString(variant.TargetPrice)
	if err != nil {
		return fmt.Errorf("parse provider variant target price: %w", err)
	}
	upstreamPrice, err := decimal.NewFromString(variant.UpstreamPrice)
	if err != nil {
		upstreamPrice = decimal.Zero
	}
	skuCode := normalizeProviderSKUCode(variant.Code)
	if skuCode == "" {
		skuCode = models.DefaultSKUCode
	}
	sku := models.ProductSKU{
		ProductID:        productID,
		SKUCode:          skuCode,
		SpecValuesJSON:   providerVariantSpecValues(item.Provider, platform, variant),
		PriceAmount:      models.NewMoneyFromDecimal(skuPrice),
		CostPriceAmount:  models.NewMoneyFromDecimal(upstreamPrice),
		ManualStockTotal: 0,
		IsActive:         variant.Active,
	}
	if err := productSKURepo.Create(&sku); err != nil {
		return fmt.Errorf("create provider sku: %w", err)
	}

	stock := variant.Stock
	if stock == 0 {
		stock = -1
	}
	skuMapping := models.SKUMapping{
		ProductMappingID: mappingID,
		LocalSKUID:       sku.ID,
		UpstreamSKUID:    0,
		UpstreamSKUCode:  variant.Code,
		UpstreamPrice:    models.NewMoneyFromDecimal(upstreamPrice),
		UpstreamStock:    stock,
		UpstreamIsActive: variant.Active,
		StockSyncedAt:    &now,
	}
	if err := skuMappingRepo.Create(&skuMapping); err != nil {
		return fmt.Errorf("create provider sku mapping: %w", err)
	}
	return nil
}

func findOrCreateProviderCategoryTx(tx *gorm.DB, platform string) (*models.Category, error) {
	slug := "platform-" + normalizeProviderSlug(platform)
	var category models.Category
	if err := tx.Where("slug = ?", slug).First(&category).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		category = models.Category{
			Slug:     slug,
			NameJSON: localizedText(platform),
			IsActive: true,
		}
		if err := tx.Create(&category).Error; err != nil {
			return nil, fmt.Errorf("create provider category: %w", err)
		}
	}
	return &category, nil
}

func (s *ProductMappingService) uniqueProductSlug(base string) (string, error) {
	if base == "" {
		base = fmt.Sprintf("provider-product-%d", time.Now().UnixMilli())
	}
	for i := 0; i < 100; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		count, err := s.productRepo.CountBySlug(candidate, nil)
		if err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("slug conflict after retries: %s", base)
}

func providerManualFormSchema(provider string) models.JSON {
	if provider == upstream.CatalogProviderFansGurus {
		return models.JSON{
			"fields": []map[string]interface{}{
				{"key": "link", "type": "url", "label": "Target URL", "required": true},
			},
		}
	}
	return models.JSON{"fields": []map[string]interface{}{}}
}

func providerCatalogManualFormSchema(item upstream.ProviderCatalogItem) models.JSON {
	if len(item.ManualSchema) > 0 {
		return models.JSON(item.ManualSchema)
	}
	return providerManualFormSchema(item.Provider)
}

func providerVariantSpecValues(provider string, platform string, variant upstream.ProviderCatalogVariant) models.JSON {
	values := models.JSON{"provider": provider, "platform": platform}
	if variant.Name != "" && variant.Name != "default" {
		values["race"] = variant.Name
	}
	return values
}

func localizedText(value string) models.JSON {
	value = strings.TrimSpace(value)
	return models.JSON{
		"zh-CN": value,
		"zh-TW": value,
		"en-US": value,
	}
}

var providerSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeProviderSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = providerSlugRe.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if len(value) > 96 {
		value = strings.Trim(value[:96], "-")
	}
	return value
}

func normalizeProviderSKUCode(value string) string {
	slug := normalizeProviderSlug(value)
	hash := sha1.Sum([]byte(value))
	suffix := hex.EncodeToString(hash[:])[:8]
	if slug == "" {
		return suffix
	}
	if len(slug) > 55 {
		slug = strings.Trim(slug[:55], "-")
	}
	return slug + "-" + suffix
}

func errorsProductMappingDependencyMissing() error {
	return errors.New("product mapping service dependency missing")
}
