package dto

import (
	"github.com/dujiao-next/internal/models"
)

// CartProductResp 购物车商品摘要
type CartProductResp struct {
	Slug                string             `json:"slug"`
	Title               models.JSON        `json:"title"`
	PriceAmount         models.Money       `json:"price_amount"`
	PriceQuantityBasis  int                `json:"price_quantity_basis"`
	Images              models.StringArray `json:"images"`
	Tags                models.StringArray `json:"tags"`
	PurchaseType        string             `json:"purchase_type"`
	MinPurchaseQuantity int                `json:"min_purchase_quantity"`
	MaxPurchaseQuantity int                `json:"max_purchase_quantity"`
	FulfillmentType     string             `json:"fulfillment_type"`
	IsActive            bool               `json:"is_active"`
}

// CartItemResp 购物车项响应
type CartItemResp struct {
	ProductID          uint            `json:"product_id"`
	SKUID              uint            `json:"sku_id"`
	Quantity           int             `json:"quantity"`
	FulfillmentType    string          `json:"fulfillment_type"`
	UnitPrice          models.Money    `json:"unit_price"`
	OriginalPrice      models.Money    `json:"original_price"`
	PriceQuantityBasis int             `json:"price_quantity_basis"`
	Currency           string          `json:"currency"`
	Product            CartProductResp `json:"product"`
}
