package service

import (
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
	item.Images = providerCatalogImages(item, platform)
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
		TitleJSON:            localizedText(providerCatalogDisplayTitle(item.Name)),
		DescriptionJSON:      providerCatalogDescription(item),
		ContentJSON:          providerCatalogContent(item),
		SeoMetaJSON:          models.JSON{},
		ManualFormSchemaJSON: providerCatalogManualFormSchema(item),
		PriceAmount:          models.NewMoneyFromDecimal(price),
		PriceQuantityBasis:   catalogInitialPriceQuantityBasis(item),
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
		CatalogReviewStatus:     models.CatalogReviewPending,
		CatalogProfileSource:    "catalog",
		UpstreamFulfillmentType: providerCatalogUpstreamFulfillmentType(item),
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
	if mapping.PlatformLocked {
		platform = mapping.Platform
	} else {
		mapping.Platform = platform
		if err := s.mappingRepo.WithTx(tx).Update(mapping); err != nil {
			return err
		}
	}
	profileIsAuthoritative := mapping.CatalogProfileSource == "item" || mapping.CatalogProfileSource == "verified"
	if !profileIsAuthoritative {
		mapping.UpstreamFulfillmentType = providerCatalogUpstreamFulfillmentType(item)
	}
	mapping.UpstreamStatus = models.UpstreamStatusActive
	if !profileIsAuthoritative && !mapping.ManualFormSchemaLocked {
		mapping.CatalogProfileSource = "catalog"
	}
	now := time.Now()
	mapping.LastSyncedAt = &now
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
	product.TitleJSON = localizedText(providerCatalogDisplayTitle(item.Name))
	if strings.TrimSpace(mapping.CatalogSourceHash) == "" {
		product.DescriptionJSON = providerCatalogDescription(item)
		product.ContentJSON = providerCatalogContent(item)
	}
	if conn == nil || conn.AutoSyncPrice {
		product.PriceAmount = models.NewMoneyFromDecimal(price)
	}
	if basis := providerCatalogPriceQuantityBasis(item); basis > 0 {
		product.PriceQuantityBasis = basis
	}
	product.CostPriceAmount = models.NewMoneyFromDecimal(cost)
	product.MinPurchaseQuantity = item.MinQuantity
	product.MaxPurchaseQuantity = item.MaxQuantity
	if !profileIsAuthoritative && !mapping.ManualFormSchemaLocked {
		product.ManualFormSchemaJSON = providerCatalogManualFormSchema(item)
	}
	product.Images = models.StringArray(providerCatalogImages(item, platform))
	product.SortOrder = item.SortOrder
	product.Tags = models.StringArray{}
	product.IsMapped = true
	if err := productRepo.Update(product); err != nil {
		return err
	}
	if err := s.mappingRepo.WithTx(tx).Update(mapping); err != nil {
		return err
	}
	variants := item.Variants
	if len(variants) == 0 {
		variants = []upstream.ProviderCatalogVariant{{Code: item.Code, Name: "default", UpstreamPrice: item.UpstreamPrice, TargetPrice: item.TargetPrice, Stock: -1, Active: true}}
	}
	for _, variant := range variants {
		if err := s.refreshProviderVariantSKU(productSKURepo, skuMappingRepo, product.ID, mapping.ID, item, variant, platform, conn, now); err != nil {
			return err
		}
	}
	return nil
}

func providerCatalogUpstreamFulfillmentType(item upstream.ProviderCatalogItem) string {
	if item.UpstreamFulfillmentType == constants.FulfillmentTypeAuto {
		return constants.FulfillmentTypeAuto
	}
	return constants.FulfillmentTypeManual
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
	if conn == nil || conn.AutoSyncPrice {
		sku.PriceAmount = models.NewMoneyFromDecimal(price)
	}
	if basis := providerCatalogPriceQuantityBasis(item); basis > 0 {
		sku.PriceQuantityBasis = basis
	}
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
		PriceQuantityBasis: catalogInitialPriceQuantityBasis(item),
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
		StockSyncedAt:    providerCatalogStockSyncedAt(item.Provider, variant.Stock, now),
	}
	if err := skuMappingRepo.Create(&skuMapping); err != nil {
		return fmt.Errorf("create provider sku mapping: %w", err)
	}
	return nil
}

func providerCatalogStockSyncedAt(provider string, stock int, now time.Time) *time.Time {
	if provider == upstream.CatalogProviderTGX && stock < 0 {
		return nil
	}
	return &now
}

