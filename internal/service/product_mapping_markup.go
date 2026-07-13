package service

import (
	"fmt"
	"strconv"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/upstream"

	"github.com/shopspring/decimal"
)

// ReapplyMarkup 对指定连接的所有映射商品重新应用加价规则
func (s *ProductMappingService) ReapplyMarkup(connectionID uint) (int, error) {
	conn, err := s.connService.GetByID(connectionID)
	if err != nil {
		return 0, err
	}
	if conn == nil {
		return 0, ErrConnectionNotFound
	}

	mappings, err := s.mappingRepo.ListActiveByConnection(connectionID)
	if err != nil {
		return 0, err
	}
	for _, mapping := range mappings {
		if mapping.Provider == upstream.CatalogProviderTGX &&
			(conn.ExchangeRate.LessThanOrEqual(decimal.Zero) || conn.ExchangeRate.GreaterThanOrEqual(decimal.NewFromInt(1))) {
			return 0, fmt.Errorf("TGX CNY-to-USD exchange rate must be greater than 0 and less than 1")
		}
	}

	updated := 0
	for _, mapping := range mappings {
		skuMappings, err := s.skuMappingRepo.ListByProductMapping(mapping.ID)
		if err != nil {
			continue
		}

		for _, sm := range skuMappings {
			newLocalPrice := providerMappingLocalPrice(mapping.Provider, sm.UpstreamPrice.Decimal, conn)
			localSKU, err := s.productSKURepo.GetByID(sm.LocalSKUID)
			if err != nil || localSKU == nil {
				continue
			}
			localSKU.PriceAmount = models.NewMoneyFromDecimal(newLocalPrice.Round(2))
			localSKU.CostPriceAmount = models.NewMoneyFromDecimal(providerMappingCostPrice(mapping.Provider, sm.UpstreamPrice.Decimal, conn))
			_ = s.productSKURepo.Update(localSKU)
		}

		// 更新 Product.PriceAmount
		localProduct, err := s.productRepo.GetByID(strconv.FormatUint(uint64(mapping.LocalProductID), 10))
		if err == nil && localProduct != nil {
			s.recalcProductPrice(localProduct)
			updated++
		}
	}

	return updated, nil
}

func providerMappingLocalPrice(provider string, upstreamPrice decimal.Decimal, conn *models.SiteConnection) decimal.Decimal {
	switch provider {
	case upstream.CatalogProviderFansGurus:
		price, err := upstream.FansGurusTargetRate(upstreamPrice.String())
		if err == nil {
			if parsed, parseErr := decimal.NewFromString(price); parseErr == nil {
				return parsed
			}
		}
	case upstream.CatalogProviderTGX:
		if conn != nil {
			if conn.ExchangeRate.LessThanOrEqual(decimal.Zero) || conn.ExchangeRate.GreaterThanOrEqual(decimal.NewFromInt(1)) {
				return decimal.Zero
			}
			return CalculateLocalPrice(upstreamPrice, conn.ExchangeRate, conn.PriceMarkupPercent, conn.PriceRoundingMode)
		}
	}
	if conn == nil {
		return upstreamPrice
	}
	return CalculateLocalPrice(upstreamPrice, conn.ExchangeRate, conn.PriceMarkupPercent, conn.PriceRoundingMode)
}

func providerMappingCostPrice(provider string, upstreamPrice decimal.Decimal, conn *models.SiteConnection) decimal.Decimal {
	if provider == upstream.CatalogProviderFansGurus || conn == nil {
		return upstreamPrice.Round(2)
	}
	return convertCurrency(upstreamPrice, conn.ExchangeRate).Round(2)
}

// recalcProductPrice 重新计算商品基准价格和成本价为最低活跃 SKU 价格
func (s *ProductMappingService) recalcProductPrice(product *models.Product) {
	allSKUs, err := s.productSKURepo.ListByProduct(product.ID, true)
	if err != nil || len(allSKUs) == 0 {
		return
	}
	minPrice := allSKUs[0].PriceAmount.Decimal
	minCostPrice := allSKUs[0].CostPriceAmount.Decimal
	for _, sku := range allSKUs[1:] {
		if sku.PriceAmount.Decimal.LessThan(minPrice) {
			minPrice = sku.PriceAmount.Decimal
		}
		if sku.CostPriceAmount.Decimal.LessThan(minCostPrice) {
			minCostPrice = sku.CostPriceAmount.Decimal
		}
	}
	product.PriceAmount = models.NewMoneyFromDecimal(minPrice.Round(2))
	product.CostPriceAmount = models.NewMoneyFromDecimal(minCostPrice.Round(2))
	_ = s.productRepo.Update(product)
}
