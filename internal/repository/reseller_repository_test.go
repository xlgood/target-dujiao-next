package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openResellerRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:reseller_repo_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.ResellerProfile{}, &models.ResellerDomain{}, &models.ResellerSiteConfig{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_reseller_domains_active_domain ON reseller_domains(domain) WHERE deleted_at IS NULL").Error; err != nil {
		t.Fatalf("create domain index failed: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_reseller_site_configs_active_reseller ON reseller_site_configs(reseller_id) WHERE deleted_at IS NULL").Error; err != nil {
		t.Fatalf("create site config index failed: %v", err)
	}
	return db
}

func seedResellerProfile(t *testing.T, db *gorm.DB, email string) models.ResellerProfile {
	t.Helper()
	user := models.User{Email: email, PasswordHash: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	profile := models.ResellerProfile{UserID: user.ID, Status: models.ResellerProfileStatusActive, SettlementStatus: models.ResellerSettlementStatusNormal}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile failed: %v", err)
	}
	return profile
}

func TestResellerRepositoryUpsertDomainRestoresSoftDeleted(t *testing.T) {
	db := openResellerRepoTestDB(t)
	profile := seedResellerProfile(t, db, "owner@example.com")
	repo := NewResellerRepository(db)
	first, err := repo.UpsertDomain(models.ResellerDomain{
		ResellerID:         profile.ID,
		Domain:             "shop.example.test",
		Type:               models.ResellerDomainTypeCustom,
		Status:             models.ResellerDomainStatusActive,
		VerificationStatus: models.ResellerDomainVerificationVerified,
	})
	if err != nil {
		t.Fatalf("create domain failed: %v", err)
	}
	if err := db.Delete(first).Error; err != nil {
		t.Fatalf("soft delete domain failed: %v", err)
	}
	second, err := repo.UpsertDomain(models.ResellerDomain{
		ResellerID:         profile.ID,
		Domain:             "shop.example.test",
		Type:               models.ResellerDomainTypeCustom,
		Status:             models.ResellerDomainStatusDisabled,
		VerificationStatus: models.ResellerDomainVerificationPending,
	})
	if err != nil {
		t.Fatalf("restore domain failed: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected restore same row id=%d got id=%d", first.ID, second.ID)
	}
	if second.DeletedAt.Valid {
		t.Fatal("expected restored domain deleted_at cleared")
	}
	if second.Status != models.ResellerDomainStatusDisabled {
		t.Fatalf("expected restored status disabled, got %s", second.Status)
	}
}

func TestResellerRepositoryFindActiveVerifiedDomain(t *testing.T) {
	db := openResellerRepoTestDB(t)
	profile := seedResellerProfile(t, db, "owner2@example.com")
	inactiveProfile := seedResellerProfile(t, db, "disabled-owner@example.com")
	if err := db.Model(&inactiveProfile).Update("status", models.ResellerProfileStatusDisabled).Error; err != nil {
		t.Fatalf("disable profile failed: %v", err)
	}
	repo := NewResellerRepository(db)
	if _, err := repo.UpsertDomain(models.ResellerDomain{
		ResellerID:         profile.ID,
		Domain:             "inactive.example.test",
		Type:               models.ResellerDomainTypeCustom,
		Status:             models.ResellerDomainStatusDisabled,
		VerificationStatus: models.ResellerDomainVerificationVerified,
	}); err != nil {
		t.Fatalf("create disabled domain failed: %v", err)
	}
	if _, err := repo.UpsertDomain(models.ResellerDomain{
		ResellerID:         profile.ID,
		Domain:             "active.example.test",
		Type:               models.ResellerDomainTypeCustom,
		Status:             models.ResellerDomainStatusActive,
		VerificationStatus: models.ResellerDomainVerificationVerified,
		IsPrimary:          true,
	}); err != nil {
		t.Fatalf("create active domain failed: %v", err)
	}
	if _, err := repo.UpsertDomain(models.ResellerDomain{
		ResellerID:         inactiveProfile.ID,
		Domain:             "disabled-profile.example.test",
		Type:               models.ResellerDomainTypeCustom,
		Status:             models.ResellerDomainStatusActive,
		VerificationStatus: models.ResellerDomainVerificationVerified,
	}); err != nil {
		t.Fatalf("create disabled profile domain failed: %v", err)
	}
	disabled, err := repo.FindActiveVerifiedDomain("inactive.example.test")
	if err != nil {
		t.Fatalf("lookup disabled failed: %v", err)
	}
	if disabled != nil {
		t.Fatalf("disabled domain should not resolve: %+v", disabled)
	}
	inactiveProfileDomain, err := repo.FindActiveVerifiedDomain("disabled-profile.example.test")
	if err != nil {
		t.Fatalf("lookup disabled profile domain failed: %v", err)
	}
	if inactiveProfileDomain != nil {
		t.Fatalf("active domain for disabled profile should not resolve: %+v", inactiveProfileDomain)
	}
	active, err := repo.FindActiveVerifiedDomain("active.example.test")
	if err != nil {
		t.Fatalf("lookup active failed: %v", err)
	}
	if active == nil || active.Profile == nil {
		t.Fatalf("expected active domain with profile, got %+v", active)
	}
	if active.Profile.UserID != profile.UserID {
		t.Fatalf("profile user mismatch want %d got %d", profile.UserID, active.Profile.UserID)
	}
}

