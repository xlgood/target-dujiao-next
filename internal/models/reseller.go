package models

import (
	"time"

	"gorm.io/gorm"
)

const (
	ResellerProfileStatusPendingReview = "pending_review"
	ResellerProfileStatusActive        = "active"
	ResellerProfileStatusRejected      = "rejected"
	ResellerProfileStatusDisabled      = "disabled"

	ResellerSettlementStatusNormal = "normal"
	ResellerSettlementStatusFrozen = "frozen"

	ResellerDomainTypeSubdomain = "subdomain"
	ResellerDomainTypeCustom    = "custom"

	ResellerDomainVerificationPending  = "pending"
	ResellerDomainVerificationVerified = "verified"
	ResellerDomainVerificationFailed   = "failed"

	ResellerDomainStatusPendingReview = "pending_review"
	ResellerDomainStatusActive        = "active"
	ResellerDomainStatusDisabled      = "disabled"

	ResellerPricingModeInherit       = "inherit"
	ResellerPricingModeMarkupPercent = "markup_percent"
	ResellerPricingModeFixedMarkup   = "fixed_markup"
	ResellerPricingModeFixedPrice    = "fixed_price"

	ResellerLedgerTypeOrderProfit  = "order_profit"
	ResellerLedgerTypeRefundDeduct = "refund_deduct"
	ResellerLedgerTypeManualAdjust = "manual_adjust"
	ResellerLedgerTypeWithdrawLock = "withdraw_lock"
	ResellerLedgerTypeWithdrawPaid = "withdraw_paid"

	ResellerLedgerStatusPendingConfirm = "pending_confirm"
	ResellerLedgerStatusAvailable      = "available"
	ResellerLedgerStatusLocked         = "locked"
	ResellerLedgerStatusWithdrawn      = "withdrawn"
	ResellerLedgerStatusCanceled       = "canceled"

	ResellerWithdrawStatusPending  = "pending"
	ResellerWithdrawStatusRejected = "rejected"
	ResellerWithdrawStatusPaid     = "paid"

	ResellerBalanceStatusNormal          = "normal"
	ResellerBalanceStatusNegativeBalance = "negative_balance"
	ResellerBalanceStatusFrozenReview    = "frozen_review"
	ResellerBalanceStatusDisabled        = "disabled"

	ResellerRelatedAccountStatusActive   = "active"
	ResellerRelatedAccountStatusDisabled = "disabled"
)

