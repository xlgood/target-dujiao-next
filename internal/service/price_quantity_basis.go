package service

import "github.com/shopspring/decimal"

// normalizePriceQuantityBasis preserves the legacy per-unit behavior for rows
// created before price_quantity_basis was introduced.
func normalizePriceQuantityBasis(basis int) int {
	if basis <= 0 {
		return 1
	}
	return basis
}

// NormalizePriceQuantityBasis is used by transport adapters to present legacy
// rows as per-unit prices.
func NormalizePriceQuantityBasis(basis int) int {
	return normalizePriceQuantityBasis(basis)
}

func amountForQuantity(price decimal.Decimal, quantity, basis int) decimal.Decimal {
	if quantity <= 0 || price.IsZero() {
		return decimal.Zero
	}
	return price.Mul(decimal.NewFromInt(int64(quantity))).
		Div(decimal.NewFromInt(int64(normalizePriceQuantityBasis(basis)))).
		Round(2)
}

// SKUPriceQuantityBasis lets callers use a SKU-specific basis while keeping
// product-level basis compatibility for legacy SKU rows.
func SKUPriceQuantityBasis(productBasis, skuBasis int) int {
	if skuBasis > 0 {
		return skuBasis
	}
	return normalizePriceQuantityBasis(productBasis)
}
