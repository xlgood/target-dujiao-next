package service

import (
	"sort"

	"github.com/dujiao-next/internal/models"

	"github.com/shopspring/decimal"
)

func normalizeWholesalePriceInputs(inputs []WholesalePriceInput) (models.WholesalePriceTiers, error) {
	if len(inputs) == 0 {
		return models.WholesalePriceTiers{}, nil
	}

	seen := make(map[int]struct{}, len(inputs))
	tiers := make(models.WholesalePriceTiers, 0, len(inputs))
	for _, input := range inputs {
		minQuantity := input.MinQuantity
		unitPrice := input.UnitPrice.Round(2)
		if minQuantity <= 0 || unitPrice.LessThanOrEqual(decimal.Zero) {
			return nil, ErrWholesalePriceInvalid
		}
		if _, ok := seen[minQuantity]; ok {
			return nil, ErrWholesalePriceInvalid
		}
		seen[minQuantity] = struct{}{}
		tiers = append(tiers, models.WholesalePriceTier{
			MinQuantity: minQuantity,
			UnitPrice:   models.NewMoneyFromDecimal(unitPrice),
		})
	}

	sort.SliceStable(tiers, func(i, j int) bool {
		return tiers[i].MinQuantity < tiers[j].MinQuantity
	})
	return tiers, nil
}

// ResolveWholesaleUnitPrice 根据商品批发价阶梯解析成交单价。
// 仅在阶梯单价低于当前基准单价时生效，避免错误配置导致价格上浮。
func ResolveWholesaleUnitPrice(product *models.Product, baseUnitPrice decimal.Decimal, quantity int) (unitPrice decimal.Decimal, discount decimal.Decimal, matched bool) {
	return resolveWholesaleUnitPrice(product, baseUnitPrice, quantity, quantity)
}

// ResolveWholesaleUnitPriceWithMatchQuantity 按 matchQuantity 判断批发档位，按 quantity 计算当前行优惠。
// 商品存在多 SKU 时，批发门槛按同一商品总购买数判断，但每个订单行只计算自己的优惠金额。
func ResolveWholesaleUnitPriceWithMatchQuantity(product *models.Product, baseUnitPrice decimal.Decimal, matchQuantity, quantity int) (unitPrice decimal.Decimal, discount decimal.Decimal, matched bool) {
	return resolveWholesaleUnitPrice(product, baseUnitPrice, matchQuantity, quantity)
}

func resolveWholesaleUnitPrice(product *models.Product, baseUnitPrice decimal.Decimal, matchQuantity, quantity int) (unitPrice decimal.Decimal, discount decimal.Decimal, matched bool) {
	base := baseUnitPrice.Round(2)
	if product == nil || matchQuantity <= 0 || quantity <= 0 || base.LessThanOrEqual(decimal.Zero) || len(product.WholesalePrices) == 0 {
		return base, decimal.Zero, false
	}

	var best *models.WholesalePriceTier
	for i := range product.WholesalePrices {
		tier := &product.WholesalePrices[i]
		if tier.MinQuantity <= 0 || tier.UnitPrice.Decimal.LessThanOrEqual(decimal.Zero) {
			continue
		}
		if matchQuantity < tier.MinQuantity {
			continue
		}
		if best == nil || tier.MinQuantity > best.MinQuantity {
			best = tier
		}
	}
	if best == nil {
		return base, decimal.Zero, false
	}

	tierPrice := best.UnitPrice.Decimal.Round(2)
	if tierPrice.GreaterThanOrEqual(base) {
		return base, decimal.Zero, false
	}

	discount = base.Sub(tierPrice).Mul(decimal.NewFromInt(int64(quantity))).Round(2)
	return tierPrice, discount, true
}
