package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
	Updated  int
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
		created, updated, err := s.importProviderCatalogItem(connectionID, item)
		if err != nil {
			return result, err
		}
		if created {
			result.Imported++
		} else if updated {
			result.Updated++
		} else {
			result.Skipped++
		}
	}
	return result, nil
}

func (s *ProductMappingService) importProviderCatalogItem(connectionID uint, item upstream.ProviderCatalogItem) (bool, bool, error) {
	if s == nil || s.mappingRepo == nil || s.productRepo == nil || s.productSKURepo == nil || s.categoryRepo == nil || s.skuMappingRepo == nil {
		return false, false, errorsProductMappingDependencyMissing()
	}
	if !item.Active || item.ContainsTelegram() {
		return false, false, nil
	}
	platform := item.Platform()
	if platform == "" {
		return false, false, nil
	}
	conn, err := s.providerCatalogConnection(connectionID, item.Provider)
	if err != nil {
		return false, false, err
	}
	if existing, lookupErr := s.mappingRepo.GetByConnectionAndUpstreamCode(connectionID, item.Code); lookupErr == nil && existing != nil {
		if existingProduct, getErr := s.productRepo.GetByID(strconv.FormatUint(uint64(existing.LocalProductID), 10)); getErr == nil && existingProduct != nil {
			item.Images = s.localizeProviderCatalogImages(item, conn, existingProduct.Images)
		}
	} else {
		item.Images = s.localizeProviderCatalogImages(item, conn, nil)
	}
	price, cost, err := providerCatalogAmounts(item.Provider, item.UpstreamPrice, item.TargetPrice, conn)
	if err != nil {
		return false, false, fmt.Errorf("calculate catalog price for %s:%s: %w", item.Provider, item.Code, err)
	}

	var created bool
	var updated bool
	err = s.productRepo.Transaction(func(tx *gorm.DB) error {
		mappingRepo := s.mappingRepo.WithTx(tx)
		existing, err := mappingRepo.GetByConnectionAndUpstreamCode(connectionID, item.Code)
		if err != nil {
			return err
		}
		if existing != nil {
			updated = true
			return s.refreshProviderCatalogItemInTx(tx, existing, item, platform, price, cost, conn)
		}
		return s.importProviderCatalogItemInTx(tx, connectionID, item, platform, price, cost, conn, &created)
	})
	if err != nil {
		return false, false, err
	}
	return created, updated, nil
}

func (s *ProductMappingService) localizeProviderCatalogImages(item upstream.ProviderCatalogItem, conn *models.SiteConnection, existing models.StringArray) []string {
	if item.Provider != upstream.CatalogProviderTGX || conn == nil || len(item.Images) == 0 {
		return item.Images
	}
	if len(existing) > 0 && strings.HasPrefix(strings.TrimSpace(existing[0]), "/uploads/") {
		return []string(existing)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := upstream.NewTGXClient(conn.BaseURL, conn.ApiKey, "")
	localized := make([]string, 0, len(item.Images))
	for _, imageURL := range item.Images {
		localPath, err := client.DownloadImage(ctx, imageURL, "uploads")
		if err != nil {
			localized = append(localized, imageURL)
			continue
		}
		if s.mediaService != nil {
			s.mediaService.RecordLocalFile(localPath, "upstream")
		}
		localized = append(localized, localPath)
	}
	return localized
}

func (s *ProductMappingService) importProviderCatalogItemInTx(tx *gorm.DB, connectionID uint, item upstream.ProviderCatalogItem, platform string, price decimal.Decimal, cost decimal.Decimal, conn *models.SiteConnection, imported *bool) error {
	productRepo := s.productRepo.WithTx(tx)
	productSKURepo := s.productSKURepo.WithTx(tx)
	mappingRepo := s.mappingRepo.WithTx(tx)
	skuMappingRepo := s.skuMappingRepo.WithTx(tx)

	category, err := findOrCreateProviderCategoryTx(tx, platform)
	if err != nil {
		return err
	}

	slug, err := s.providerCatalogProductSlug(item.Provider, platform, item.Code, 0)
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
		PriceQuantityBasis:   providerCatalogPriceQuantityBasis(item),
		CostPriceAmount:      models.NewMoneyFromDecimal(cost),
		Images:               models.StringArray(item.Images),
		SortOrder:            item.SortOrder,
		Tags:                 models.StringArray{},
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
		if err := s.createProviderVariantSKU(productSKURepo, skuMappingRepo, product.ID, mapping.ID, item, variant, platform, conn, now); err != nil {
			return err
		}
	}

	*imported = true
	return nil
}

