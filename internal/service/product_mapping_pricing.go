package service

import "github.com/dujiao-next/internal/models"

// recalcProductPrice keeps a mapped product's displayed price aligned with its
// lowest active SKU after a price-sync operation.
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
