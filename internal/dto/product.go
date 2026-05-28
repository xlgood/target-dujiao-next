package dto

import (
	"github.com/dujiao-next/internal/models"
)

// ProductResp 商品公共响应
type ProductResp struct {
	ID                   uint                       `json:"id"`
	CategoryID           uint                       `json:"category_id"`
	Slug                 string                     `json:"slug"`
	SeoMeta              models.JSON                `json:"seo_meta"`
	Title                models.JSON                `json:"title"`
	Description          models.JSON                `json:"description"`
	Content              models.JSON                `json:"content"`
	PriceAmount          models.Money               `json:"price_amount"`
	WholesalePrices      models.WholesalePriceTiers `json:"wholesale_prices,omitempty"`
	Images               models.StringArray         `json:"images"`
	Tags                 models.StringArray         `json:"tags"`
	PurchaseType         string                     `json:"purchase_type"`
	MinPurchaseQuantity  int                        `json:"min_purchase_quantity"`
	MaxPurchaseQuantity  int                        `json:"max_purchase_quantity"`
	FulfillmentType      string                     `json:"fulfillment_type"`
	ManualFormSchema     models.JSON                `json:"manual_form_schema"`
	ManualStockAvailable int                        `json:"manual_stock_available"`
	AutoStockAvailable   int64                      `json:"auto_stock_available"`
	StockStatus          string                     `json:"stock_status"`
	IsSoldOut            bool                       `json:"is_sold_out"`

	// 支付渠道限制
	PaymentChannelIDs []uint `json:"payment_channel_ids,omitempty"`

	// 关联
	Category CategoryResp `json:"category,omitempty"`
	SKUs     []SKUResp    `json:"skus,omitempty"`

	// 促销/会员价
	PromotionID          *uint               `json:"promotion_id,omitempty"`
	PromotionName        string              `json:"promotion_name,omitempty"`
	PromotionType        string              `json:"promotion_type,omitempty"`
	PromotionPriceAmount *models.Money       `json:"promotion_price_amount,omitempty"`
	PromotionRules       []PromotionRuleResp `json:"promotion_rules,omitempty"`
	MemberPrices         []MemberLevelPrice  `json:"member_prices,omitempty"`

	// 关联文章（仅商品详情接口填充，列表接口不返回）
	RelatedPosts []RelatedPostCard `json:"related_posts,omitempty"`
}

// SKUResp 商品 SKU 公共响应
type SKUResp struct {
	ID                 uint         `json:"id"`
	SKUCode            string       `json:"sku_code"`
	SpecValues         models.JSON  `json:"spec_values"`
	PriceAmount        models.Money `json:"price_amount"`
	ManualStockTotal   int          `json:"manual_stock_total"`
	ManualStockSold    int          `json:"manual_stock_sold"`
	AutoStockAvailable int64        `json:"auto_stock_available"`
	UpstreamStock      int          `json:"upstream_stock"`
	IsActive           bool         `json:"is_active"`

	// 促销/会员价附加
	PromotionPriceAmount *models.Money `json:"promotion_price_amount,omitempty"`
	MemberPriceAmount    *models.Money `json:"member_price_amount,omitempty"`
}

// CategoryResp 分类公共响应
type CategoryResp struct {
	ID        uint        `json:"id"`
	ParentID  uint        `json:"parent_id"`
	Slug      string      `json:"slug"`
	Name      models.JSON `json:"name"`
	Icon      string      `json:"icon,omitempty"`
	SortOrder int         `json:"sort_order"`
}

// NewCategoryResp 从 models.Category 构造响应
func NewCategoryResp(c *models.Category) CategoryResp {
	return CategoryResp{
		ID:        c.ID,
		ParentID:  c.ParentID,
		Slug:      c.Slug,
		Name:      c.NameJSON,
		Icon:      c.Icon,
		SortOrder: c.SortOrder,
	}
}

// NewCategoryRespList 批量转换分类列表
func NewCategoryRespList(categories []models.Category) []CategoryResp {
	result := make([]CategoryResp, 0, len(categories))
	for i := range categories {
		result = append(result, NewCategoryResp(&categories[i]))
	}
	return result
}

// PromotionRuleResp 活动规则展示
type PromotionRuleResp struct {
	ID        uint         `json:"id"`
	Name      string       `json:"name"`
	Type      string       `json:"type"`
	Value     models.Money `json:"value"`
	MinAmount models.Money `json:"min_amount"`
}

// MemberLevelPrice 会员等级价格
type MemberLevelPrice struct {
	MemberLevelID uint         `json:"member_level_id"`
	SKUID         uint         `json:"sku_id"`
	PriceAmount   models.Money `json:"price_amount"`
}

// MemberLevelResp 会员等级公共响应
type MemberLevelResp struct {
	ID                uint        `json:"id"`
	Name              models.JSON `json:"name"`
	Slug              string      `json:"slug"`
	Icon              string      `json:"icon"`
	DiscountRate      float64     `json:"discount_rate"`
	RechargeThreshold float64     `json:"recharge_threshold"`
	SpendThreshold    float64     `json:"spend_threshold"`
	IsDefault         bool        `json:"is_default"`
	SortOrder         int         `json:"sort_order"`
}
