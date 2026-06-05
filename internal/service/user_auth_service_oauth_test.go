package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/telegramidentity"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupTelegramOAuthTestService(t *testing.T) (*UserAuthService, *gorm.DB) {
	t.Helper()

	dsn := fmt.Sprintf("file:user_auth_service_oauth_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.UserOAuthIdentity{}, &models.EmailVerifyCode{}, &models.Setting{}); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	cfg := &config.Config{
		UserJWT: config.JWTConfig{
			SecretKey:   "user-jwt-test-secret",
			ExpireHours: 24,
		},
	}
	settingSvc := NewSettingService(repository.NewSettingRepository(db))
	svc := NewUserAuthService(
		cfg,
		repository.NewUserRepository(db),
		repository.NewUserOAuthIdentityRepository(db),
		repository.NewEmailVerifyCodeRepository(db),
		settingSvc,
		nil,
		nil,
	)
	return svc, db
}

func TestFindOrCreateTelegramUserRespectsRegistrationSetting(t *testing.T) {
	svc, db := setupTelegramOAuthTestService(t)

	if _, err := svc.settingService.Update(constants.SettingKeyRegistrationConfig, map[string]interface{}{
		constants.SettingFieldRegistrationEnabled: false,
	}); err != nil {
		t.Fatalf("disable registration failed: %v", err)
	}

	user, err := svc.findOrCreateTelegramUser(&TelegramIdentityVerified{
		Provider:       constants.UserOAuthProviderTelegram,
		ProviderUserID: "10001",
		Username:       "tg_new_user",
		AuthAt:         time.Now(),
	})
	if !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatalf("expected ErrRegistrationDisabled, got user=%v err=%v", user, err)
	}

	var count int64
	if err := db.Model(&models.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count users failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no users created, got %d", count)
	}
}

func TestFindOrCreateTelegramUserIgnoresEmailDomainAllowlist(t *testing.T) {
	svc, db := setupTelegramOAuthTestService(t)
	if _, err := svc.settingService.Update(constants.SettingKeyRegistrationConfig, map[string]interface{}{
		constants.SettingFieldRegistrationEnabled:         true,
		constants.SettingFieldEmailDomainAllowlistEnabled: true,
		constants.SettingFieldAllowedEmailDomains:         []interface{}{"qq.com"},
	}); err != nil {
		t.Fatalf("update registration config failed: %v", err)
	}

	user, err := svc.findOrCreateTelegramUser(&TelegramIdentityVerified{
		Provider:       constants.UserOAuthProviderTelegram,
		ProviderUserID: "allowlist_tg_10001",
		Username:       "allowlist_tg",
		AuthAt:         time.Now(),
	})
	if err != nil {
		t.Fatalf("telegram user creation should ignore email domain allowlist: %v", err)
	}
	if user == nil || !telegramidentity.IsPlaceholderEmail(user.Email) {
		t.Fatalf("expected telegram placeholder email user, got %+v", user)
	}

	var count int64
	if err := db.Model(&models.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count users failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one telegram user, got %d", count)
	}
}

