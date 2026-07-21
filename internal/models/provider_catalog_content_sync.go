package models

import "time"

// ProviderCatalogContentSyncRun records an asynchronous metadata-only sync.
// It intentionally does not store the complete public page: each mapped SKU's
// original category and description are retained on ProductMapping instead.
type ProviderCatalogContentSyncRun struct {
	ID               uint      `gorm:"primarykey" json:"id"`
	Status           string    `gorm:"type:varchar(20);not null;default:'queued';index" json:"status"`
	FansGurusPulled  int       `gorm:"not null;default:0" json:"fans_gurus_pulled"`
	TGXPulled        int       `gorm:"not null;default:0" json:"tgx_pulled"`
	TGXProfilePulled int       `gorm:"not null;default:0" json:"tgx_profile_pulled"`
	TGXProfileFailed int       `gorm:"not null;default:0" json:"tgx_profile_failed"`
	Matched          int       `gorm:"not null;default:0" json:"matched"`
	Updated          int       `gorm:"not null;default:0" json:"updated"`
	Skipped          int       `gorm:"not null;default:0" json:"skipped"`
	ErrorMessage     string    `gorm:"type:text" json:"error_message,omitempty"`
	StartedAt        time.Time `gorm:"index" json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	CreatedAt        time.Time `gorm:"index" json:"created_at"`
}

func (ProviderCatalogContentSyncRun) TableName() string {
	return "provider_catalog_content_sync_runs"
}