func (s *ProductMappingService) refreshProviderCatalogItemInTx(tx *gorm.DB, mapping *models.ProductMapping, item upstream.ProviderCatalogItem, platform string, price, cost decimal.Decimal, conn *models.SiteConnection) error {
	if mapping == nil || mapping.LocalProductID == 0 {
		return ErrMappingNotFound
	}
	productRepo := s.productRepo.WithTx(tx)
	productSKURepo := s.productSKURepo.WithTx(tx)
	skuMappingRepo := s.skuMappingRepo.WithTx(tx)
	product, err := productRepo.GetByID(strconv.FormatUint(uint64(mapping.LocalProductID), 10))
	if err != nil {
		return err
	}
	if product == nil {
		return ErrMappingNotFound
	}
	category, err := findOrCreateProviderCategoryTx(tx, platform)
	if err != nil {
		return err
	}
	product.CategoryID = category.ID
	slug, err := s.providerCatalogProductSlug(item.Provider, platform, item.Code, product.ID)
	if err != nil {
		return err
	}
	product.Slug = slug
	product.TitleJSON = localizedText(item.Name)
	product.DescriptionJSON = localizedText(item.Description)
	product.ContentJSON = localizedText(item.Description)
	product.PriceAmount = models.NewMoneyFromDecimal(price)
	product.PriceQuantityBasis = providerCatalogPriceQuantityBasis(item)
	product.CostPriceAmount = models.NewMoneyFromDecimal(cost)
	product.MinPurchaseQuantity = item.MinQuantity
	product.MaxPurchaseQuantity = item.MaxQuantity
	product.ManualFormSchemaJSON = providerCatalogManualFormSchema(item)
	product.Images = models.StringArray(item.Images)
	product.SortOrder = item.SortOrder
	product.Tags = models.StringArray{}
	product.IsMapped = true
	if err := productRepo.Update(product); err != nil {
		return err
	}
	variants := item.Variants
	if len(variants) == 0 {
		variants = []upstream.ProviderCatalogVariant{{Code: item.Code, Name: "default", UpstreamPrice: item.UpstreamPrice, TargetPrice: item.TargetPrice, Stock: -1, Active: true}}
	}
	now := time.Now()
	for _, variant := range variants {
		if err := s.refreshProviderVariantSKU(productSKURepo, skuMappingRepo, product.ID, mapping.ID, item, variant, platform, conn, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *ProductMappingService) refreshProviderVariantSKU(productSKURepo repository.ProductSKURepository, skuMappingRepo repository.SKUMappingRepository, productID, mappingID uint, item upstream.ProviderCatalogItem, variant upstream.ProviderCatalogVariant, platform string, conn *models.SiteConnection, now time.Time) error {
	skuMapping, err := skuMappingRepo.GetByMappingAndUpstreamSKUCode(mappingID, variant.Code)
	if err != nil {
		return err
	}
	if skuMapping == nil {
		return s.createProviderVariantSKU(productSKURepo, skuMappingRepo, productID, mappingID, item, variant, platform, conn, now)
	}
	sku, err := productSKURepo.GetByID(skuMapping.LocalSKUID)
	if err != nil {
		return err
	}
	if sku == nil {
		return s.createProviderVariantSKU(productSKURepo, skuMappingRepo, productID, mappingID, item, variant, platform, conn, now)
	}
	price, cost, err := providerCatalogAmounts(item.Provider, variant.UpstreamPrice, variant.TargetPrice, conn)
	if err != nil {
		return err
	}
	sku.PriceAmount = models.NewMoneyFromDecimal(price)
	sku.PriceQuantityBasis = providerCatalogPriceQuantityBasis(item)
	sku.CostPriceAmount = models.NewMoneyFromDecimal(cost)
	sku.SpecValuesJSON = providerVariantSpecValues(item.Provider, platform, variant)
	if err := productSKURepo.Update(sku); err != nil {
		return err
	}
	skuMapping.UpstreamPrice = models.NewMoneyFromDecimal(parseProviderUpstreamPrice(variant.UpstreamPrice))
	skuMapping.UpstreamSKUCode = variant.Code
	if variant.Stock >= 0 {
		skuMapping.UpstreamStock = variant.Stock
		skuMapping.UpstreamIsActive = variant.Active
		skuMapping.StockSyncedAt = &now
	}
	return skuMappingRepo.Update(skuMapping)
}

func (s *ProductMappingService) createProviderVariantSKU(
	productSKURepo repository.ProductSKURepository,
	skuMappingRepo repository.SKUMappingRepository,
	productID uint,
	mappingID uint,
	item upstream.ProviderCatalogItem,
	variant upstream.ProviderCatalogVariant,
	platform string,
	conn *models.SiteConnection,
	now time.Time,
) error {
	skuPrice, costPrice, err := providerCatalogAmounts(item.Provider, variant.UpstreamPrice, variant.TargetPrice, conn)
	if err != nil {
		return fmt.Errorf("calculate provider variant price: %w", err)
	}
	upstreamPrice := parseProviderUpstreamPrice(variant.UpstreamPrice)
	skuCode := normalizeProviderSKUCode(variant.Code)
	if skuCode == "" {
		skuCode = models.DefaultSKUCode
	}
	sku := models.ProductSKU{
		ProductID:          productID,
		SKUCode:            skuCode,
		SpecValuesJSON:     providerVariantSpecValues(item.Provider, platform, variant),
		PriceAmount:        models.NewMoneyFromDecimal(skuPrice),
		PriceQuantityBasis: providerCatalogPriceQuantityBasis(item),
		CostPriceAmount:    models.NewMoneyFromDecimal(costPrice),
		ManualStockTotal:   0,
		IsActive:           variant.Active,
	}
	if err := productSKURepo.Create(&sku); err != nil {
		return fmt.Errorf("create provider sku: %w", err)
	}

	skuMapping := models.SKUMapping{
		ProductMappingID: mappingID,
		LocalSKUID:       sku.ID,
		UpstreamSKUID:    0,
		UpstreamSKUCode:  variant.Code,
		UpstreamPrice:    models.NewMoneyFromDecimal(upstreamPrice),
		UpstreamStock:    variant.Stock,
		UpstreamIsActive: variant.Active,
		StockSyncedAt:    &now,
	}
	if err := skuMappingRepo.Create(&skuMapping); err != nil {
		return fmt.Errorf("create provider sku mapping: %w", err)
	}
	return nil
}

func (s *ProductMappingService) providerCatalogConnection(connectionID uint, provider string) (*models.SiteConnection, error) {
	if provider != upstream.CatalogProviderTGX || s.connService == nil {
		return nil, nil
	}
	conn, err := s.connService.GetByID(connectionID)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, ErrConnectionNotFound
	}
	return conn, nil
}

// providerCatalogAmounts returns local USD sale and cost amounts. FansGurus
// quotes USD directly; TGX quotes CNY and must be converted via its connection.
func providerCatalogAmounts(provider, upstreamRaw, fallbackTarget string, conn *models.SiteConnection) (decimal.Decimal, decimal.Decimal, error) {
	upstreamPrice := parseProviderUpstreamPrice(upstreamRaw)
	switch provider {
	case upstream.CatalogProviderFansGurus:
		local, err := decimal.NewFromString(fallbackTarget)
		if err != nil {
			return decimal.Zero, decimal.Zero, err
		}
		return local, upstreamPrice, nil
	case upstream.CatalogProviderTGX:
		if conn == nil {
			// Keep the pure import helper backward-compatible for callers that do
			// not have a persisted connection. Production catalog sync always
			// supplies the TGX connection and therefore never uses this branch.
			local, err := decimal.NewFromString(fallbackTarget)
			if err != nil {
				return decimal.Zero, decimal.Zero, err
			}
			return local, upstreamPrice, nil
		}
		if conn.ExchangeRate.LessThanOrEqual(decimal.Zero) || conn.ExchangeRate.GreaterThanOrEqual(decimal.NewFromInt(1)) {
			return decimal.Zero, decimal.Zero, fmt.Errorf("TGX CNY-to-USD exchange rate must be greater than 0 and less than 1")
		}
		return CalculateLocalPrice(upstreamPrice, conn.ExchangeRate, conn.PriceMarkupPercent, conn.PriceRoundingMode), convertCurrency(upstreamPrice, conn.ExchangeRate).Round(2), nil
	default:
		local, err := decimal.NewFromString(fallbackTarget)
		if err != nil {
			return decimal.Zero, decimal.Zero, err
		}
		return local, upstreamPrice, nil
	}
}

func parseProviderUpstreamPrice(raw string) decimal.Decimal {
	price, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero
	}
	return price
}

