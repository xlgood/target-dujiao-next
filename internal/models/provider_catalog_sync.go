package models

import "time"

type ProviderCatalogSyncRun struct {
	ID               uint      `gorm:"primarykey" json:"id"`
	Status           string    `gorm:"type:varchar(20);not null;default:'success';index" json:"status"`
	FansGurusPulled  int       `gorm:"not null;default:0" json:"fansgurus_pulled"`
	TGXPulled        int       `gorm:"not null;default:0" json:"tgx_pulled"`
	Imported         int       `gorm:"not null;default:0" json:"imported"`
	Updated          int       `gorm:"not null;default:0" json:"updated"`
	Skipped          int       `gorm:"not null;default:0" json:"skipped"`
	Deactivated      int       `gorm:"not null;default:0" json:"deactivated"`
	FilteredTelegram int       `gorm:"not null;default:0" json:"filtered_telegram"`
	FilteredInactive int       `gorm:"not null;default:0" json:"filtered_inactive"`
	FilteredPlatform int       `gorm:"not null;default:0" json:"filtered_platform"`
	SupportedJSON    JSON      `gorm:"type:json" json:"supported"`
	RawFansGurusJSON JSON      `gorm:"type:json" json:"raw_fansgurus"`
	RawTGXJSON       JSON      `gorm:"type:json" json:"raw_tgx"`
	ErrorMessage     string    `gorm:"type:text" json:"error_message,omitempty"`
	StartedAt        time.Time `gorm:"index" json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	CreatedAt        time.Time `gorm:"index" json:"created_at"`
}

func (ProviderCatalogSyncRun) TableName() string {
	return "provider_catalog_sync_runs"
}