func TestLoginWithTelegramAllowsExistingIdentityWhenRegistrationDisabled(t *testing.T) {
	svc, db := setupTelegramOAuthTestService(t)

	now := time.Now()
	user := &models.User{
		Email:        telegramidentity.BuildPlaceholderEmail("10002"),
		PasswordHash: "telegram-auto",
		DisplayName:  "TG Existing",
		Status:       constants.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	identity := &models.UserOAuthIdentity{
		UserID:         user.ID,
		Provider:       constants.UserOAuthProviderTelegram,
		ProviderUserID: "10002",
		Username:       "tg_existing",
		AuthAt:         &now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(identity).Error; err != nil {
		t.Fatalf("create identity failed: %v", err)
	}
	if _, err := svc.settingService.Update(constants.SettingKeyRegistrationConfig, map[string]interface{}{
		constants.SettingFieldRegistrationEnabled: false,
	}); err != nil {
		t.Fatalf("disable registration failed: %v", err)
	}

	res, err := svc.loginWithVerifiedTelegram(&TelegramIdentityVerified{
		Provider:       constants.UserOAuthProviderTelegram,
		ProviderUserID: "10002",
		Username:       "tg_existing",
		AuthAt:         time.Now(),
	})
	if err != nil {
		t.Fatalf("loginWithVerifiedTelegram returned error: %v", err)
	}
	if res.User == nil || res.User.ID != user.ID {
		t.Fatalf("expected existing user %d, got %+v", user.ID, res.User)
	}
	if res.Token == "" {
		t.Fatalf("expected token")
	}
	if res.ExpiresAt.Before(time.Now()) {
		t.Fatalf("expected future expiresAt")
	}
}

func TestLoginWithTelegramMigratesOIDCSubjectIdentityToTelegramID(t *testing.T) {
	svc, db := setupTelegramOAuthTestService(t)

	now := time.Now()
	user := &models.User{
		Email:        telegramidentity.BuildPlaceholderEmail("1234123412341234123"),
		PasswordHash: "telegram-auto",
		DisplayName:  "OIDC Existing",
		Status:       constants.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	identity := &models.UserOAuthIdentity{
		UserID:         user.ID,
		Provider:       constants.UserOAuthProviderTelegram,
		ProviderUserID: "1234123412341234123",
		Username:       "old_oidc",
		AuthAt:         &now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(identity).Error; err != nil {
		t.Fatalf("create identity failed: %v", err)
	}

	res, err := svc.loginWithVerifiedTelegram(&TelegramIdentityVerified{
		Provider:              constants.UserOAuthProviderTelegram,
		ProviderUserID:        "987654321",
		ProviderUserIDAliases: []string{"1234123412341234123"},
		Username:              "new_oidc",
		AuthAt:                time.Now(),
	})
	if err != nil {
		t.Fatalf("loginWithVerifiedTelegram returned error: %v", err)
	}
	if res.User == nil || res.User.ID != user.ID {
		t.Fatalf("expected existing user %d, got %+v", user.ID, res.User)
	}

	var migrated models.UserOAuthIdentity
	if err := db.First(&migrated, identity.ID).Error; err != nil {
		t.Fatalf("load migrated identity failed: %v", err)
	}
	if migrated.ProviderUserID != "987654321" {
		t.Fatalf("provider user id not migrated: %q", migrated.ProviderUserID)
	}
	if migrated.Username != "new_oidc" {
		t.Fatalf("username not updated: %q", migrated.Username)
	}
}

func TestTelegramMiniAppLoginReturnsRegistrationDisabledWhenCreatingNewUser(t *testing.T) {
	svc, _ := setupTelegramOAuthTestService(t)
	telegramSvc := NewTelegramAuthService(config.TelegramAuthConfig{
		Enabled:            true,
		BotToken:           "test-bot-token",
		LoginExpireSeconds: 300,
		ReplayTTLSeconds:   300,
	})
	telegramSvc.replaySetNX = func(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
		return true, nil
	}
	svc.telegramAuthService = telegramSvc

	if _, err := svc.settingService.Update(constants.SettingKeyRegistrationConfig, map[string]interface{}{
		constants.SettingFieldRegistrationEnabled: false,
	}); err != nil {
		t.Fatalf("disable registration failed: %v", err)
	}

	initData := buildTestTelegramMiniAppInitData(t, "test-bot-token", time.Now().Unix(), `{"id":10003,"first_name":"Mini","last_name":"Blocked","username":"mini_blocked"}`)
	res, err := svc.LoginWithTelegramMiniApp(LoginWithTelegramMiniAppInput{
		InitData: initData,
		Context:  context.Background(),
	})
	if !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatalf("expected ErrRegistrationDisabled, got res=%+v err=%v", res, err)
	}
}

// TestLoginWithTelegramAssignsDefaultMemberLevel 回归测试：Telegram 一键登录创建的新用户
// 必须被分配默认会员等级，且不会被后续 Update(Save) 用零值覆盖（issue #197）。
func TestLoginWithTelegramAssignsDefaultMemberLevel(t *testing.T) {
	svc, db := setupTelegramOAuthTestService(t)
	if err := db.AutoMigrate(&models.MemberLevel{}); err != nil {
		t.Fatalf("auto migrate member level failed: %v", err)
	}

	now := time.Now()
	defaultLevel := &models.MemberLevel{
		NameJSON:  models.JSON{"zh-CN": "默认等级"},
		Slug:      "default",
		IsDefault: true,
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(defaultLevel).Error; err != nil {
		t.Fatalf("create default level failed: %v", err)
	}

	svc.SetMemberLevelService(NewMemberLevelService(
		repository.NewMemberLevelRepository(db),
		nil,
		repository.NewUserRepository(db),
	))

	res, err := svc.loginWithVerifiedTelegram(&TelegramIdentityVerified{
		Provider:       constants.UserOAuthProviderTelegram,
		ProviderUserID: "20001",
		Username:       "tg_level_user",
		AuthAt:         time.Now(),
	})
	if err != nil {
		t.Fatalf("loginWithVerifiedTelegram returned error: %v", err)
	}
	if res.User == nil {
		t.Fatalf("expected user, got nil")
	}
	if res.User.MemberLevelID != defaultLevel.ID {
		t.Fatalf("in-memory user member level = %d, want %d", res.User.MemberLevelID, defaultLevel.ID)
	}

	// 关键断言：数据库中的等级未被登录流程末尾的 Update 覆盖
	var persisted models.User
	if err := db.First(&persisted, res.User.ID).Error; err != nil {
		t.Fatalf("load persisted user failed: %v", err)
	}
	if persisted.MemberLevelID != defaultLevel.ID {
		t.Fatalf("persisted user member level = %d, want %d (被零值覆盖)", persisted.MemberLevelID, defaultLevel.ID)
	}
}