func providerCatalogPriceQuantityBasis(item upstream.ProviderCatalogItem) int {
	if item.PriceQuantityBasis > 0 {
		return item.PriceQuantityBasis
	}
	if item.Provider == upstream.CatalogProviderFansGurus {
		return 1000
	}
	return 1
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

func providerVariantSpecValues(_ string, _ string, variant upstream.ProviderCatalogVariant) models.JSON {
	values := models.JSON{}
	if variant.Name != "" && variant.Name != "default" {
		values["race"] = variant.Name
	}
	return values
}

func (s *ProductMappingService) providerCatalogProductSlug(provider, platform, code string, excludeProductID uint) (string, error) {
	hash := sha1.Sum([]byte(strings.TrimSpace(provider) + ":" + strings.TrimSpace(code)))
	base := normalizeProviderSlug(fmt.Sprintf("catalog-%s-%s", platform, hex.EncodeToString(hash[:])[:16]))
	excludeID := ""
	if excludeProductID > 0 {
		excludeID = strconv.FormatUint(uint64(excludeProductID), 10)
	}
	for i := 0; i < 100; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		var excluded *string
		if excludeID != "" {
			excluded = &excludeID
		}
		count, err := s.productRepo.CountBySlug(candidate, excluded)
		if err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("provider catalog slug conflict after retries: %s", base)
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