func TestResellerRepositoryListProfilesFiltersAndPreloadsUser(t *testing.T) {
	db := openResellerRepoTestDB(t)
	repo := NewResellerRepository(db)
	user := models.User{Email: "reseller-list@example.test", PasswordHash: "hash", DisplayName: "Reseller List"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	other := models.User{Email: "other-reseller-list@example.test", PasswordHash: "hash", DisplayName: "Other"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user failed: %v", err)
	}
	profile := models.ResellerProfile{UserID: user.ID, Status: models.ResellerProfileStatusPendingReview, ApplyReason: "list me", SettlementStatus: models.ResellerSettlementStatusNormal}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile failed: %v", err)
	}
	if err := db.Create(&models.ResellerProfile{UserID: other.ID, Status: models.ResellerProfileStatusActive, SettlementStatus: models.ResellerSettlementStatusNormal}).Error; err != nil {
		t.Fatalf("create other profile failed: %v", err)
	}

	rows, total, err := repo.ListProfiles(ResellerProfileListFilter{
		Page: 1, PageSize: 20, Status: models.ResellerProfileStatusPendingReview, Keyword: "reseller-list@example.test",
	})
	if err != nil {
		t.Fatalf("ListProfiles failed: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != profile.ID {
		t.Fatalf("expected one filtered profile, total=%d rows=%+v", total, rows)
	}
	if rows[0].User == nil || rows[0].User.Email != user.Email {
		t.Fatalf("expected user preload, got %+v", rows[0].User)
	}
}

func TestResellerRepositoryListDomainsFiltersAndPreloadsProfileUser(t *testing.T) {
	db := openResellerRepoTestDB(t)
	repo := NewResellerRepository(db)
	user := models.User{Email: "domain-owner@example.test", PasswordHash: "hash", DisplayName: "Domain Owner"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	profile := models.ResellerProfile{UserID: user.ID, Status: models.ResellerProfileStatusActive, SettlementStatus: models.ResellerSettlementStatusNormal}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile failed: %v", err)
	}
	domain := models.ResellerDomain{
		ResellerID: profile.ID, Domain: "shop.example.test", Type: models.ResellerDomainTypeCustom,
		VerificationStatus: models.ResellerDomainVerificationPending, Status: models.ResellerDomainStatusPendingReview,
	}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("create domain failed: %v", err)
	}

	rows, total, err := repo.ListDomains(ResellerDomainListFilter{
		Page: 1, PageSize: 20, ResellerID: profile.ID, Domain: "shop.example.test", Type: models.ResellerDomainTypeCustom,
	})
	if err != nil {
		t.Fatalf("ListDomains failed: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != domain.ID {
		t.Fatalf("expected one filtered domain, total=%d rows=%+v", total, rows)
	}
	if rows[0].Profile == nil || rows[0].Profile.User == nil || rows[0].Profile.User.Email != user.Email {
		t.Fatalf("expected profile and user preload, got %+v", rows[0].Profile)
	}
}

func TestResellerRepositorySiteConfigLifecycle(t *testing.T) {
	db := openResellerRepoTestDB(t)
	repo := NewResellerRepository(db)
	profile := seedResellerProfile(t, db, "site-config@example.test")

	initial, err := repo.GetSiteConfigByResellerID(profile.ID)
	if err != nil {
		t.Fatalf("get empty site config failed: %v", err)
	}
	if initial != nil {
		t.Fatalf("expected nil config, got %+v", initial)
	}

	saved, err := repo.UpsertSiteConfig(models.ResellerSiteConfig{
		ResellerID: profile.ID,
		SiteName:   "Alice Store",
		Logo:       "/uploads/reseller/logo.png",
		SupportJSON: models.JSON{
			"telegram": "https://t.me/alice",
		},
	})
	if err != nil {
		t.Fatalf("upsert site config failed: %v", err)
	}
	if saved.ID == 0 || saved.SiteName != "Alice Store" {
		t.Fatalf("unexpected saved config: %+v", saved)
	}

	loaded, err := repo.GetSiteConfigByResellerID(profile.ID)
	if err != nil {
		t.Fatalf("get site config failed: %v", err)
	}
	if loaded == nil || loaded.ID != saved.ID || loaded.SupportJSON["telegram"] != "https://t.me/alice" {
		t.Fatalf("unexpected loaded config: %+v", loaded)
	}

	if err := repo.DeleteSiteConfigByResellerID(profile.ID); err != nil {
		t.Fatalf("delete site config failed: %v", err)
	}
	afterDelete, err := repo.GetSiteConfigByResellerID(profile.ID)
	if err != nil {
		t.Fatalf("get deleted site config failed: %v", err)
	}
	if afterDelete != nil {
		t.Fatalf("expected deleted config to be hidden, got %+v", afterDelete)
	}

	restored, err := repo.UpsertSiteConfig(models.ResellerSiteConfig{
		ResellerID: profile.ID,
		SiteName:   "Alice Restored",
	})
	if err != nil {
		t.Fatalf("restore site config failed: %v", err)
	}
	if restored.ID != saved.ID || restored.SiteName != "Alice Restored" {
		t.Fatalf("expected soft-deleted row restored, got saved=%d restored=%+v", saved.ID, restored)
	}
}

func TestResellerRepositoryListSiteConfigsFiltersAndPreloadsProfileUser(t *testing.T) {
	db := openResellerRepoTestDB(t)
	repo := NewResellerRepository(db)
	alice := seedResellerProfile(t, db, "alice-site@example.test")
	bob := seedResellerProfile(t, db, "bob-site@example.test")
	if _, err := repo.UpsertSiteConfig(models.ResellerSiteConfig{ResellerID: alice.ID, SiteName: "Alice Store"}); err != nil {
		t.Fatalf("create alice config failed: %v", err)
	}
	if _, err := repo.UpsertSiteConfig(models.ResellerSiteConfig{ResellerID: bob.ID, SiteName: "Bob Store"}); err != nil {
		t.Fatalf("create bob config failed: %v", err)
	}

	rows, total, err := repo.ListSiteConfigs(ResellerSiteConfigListFilter{
		Keyword:  "alice",
		Page:     1,
		PageSize: 20,
	})
	if err != nil {
		t.Fatalf("list site configs failed: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("expected one row, got total=%d rows=%d", total, len(rows))
	}
	if rows[0].ResellerID != alice.ID || rows[0].Profile == nil || rows[0].Profile.User == nil {
		t.Fatalf("expected profile and user preload, got %+v", rows[0])
	}
}

func TestResellerRepositoryUpdateProfileAndDomain(t *testing.T) {
	db := openResellerRepoTestDB(t)
	repo := NewResellerRepository(db)
	user := models.User{Email: "update-reseller@example.test", PasswordHash: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	profile := models.ResellerProfile{UserID: user.ID, Status: models.ResellerProfileStatusPendingReview, SettlementStatus: models.ResellerSettlementStatusNormal}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile failed: %v", err)
	}
	reviewer := uint(99)
	now := time.Now().UTC()
	profile.Status = models.ResellerProfileStatusActive
	profile.ReviewedBy = &reviewer
	profile.ReviewedAt = &now
	if err := repo.UpdateProfile(&profile); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}
	loaded, err := repo.GetProfileByID(profile.ID)
	if err != nil {
		t.Fatalf("GetProfileByID failed: %v", err)
	}
	if loaded == nil || loaded.Status != models.ResellerProfileStatusActive || loaded.ReviewedBy == nil || *loaded.ReviewedBy != reviewer {
		t.Fatalf("profile was not updated: %+v", loaded)
	}

	domain := models.ResellerDomain{ResellerID: profile.ID, Domain: "r1.example.test", Type: models.ResellerDomainTypeSubdomain, VerificationStatus: models.ResellerDomainVerificationPending, Status: models.ResellerDomainStatusPendingReview}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("create domain failed: %v", err)
	}
	domain.Status = models.ResellerDomainStatusActive
	domain.VerificationStatus = models.ResellerDomainVerificationVerified
	domain.VerifiedAt = &now
	if err := repo.UpdateDomain(&domain); err != nil {
		t.Fatalf("UpdateDomain failed: %v", err)
	}
	loadedDomain, err := repo.GetDomainByID(domain.ID)
	if err != nil {
		t.Fatalf("GetDomainByID failed: %v", err)
	}
	if loadedDomain == nil || loadedDomain.Status != models.ResellerDomainStatusActive || loadedDomain.VerificationStatus != models.ResellerDomainVerificationVerified {
		t.Fatalf("domain was not updated: %+v", loadedDomain)
	}
}