func providerCatalogImages(item upstream.ProviderCatalogItem, platform string) []string {
	if item.Provider == upstream.CatalogProviderTGX {
		images := make([]string, 0, len(item.Images))
		for _, image := range item.Images {
			image = strings.TrimSpace(image)
			if strings.HasPrefix(image, "https://") || strings.HasPrefix(image, "http://") {
				images = append(images, image)
			}
		}
		if len(images) > 0 {
			return images
		}
	}
	return []string{models.ProviderCatalogImagePath(platform)}
}

func providerCatalogDescription(item upstream.ProviderCatalogItem) models.JSON {
	if item.Provider == upstream.CatalogProviderTGX || item.Provider == upstream.CatalogProviderFansGurus {
		return providerCatalogServiceDescription()
	}
	return localizedText(item.Description)
}

func providerCatalogContent(item upstream.ProviderCatalogItem) models.JSON {
	if item.Provider == upstream.CatalogProviderTGX || item.Provider == upstream.CatalogProviderFansGurus {
		return providerCatalogServiceContent(item)
	}
	return localizedText(item.Description)
}

func providerCatalogServiceDescription() models.JSON {
	return models.JSON{
		"zh-CN": "请填写服务所需资料后提交订单。",
		"zh-TW": "請填寫服務所需資料後提交訂單。",
		"en-US": "Enter the required information before submitting your order.",
	}
}

func providerCatalogServiceContent(item upstream.ProviderCatalogItem) models.JSON {
	if item.Provider == upstream.CatalogProviderFansGurus && strings.EqualFold(strings.TrimSpace(item.Type), "custom comments") {
		return models.JSON{
			"zh-CN": "<h3>下单说明</h3><p>请填写目标链接，并每行填写一条评论。系统将根据有效评论条数自动计算数量和金额。</p>" + providerCatalogQuantityLine(item, "zh-CN"),
			"zh-TW": "<h3>下單說明</h3><p>請填寫目標連結，並每行填寫一則評論。系統會依有效評論數量自動計算數量和金額。</p>" + providerCatalogQuantityLine(item, "zh-TW"),
			"en-US": "<h3>Order information</h3><p>Enter the target link and one comment per line. Quantity and price are calculated from the number of valid comments.</p>" + providerCatalogQuantityLine(item, "en-US"),
		}
	}
	return models.JSON{
		"zh-CN": "<h3>下单说明</h3><p>请确认链接或账号信息准确无误后提交订单。</p>" + providerCatalogQuantityLine(item, "zh-CN"),
		"zh-TW": "<h3>下單說明</h3><p>請確認連結或帳號資訊準確無誤後提交訂單。</p>" + providerCatalogQuantityLine(item, "zh-TW"),
		"en-US": "<h3>Order information</h3><p>Verify that any link or account information is accurate before submitting your order.</p>" + providerCatalogQuantityLine(item, "en-US"),
	}
}

func providerCatalogQuantityLine(item upstream.ProviderCatalogItem, locale string) string {
	minimum := item.MinQuantity
	maximum := item.MaxQuantity
	if minimum <= 0 && maximum <= 0 {
		return ""
	}
	switch locale {
	case "zh-CN":
		if minimum > 0 && maximum > 0 {
			return fmt.Sprintf("<p>可购买数量：%d 至 %d。</p>", minimum, maximum)
		}
		if minimum > 0 {
			return fmt.Sprintf("<p>最小购买数量：%d。</p>", minimum)
		}
		return fmt.Sprintf("<p>最大购买数量：%d。</p>", maximum)
	case "zh-TW":
		if minimum > 0 && maximum > 0 {
			return fmt.Sprintf("<p>可購買數量：%d 至 %d。</p>", minimum, maximum)
		}
		if minimum > 0 {
			return fmt.Sprintf("<p>最小購買數量：%d。</p>", minimum)
		}
		return fmt.Sprintf("<p>最大購買數量：%d。</p>", maximum)
	default:
		if minimum > 0 && maximum > 0 {
			return fmt.Sprintf("<p>Purchase quantity: %d to %d.</p>", minimum, maximum)
		}
		if minimum > 0 {
			return fmt.Sprintf("<p>Minimum purchase quantity: %d.</p>", minimum)
		}
		return fmt.Sprintf("<p>Maximum purchase quantity: %d.</p>", maximum)
	}
}

