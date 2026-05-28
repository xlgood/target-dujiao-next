package models

import (
	"time"

	"gorm.io/gorm"
)

// Order 订单表
type Order struct {
	ID                      uint           `gorm:"primarykey" json:"id"`                                                   // 主键
	OrderNo                 string         `gorm:"uniqueIndex;not null" json:"order_no"`                                   // 订单编号
	ParentID                *uint          `gorm:"index" json:"parent_id,omitempty"`                                       // 父订单ID
	UserID                  uint           `gorm:"index;not null" json:"user_id,omitempty"`                                // 用户ID（游客订单为 0）
	GuestEmail              string         `gorm:"index" json:"guest_email,omitempty"`                                     // 游客邮箱
	GuestPassword           string         `gorm:"type:varchar(200)" json:"-"`                                             // 游客订单密码
	GuestLocale             string         `gorm:"type:varchar(20)" json:"guest_locale,omitempty"`                         // 游客语言
	Status                  string         `gorm:"index;not null" json:"status"`                                           // 订单状态
	Currency                string         `gorm:"not null" json:"currency"`                                               // 币种
	OriginalAmount          Money          `gorm:"type:decimal(20,2);not null;default:0" json:"original_amount"`           // 原始金额
	DiscountAmount          Money          `gorm:"type:decimal(20,2);not null;default:0" json:"discount_amount"`           // 优惠金额
	MemberDiscountAmount    Money          `gorm:"type:decimal(20,2);not null;default:0" json:"member_discount_amount"`    // 会员优惠金额
	PromotionDiscountAmount Money          `gorm:"type:decimal(20,2);not null;default:0" json:"promotion_discount_amount"` // 活动价优惠金额
	WholesaleDiscountAmount Money          `gorm:"type:decimal(20,2);not null;default:0" json:"wholesale_discount_amount"` // 批发价优惠金额
	TotalAmount             Money          `gorm:"type:decimal(20,2);not null;default:0" json:"total_amount"`              // 实付金额
	WalletPaidAmount        Money          `gorm:"type:decimal(20,2);not null;default:0" json:"wallet_paid_amount"`        // 钱包支付金额
	OnlinePaidAmount        Money          `gorm:"type:decimal(20,2);not null;default:0" json:"online_paid_amount"`        // 在线支付金额
	RefundedAmount          Money          `gorm:"type:decimal(20,2);not null;default:0" json:"refunded_amount"`           // 已退款金额（退回钱包）
	MemberLevelID           *uint          `gorm:"index" json:"member_level_id,omitempty"`                                 // 下单时等级快照
	CouponID                *uint          `gorm:"index" json:"coupon_id,omitempty"`                                       // 优惠券ID
	PromotionID             *uint          `gorm:"index" json:"promotion_id,omitempty"`                                    // 活动价ID（单品订单）
	AffiliateProfileID      *uint          `gorm:"index" json:"affiliate_profile_id,omitempty"`                            // 推广返利关联用户ID快照
	AffiliateCode           string         `gorm:"type:varchar(32);index" json:"affiliate_code,omitempty"`                 // 推广返利联盟ID快照
	ClientIP                string         `gorm:"type:varchar(64)" json:"client_ip,omitempty"`                            // 下单客户端IP
	ExpiresAt               *time.Time     `gorm:"index" json:"expires_at"`                                                // 过期时间
	PaidAt                  *time.Time     `gorm:"index" json:"paid_at"`                                                   // 支付时间
	CanceledAt              *time.Time     `gorm:"index" json:"canceled_at"`                                               // 取消时间
	CreatedAt               time.Time      `gorm:"index" json:"created_at"`                                                // 创建时间
	UpdatedAt               time.Time      `gorm:"index" json:"updated_at"`                                                // 更新时间
	DeletedAt               gorm.DeletedAt `gorm:"index" json:"-"`                                                         // 软删除时间

	Items []OrderItem `gorm:"foreignKey:OrderID" json:"items,omitempty"` // 订单项
	// 关联
	Fulfillment *Fulfillment `gorm:"foreignKey:OrderID" json:"fulfillment,omitempty"` // 交付记录
	Children    []Order      `gorm:"foreignKey:ParentID" json:"children,omitempty"`   // 子订单
}

// TableName 指定表名
func (Order) TableName() string {
	return "orders"
}

// StripCostPrice 清除订单项中的成本价信息，避免前台用户看到成本价。
func (o *Order) StripCostPrice() {
	for i := range o.Items {
		o.Items[i].CostPrice = Money{}
	}
	for i := range o.Children {
		o.Children[i].StripCostPrice()
	}
}

// MaskUpstreamFulfillmentType 将订单及子订单中的 upstream 交付类型替换为 manual，
// 避免前台用户感知到上游对接的存在。
func (o *Order) MaskUpstreamFulfillmentType() {
	const upstream = "upstream"
	const manual = "manual"
	for i := range o.Items {
		if o.Items[i].FulfillmentType == upstream {
			o.Items[i].FulfillmentType = manual
		}
	}
	if o.Fulfillment != nil && o.Fulfillment.Type == upstream {
		o.Fulfillment.Type = manual
	}
	for i := range o.Children {
		o.Children[i].MaskUpstreamFulfillmentType()
	}
}

// TruncateFulfillmentPayload 截断订单及子订单中超长的交付内容，防止前端渲染崩溃。
func (o *Order) TruncateFulfillmentPayload() {
	if o.Fulfillment != nil {
		o.Fulfillment.TruncatePayload(FulfillmentPayloadMaxPreviewLines)
	}
	for i := range o.Children {
		o.Children[i].TruncateFulfillmentPayload()
	}
}
