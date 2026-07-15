package models

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

// ProcurementOrder 采购单表（A 向 B 发起的上游采购记录）
type ProcurementOrder struct {
	ID                       uint           `gorm:"primarykey" json:"id"`
	ConnectionID             uint           `gorm:"index;not null" json:"connection_id"`
	LocalOrderID             uint           `gorm:"index;not null" json:"local_order_id"`
	LocalOrderNo             string         `gorm:"type:varchar(64);index" json:"local_order_no"`
	UpstreamOrderID          uint           `json:"-"`
	UpstreamOrderNo          string         `gorm:"type:varchar(64);index" json:"upstream_order_no,omitempty"`
	Status                   string         `gorm:"type:varchar(20);not null;default:'pending';index" json:"status"`
	UpstreamAmount           Money          `gorm:"type:decimal(20,2);not null;default:0" json:"upstream_amount"`
	UpstreamCurrency         string         `gorm:"type:varchar(10);not null;default:''" json:"upstream_currency"` // 上游货币代码
	LocalSellAmount          Money          `gorm:"type:decimal(20,2);not null;default:0" json:"local_sell_amount"`
	Currency                 string         `gorm:"type:varchar(10);not null" json:"currency"` // 本地货币代码
	ErrorMessage             string         `gorm:"type:text" json:"error_message,omitempty"`
	ManualReviewResolution   string         `gorm:"type:varchar(32)" json:"manual_review_resolution,omitempty"`
	ManualReviewResolvedAt   *time.Time     `json:"manual_review_resolved_at,omitempty"`
	SubmissionEnqueuedAt     *time.Time     `gorm:"index" json:"submission_enqueued_at,omitempty"`
	RetryCount               int            `gorm:"not null;default:0" json:"retry_count"`
	NextRetryAt              *time.Time     `gorm:"index" json:"next_retry_at,omitempty"`
	UpstreamPayload          string         `gorm:"type:text" json:"upstream_payload,omitempty"`
	UpstreamPayloadLineCount int            `gorm:"-" json:"upstream_payload_line_count"`
	TraceID                  string         `gorm:"type:varchar(64);index" json:"trace_id"`
	CreatedAt                time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt                time.Time      `gorm:"index" json:"updated_at"`
	DeletedAt                gorm.DeletedAt `gorm:"index" json:"-"`

	Connection    *SiteConnection `gorm:"foreignKey:ConnectionID" json:"connection,omitempty"`
	LocalOrder    *Order          `gorm:"foreignKey:LocalOrderID" json:"local_order,omitempty"`
	ParentOrderNo string          `gorm:"-" json:"parent_order_no,omitempty"` // 父订单号（虚拟字段）
	// UpstreamRefundRecords 仅用于接口返回，不写入数据库；值来自上游 /upstream/orders 的 refund_records
	UpstreamRefundRecords []JSON `gorm:"-" json:"upstream_refund_records,omitempty"`
	// UpstreamRefundedAmount 仅用于接口返回，不写入数据库；值来自上游 /upstream/orders 的 refunded_amount
	UpstreamRefundedAmount string `gorm:"-" json:"upstream_refunded_amount,omitempty"`
}

// TruncateUpstreamPayload 截断超长的上游交付内容。
func (p *ProcurementOrder) TruncateUpstreamPayload(maxLines int) {
	if p == nil || p.UpstreamPayload == "" {
		return
	}
	lines := strings.Split(p.UpstreamPayload, "\n")
	p.UpstreamPayloadLineCount = len(lines)
	if len(lines) > maxLines {
		p.UpstreamPayload = strings.Join(lines[:maxLines], "\n")
	}
}

// TableName 指定表名
func (ProcurementOrder) TableName() string {
	return "procurement_orders"
}