func (s *ProductMappingService) providerCatalogConnection(connectionID uint, provider string) (*models.SiteConnection, error) {
	if s.connService == nil {
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

// providerCatalogAmounts returns local sale and cost amounts from the connection
// configuration. FansGurus uses exchange rate 1; TGX must use its CNY-to-USD rate.
func providerCatalogAmounts(provider, upstreamRaw, fallbackTarget string, conn *models.SiteConnection) (decimal.Decimal, decimal.Decimal, error) {
	upstreamPrice := parseProviderUpstreamPrice(upstreamRaw)
	if conn == nil {
		local, err := decimal.NewFromString(fallbackTarget)
		if err != nil {
			return decimal.Zero, decimal.Zero, err
		}
		return local, upstreamPrice, nil
	}
	if provider == upstream.CatalogProviderTGX && (conn.ExchangeRate.LessThanOrEqual(decimal.Zero) || conn.ExchangeRate.GreaterThanOrEqual(decimal.NewFromInt(1))) {
		return decimal.Zero, decimal.Zero, fmt.Errorf("TGX CNY-to-USD exchange rate must be greater than 0 and less than 1")
	}
	return CalculateLocalPrice(upstreamPrice, conn.ExchangeRate, conn.PriceMarkupPercent, conn.PriceRoundingMode), convertCurrency(upstreamPrice, conn.ExchangeRate).Round(2), nil
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
	if item.Provider == upstream.CatalogProviderTGX {
		return 1
	}
	return 0
}

func catalogInitialPriceQuantityBasis(item upstream.ProviderCatalogItem) int {
	if basis := providerCatalogPriceQuantityBasis(item); basis > 0 {
		return basis
	}
	// Model fields require a positive value. An unverified item is created
	// unpublished, so this placeholder cannot become a public price.
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
			Slug:      slug,
			NameJSON:  localizedText(platform),
			Icon:      models.ProviderCatalogImagePath(platform),
			IsActive:  true,
		}
		if err := tx.Create(&category).Error; err != nil {
			return nil, fmt.Errorf("create provider category: %w", err)
		}
	} else if strings.TrimSpace(category.Icon) == "" {
		category.Icon = models.ProviderCatalogImagePath(platform)
		if err := tx.Model(&models.Category{}).Where("id = ?", category.ID).Update("icon", category.Icon).Error; err != nil {
			return nil, fmt.Errorf("set provider category cover: %w", err)
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
				{"key": "link", "type": "url", "label": localizedFormLabel("目标链接", "目標連結", "Target URL"), "required": true},
			},
		}
	}
	return models.JSON{"fields": []map[string]interface{}{}}
}

func providerCatalogManualFormSchema(item upstream.ProviderCatalogItem) models.JSON {
	if len(item.ManualSchema) > 0 {
		return models.JSON(item.ManualSchema)
	}
	if item.Provider == upstream.CatalogProviderFansGurus {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "custom comments":
			return models.JSON{"fields": []map[string]interface{}{
				{"key": "link", "type": "url", "label": localizedFormLabel("目标链接", "目標連結", "Target URL"), "required": true},
				{"key": "comments", "type": "textarea", "label": localizedFormLabel("评论内容（每行一条）", "評論內容（每行一則）", "Comments (one per line)"), "placeholder": localizedFormLabel("每行填写一条评论", "每行填寫一則評論", "One comment per line"), "required": true},
			}}
		case "poll":
			return models.JSON{"fields": []map[string]interface{}{
				{"key": "link", "type": "url", "label": "Target URL", "required": true},
				{"key": "answer_number", "type": "number", "label": "Answer number", "required": true},
			}}
		case "invites from groups":
			return models.JSON{"fields": []map[string]interface{}{
				{"key": "link", "type": "url", "label": "Target URL", "required": true},
				{"key": "groups", "type": "textarea", "label": "Groups", "required": true},
			}}
		case "subscriptions":
			return models.JSON{"fields": []map[string]interface{}{
				{"key": "username", "type": "text", "label": "Username", "required": true},
				{"key": "min", "type": "number", "label": "Minimum quantity", "required": true},
				{"key": "max", "type": "number", "label": "Maximum quantity", "required": true},
				{"key": "posts", "type": "number", "label": "Posts", "required": false},
				{"key": "old_posts", "type": "number", "label": "Old posts", "required": false},
				{"key": "delay", "type": "number", "label": "Delay", "required": false},
				{"key": "expiry", "type": "text", "label": "Expiry", "required": false},
			}}
		case "", "default":
			return models.JSON{"fields": []map[string]interface{}{
				{"key": "link", "type": "url", "label": "Target URL", "required": true},
				{"key": "runs", "type": "number", "label": "Runs", "required": false},
				{"key": "interval", "type": "number", "label": "Interval (minutes)", "required": false},
			}}
		}
	}
	return providerManualFormSchema(item.Provider)
}

func localizedFormLabel(zhCN, zhTW, enUS string) models.JSON {
	return models.JSON{"zh-CN": zhCN, "zh-TW": zhTW, "en-US": enUS}
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