// ResellerProfile 分销商资料。
type ResellerProfile struct {
	ID                   uint           `gorm:"primarykey" json:"id"`
	UserID               uint           `gorm:"not null;uniqueIndex" json:"user_id"`
	Status               string         `gorm:"type:varchar(32);index;not null;default:'pending_review'" json:"status"`
	ApplyReason          string         `gorm:"type:text" json:"apply_reason,omitempty"`
	RejectReason         string         `gorm:"type:text" json:"reject_reason,omitempty"`
	DefaultMarkupPercent Money          `gorm:"type:decimal(10,2);not null;default:0" json:"default_markup_percent"`
	MaxMarkupPercent     Money          `gorm:"type:decimal(10,2);not null;default:0" json:"max_markup_percent"`
	SettlementStatus     string         `gorm:"type:varchar(32);index;not null;default:'normal'" json:"settlement_status"`
	ReviewedBy           *uint          `gorm:"index" json:"reviewed_by,omitempty"`
	ReviewedAt           *time.Time     `gorm:"index" json:"reviewed_at,omitempty"`
	CreatedAt            time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt            time.Time      `gorm:"index" json:"updated_at"`
	DeletedAt            gorm.DeletedAt `gorm:"index" json:"-"`

	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (ResellerProfile) TableName() string { return "reseller_profiles" }

// ResellerDomain 分销商域名绑定。
type ResellerDomain struct {
	ID                 uint           `gorm:"primarykey" json:"id"`
	ResellerID         uint           `gorm:"not null;index" json:"reseller_id"`
	Domain             string         `gorm:"type:varchar(255);not null;index" json:"domain"`
	Type               string         `gorm:"type:varchar(24);not null" json:"type"`
	VerificationToken  string         `gorm:"type:varchar(128)" json:"verification_token,omitempty"`
	VerificationStatus string         `gorm:"type:varchar(24);index;not null;default:'pending'" json:"verification_status"`
	Status             string         `gorm:"type:varchar(24);index;not null;default:'pending_review'" json:"status"`
	IsPrimary          bool           `gorm:"not null;default:false" json:"is_primary"`
	VerifiedAt         *time.Time     `gorm:"index" json:"verified_at,omitempty"`
	CreatedAt          time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt          time.Time      `gorm:"index" json:"updated_at"`
	DeletedAt          gorm.DeletedAt `gorm:"index" json:"-"`

	Profile *ResellerProfile `gorm:"foreignKey:ResellerID" json:"profile,omitempty"`
}

func (ResellerDomain) TableName() string { return "reseller_domains" }

// ResellerSiteConfig 分销站点白标配置。
type ResellerSiteConfig struct {
	ID               uint           `gorm:"primarykey" json:"id"`
	ResellerID       uint           `gorm:"not null;index" json:"reseller_id"`
	SiteName         string         `gorm:"type:varchar(120)" json:"site_name"`
	Logo             string         `gorm:"type:varchar(500)" json:"logo"`
	Favicon          string         `gorm:"type:varchar(500)" json:"favicon"`
	AnnouncementJSON JSON           `gorm:"type:json" json:"announcement_json"`
	SupportJSON      JSON           `gorm:"type:json" json:"support_json"`
	SEOJSON          JSON           `gorm:"type:json" json:"seo_json"`
	FooterLinksJSON  JSON           `gorm:"type:json" json:"footer_links_json"`
	NavConfigJSON    JSON           `gorm:"type:json" json:"nav_config_json"`
	ThemeJSON        JSON           `gorm:"type:json" json:"theme_json"`
	CreatedAt        time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt        time.Time      `gorm:"index" json:"updated_at"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`
}

func (ResellerSiteConfig) TableName() string { return "reseller_site_configs" }

// ResellerProductSetting 分销商商品配置。
type ResellerProductSetting struct {
	ID                uint           `gorm:"primarykey" json:"id"`
	ResellerID        uint           `gorm:"not null;index" json:"reseller_id"`
	ProductID         uint           `gorm:"not null;index" json:"product_id"`
	SKUID             uint           `gorm:"column:sku_id;not null;default:0;index" json:"sku_id"`
	IsListed          bool           `gorm:"not null;default:true" json:"is_listed"`
	PricingMode       string         `gorm:"type:varchar(32);not null;default:'inherit'" json:"pricing_mode"`
	MarkupPercent     Money          `gorm:"type:decimal(10,2);not null;default:0" json:"markup_percent"`
	FixedMarkupAmount Money          `gorm:"type:decimal(20,2);not null;default:0" json:"fixed_markup_amount"`
	FixedPriceAmount  Money          `gorm:"type:decimal(20,2);not null;default:0" json:"fixed_price_amount"`
	SortOrder         int            `gorm:"not null;default:0;index" json:"sort_order"`
	CreatedAt         time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt         time.Time      `gorm:"index" json:"updated_at"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"-"`
}

func (ResellerProductSetting) TableName() string { return "reseller_product_settings" }

// ResellerOrderSnapshot 订单分销定价快照。
type ResellerOrderSnapshot struct {
	ID                  uint           `gorm:"primarykey" json:"id"`
	OrderID             uint           `gorm:"not null;uniqueIndex" json:"order_id"`
	ResellerID          uint           `gorm:"not null;index" json:"reseller_id"`
	Domain              string         `gorm:"type:varchar(255);not null;index" json:"domain"`
	Currency            string         `gorm:"type:varchar(16);not null" json:"currency"`
	ResellerUserID      uint           `gorm:"not null;index" json:"reseller_user_id"`
	BuyerUserID         uint           `gorm:"not null;default:0;index" json:"buyer_user_id"`
	BaseAmount          Money          `gorm:"type:decimal(20,2);not null;default:0" json:"base_amount"`
	ResellerAmount      Money          `gorm:"type:decimal(20,2);not null;default:0" json:"reseller_amount"`
	ProfitAmount        Money          `gorm:"type:decimal(20,2);not null;default:0" json:"profit_amount"`
	ProfitEligible      bool           `gorm:"not null;default:true;index" json:"profit_eligible"`
	ProfitBlockReason   string         `gorm:"type:varchar(64);index" json:"profit_block_reason"`
	PricingSnapshotJSON JSON           `gorm:"type:json" json:"pricing_snapshot_json"`
	RiskSnapshotJSON    JSON           `gorm:"type:json" json:"risk_snapshot_json"`
	CreatedAt           time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt           time.Time      `gorm:"index" json:"updated_at"`
	DeletedAt           gorm.DeletedAt `gorm:"index" json:"-"`
}

func (ResellerOrderSnapshot) TableName() string { return "reseller_order_snapshots" }

// ResellerLedgerEntry 分销商资金流水。
type ResellerLedgerEntry struct {
	ID                uint           `gorm:"primarykey" json:"id"`
	ResellerID        uint           `gorm:"not null;index" json:"reseller_id"`
	OrderID           *uint          `gorm:"index" json:"order_id,omitempty"`
	Type              string         `gorm:"type:varchar(32);not null;index" json:"type"`
	Amount            Money          `gorm:"type:decimal(20,2);not null;default:0" json:"amount"`
	Currency          string         `gorm:"type:varchar(16);not null;index" json:"currency"`
	IdempotencyKey    string         `gorm:"type:varchar(160);not null;uniqueIndex" json:"idempotency_key"`
	MetadataJSON      JSON           `gorm:"type:json" json:"metadata_json"`
	Status            string         `gorm:"type:varchar(32);not null;index" json:"status"`
	AvailableAt       *time.Time     `gorm:"index" json:"available_at,omitempty"`
	WithdrawRequestID *uint          `gorm:"index" json:"withdraw_request_id,omitempty"`
	Remark            string         `gorm:"type:text" json:"remark,omitempty"`
	CreatedAt         time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt         time.Time      `gorm:"index" json:"updated_at"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"-"`

	Profile *ResellerProfile `gorm:"foreignKey:ResellerID" json:"profile,omitempty"`
	Order   *Order           `gorm:"foreignKey:OrderID" json:"order,omitempty"`
}

func (ResellerLedgerEntry) TableName() string { return "reseller_ledger_entries" }

// ResellerWithdrawRequest 分销商提现申请。
type ResellerWithdrawRequest struct {
	ID           uint           `gorm:"primarykey" json:"id"`
	ResellerID   uint           `gorm:"not null;index" json:"reseller_id"`
	Amount       Money          `gorm:"type:decimal(20,2);not null;default:0" json:"amount"`
	Currency     string         `gorm:"type:varchar(16);not null;index" json:"currency"`
	Channel      string         `gorm:"type:varchar(64);not null" json:"channel"`
	Account      string         `gorm:"type:varchar(255);not null" json:"account"`
	Status       string         `gorm:"type:varchar(32);not null;index" json:"status"`
	RejectReason string         `gorm:"type:text" json:"reject_reason,omitempty"`
	ProcessedBy  *uint          `gorm:"index" json:"processed_by,omitempty"`
	ProcessedAt  *time.Time     `gorm:"index" json:"processed_at,omitempty"`
	CreatedAt    time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"index" json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`

	Profile   *ResellerProfile `gorm:"foreignKey:ResellerID" json:"profile,omitempty"`
	Processor *Admin           `gorm:"foreignKey:ProcessedBy" json:"processor,omitempty"`
}

func (ResellerWithdrawRequest) TableName() string { return "reseller_withdraw_requests" }

// ResellerBalanceAccount 分销商按币种余额账户。
type ResellerBalanceAccount struct {
	ID                   uint           `gorm:"primarykey" json:"id"`
	ResellerID           uint           `gorm:"not null;index" json:"reseller_id"`
	Currency             string         `gorm:"type:varchar(16);not null;index" json:"currency"`
	Status               string         `gorm:"type:varchar(32);not null;index;default:'normal'" json:"status"`
	AvailableAmountCache Money          `gorm:"type:decimal(20,2);not null;default:0" json:"available_amount_cache"`
	LockedAmountCache    Money          `gorm:"type:decimal(20,2);not null;default:0" json:"locked_amount_cache"`
	NegativeAmountCache  Money          `gorm:"type:decimal(20,2);not null;default:0" json:"negative_amount_cache"`
	LastLedgerEntryID    uint           `gorm:"not null;default:0" json:"last_ledger_entry_id"`
	RiskNote             string         `gorm:"type:text" json:"risk_note,omitempty"`
	CreatedAt            time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt            time.Time      `gorm:"index" json:"updated_at"`
	DeletedAt            gorm.DeletedAt `gorm:"index" json:"-"`

	Profile *ResellerProfile `gorm:"foreignKey:ResellerID" json:"profile,omitempty"`
}

func (ResellerBalanceAccount) TableName() string { return "reseller_balance_accounts" }

// ResellerRelatedAccount 分销商关联账号，用于自买风控。
type ResellerRelatedAccount struct {
	ID           uint           `gorm:"primarykey" json:"id"`
	ResellerID   uint           `gorm:"not null;index" json:"reseller_id"`
	UserID       uint           `gorm:"not null;index" json:"user_id"`
	RelationType string         `gorm:"type:varchar(32);not null;index" json:"relation_type"`
	Source       string         `gorm:"type:varchar(64);not null" json:"source"`
	Status       string         `gorm:"type:varchar(32);not null;index;default:'active'" json:"status"`
	Remark       string         `gorm:"type:text" json:"remark,omitempty"`
	CreatedBy    *uint          `gorm:"index" json:"created_by,omitempty"`
	CreatedAt    time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"index" json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

func (ResellerRelatedAccount) TableName() string { return "reseller_related_accounts" }
