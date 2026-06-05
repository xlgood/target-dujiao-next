package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/telegramidentity"

	"golang.org/x/crypto/bcrypt"
)

// LoginWithTelegramInput Telegram 登录输入
type LoginWithTelegramInput struct {
	Payload TelegramLoginPayload
	Context context.Context
}

// LoginWithTelegramMiniAppInput Telegram Mini App 登录输入
type LoginWithTelegramMiniAppInput struct {
	InitData string
	Context  context.Context
}

// BindTelegramInput 绑定 Telegram 输入
type BindTelegramInput struct {
	UserID  uint
	Payload TelegramLoginPayload
	Context context.Context
}

// BindTelegramMiniAppInput Telegram Mini App 绑定输入
type BindTelegramMiniAppInput struct {
	UserID   uint
	InitData string
	Context  context.Context
}

// TelegramChannelIdentityInput Telegram 渠道身份输入
type TelegramChannelIdentityInput struct {
	ChannelUserID string
	Username      string
	FirstName     string
	LastName      string
	AvatarURL     string
}

// BindTelegramChannelByEmailCodeInput Telegram 渠道邮箱验证码绑定输入
type BindTelegramChannelByEmailCodeInput struct {
	Identity TelegramChannelIdentityInput
	Email    string
	Code     string
}

// LoginWithTelegram Telegram 登录（已启用 2FA 的账号会返回挑战 token，不直接发 JWT）
func (s *UserAuthService) LoginWithTelegram(input LoginWithTelegramInput) (*UserLoginResult, error) {
	if s.telegramAuthService == nil || s.userOAuthIdentityRepo == nil {
		return nil, ErrTelegramAuthConfigInvalid
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	verified, err := s.telegramAuthService.VerifyLogin(ctx, input.Payload)
	if err != nil {
		return nil, err
	}
	return s.loginWithVerifiedTelegram(verified)
}

// LoginWithTelegramMiniApp Telegram Mini App 登录（已启用 2FA 的账号会返回挑战 token，不直接发 JWT）
func (s *UserAuthService) LoginWithTelegramMiniApp(input LoginWithTelegramMiniAppInput) (*UserLoginResult, error) {
	if s.telegramAuthService == nil || s.userOAuthIdentityRepo == nil {
		return nil, ErrTelegramAuthConfigInvalid
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	verified, err := s.telegramAuthService.VerifyMiniAppInitData(ctx, input.InitData)
	if err != nil {
		return nil, err
	}
	return s.loginWithVerifiedTelegram(verified)
}

// StartTelegramOIDCInput 启动 Telegram OIDC 流程输入
type StartTelegramOIDCInput struct {
	Intent  string // "login" | "bind"
	UserID  uint
	Context context.Context
}

// LoginWithTelegramOIDCInput Telegram OIDC 登录输入
type LoginWithTelegramOIDCInput struct {
	Code    string
	State   string
	Context context.Context
}

// BindTelegramOIDCInput Telegram OIDC 绑定输入
type BindTelegramOIDCInput struct {
	UserID  uint
	Code    string
	State   string
	Context context.Context
}

// StartTelegramOIDC 生成 Telegram OIDC 授权 URL
func (s *UserAuthService) StartTelegramOIDC(input StartTelegramOIDCInput) (string, error) {
	if s.telegramAuthService == nil {
		return "", ErrTelegramAuthConfigInvalid
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	intent := input.Intent
	if intent != telegramOIDCIntentLogin && intent != telegramOIDCIntentBind {
		return "", ErrTelegramAuthPayloadInvalid
	}
	return s.telegramAuthService.StartOIDCLogin(ctx, intent, input.UserID)
}

// LoginWithTelegramOIDC 通过 Telegram OIDC 回调登录
func (s *UserAuthService) LoginWithTelegramOIDC(input LoginWithTelegramOIDCInput) (*UserLoginResult, error) {
	if s.telegramAuthService == nil || s.userOAuthIdentityRepo == nil {
		return nil, ErrTelegramAuthConfigInvalid
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	verified, intent, _, err := s.telegramAuthService.CompleteOIDCLogin(ctx, input.Code, input.State)
	if err != nil {
		return nil, err
	}
	if intent != telegramOIDCIntentLogin {
		return nil, ErrTelegramAuthPayloadInvalid
	}
	return s.loginWithVerifiedTelegram(verified)
}

// BindTelegramOIDC 通过 Telegram OIDC 回调绑定当前用户
func (s *UserAuthService) BindTelegramOIDC(input BindTelegramOIDCInput) (*models.UserOAuthIdentity, error) {
	if input.UserID == 0 {
		return nil, ErrNotFound
	}
	if s.telegramAuthService == nil || s.userOAuthIdentityRepo == nil {
		return nil, ErrTelegramAuthConfigInvalid
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	verified, intent, userID, err := s.telegramAuthService.CompleteOIDCLogin(ctx, input.Code, input.State)
	if err != nil {
		return nil, err
	}
	if intent != telegramOIDCIntentBind || userID != input.UserID {
		return nil, ErrTelegramAuthPayloadInvalid
	}
	return s.bindVerifiedTelegram(input.UserID, verified)
}

func (s *UserAuthService) loginWithVerifiedTelegram(verified *TelegramIdentityVerified) (*UserLoginResult, error) {
	identity, err := s.getTelegramIdentityByVerifiedID(verified)
	if err != nil {
		return nil, err
	}

	var user *models.User
	if identity != nil {
		user, err = s.getActiveUserByID(identity.UserID)
		if err != nil {
			return nil, err
		}
		identityChanged, err := s.canonicalizeTelegramProviderUserID(verified, identity)
		if err != nil {
			return nil, err
		}
		identityChanged = applyTelegramIdentity(verified, identity) || identityChanged
		if identityChanged {
			identity.UpdatedAt = time.Now()
			if err := s.userOAuthIdentityRepo.Update(identity); err != nil {
				return nil, err
			}
		}
	} else {
		user, err = s.findOrCreateTelegramUser(verified)
		if err != nil {
			return nil, err
		}
		identity = &models.UserOAuthIdentity{
			UserID:         user.ID,
			Provider:       verified.Provider,
			ProviderUserID: verified.ProviderUserID,
			Username:       verified.Username,
			AvatarURL:      verified.AvatarURL,
			AuthAt:         &verified.AuthAt,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}
		if err := s.userOAuthIdentityRepo.Create(identity); err != nil {
			existing, getErr := s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, verified.ProviderUserID)
			if getErr != nil {
				return nil, err
			}
			if existing == nil {
				return nil, err
			}
			identity = existing
			user, err = s.getActiveUserByID(existing.UserID)
			if err != nil {
				return nil, err
			}
		}
	}

	// 已启用 2FA → 仅签发挑战 token
	if user.TOTPEnabledAt != nil {
		challenge, jti, expiresAt, err := s.IssueUserChallengeToken(user.ID, false)
		if err != nil {
			return nil, err
		}
		return &UserLoginResult{
			RequiresTOTP:       true,
			User:               user,
			ChallengeToken:     challenge,
			ChallengeJTI:       jti,
			ChallengeExpiresAt: expiresAt,
		}, nil
	}

	token, expiresAt, err := s.GenerateUserJWT(user, 0)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	user.LastLoginAt = &now
	user.UpdatedAt = now
	if err := s.userRepo.Update(user); err != nil {
		return nil, err
	}
	_ = cache.SetUserAuthState(context.Background(), cache.BuildUserAuthState(user))
	return &UserLoginResult{
		RequiresTOTP: false,
		User:         user,
		Token:        token,
		ExpiresAt:    expiresAt,
	}, nil
}

// BindTelegram 绑定 Telegram
func (s *UserAuthService) BindTelegram(input BindTelegramInput) (*models.UserOAuthIdentity, error) {
	if input.UserID == 0 {
		return nil, ErrNotFound
	}
	if s.telegramAuthService == nil || s.userOAuthIdentityRepo == nil {
		return nil, ErrTelegramAuthConfigInvalid
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	verified, err := s.telegramAuthService.VerifyLogin(ctx, input.Payload)
	if err != nil {
		return nil, err
	}
	return s.bindVerifiedTelegram(input.UserID, verified)
}

// BindTelegramMiniApp 绑定当前用户的 Telegram Mini App 身份
func (s *UserAuthService) BindTelegramMiniApp(input BindTelegramMiniAppInput) (*models.UserOAuthIdentity, error) {
	if input.UserID == 0 {
		return nil, ErrNotFound
	}
	if s.telegramAuthService == nil || s.userOAuthIdentityRepo == nil {
		return nil, ErrTelegramAuthConfigInvalid
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	verified, err := s.telegramAuthService.VerifyMiniAppInitData(ctx, input.InitData)
	if err != nil {
		return nil, err
	}
	return s.bindVerifiedTelegram(input.UserID, verified)
}

func (s *UserAuthService) bindVerifiedTelegram(userID uint, verified *TelegramIdentityVerified) (*models.UserOAuthIdentity, error) {
	if _, err := s.getActiveUserByID(userID); err != nil {
		return nil, err
	}

	occupied, err := s.getTelegramIdentityByVerifiedID(verified)
	if err != nil {
		return nil, err
	}
	if occupied != nil && occupied.UserID != userID {
		return nil, ErrUserOAuthIdentityExists
	}

	current, err := s.userOAuthIdentityRepo.GetByUserProvider(userID, verified.Provider)
	if err != nil {
		return nil, err
	}
	if current != nil && !telegramProviderUserIDMatchesVerified(current.ProviderUserID, verified) {
		return nil, ErrUserOAuthAlreadyBound
	}
	if current == nil {
		current = &models.UserOAuthIdentity{
			UserID:         userID,
			Provider:       verified.Provider,
			ProviderUserID: verified.ProviderUserID,
			Username:       verified.Username,
			AvatarURL:      verified.AvatarURL,
			AuthAt:         &verified.AuthAt,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}
		if err := s.userOAuthIdentityRepo.Create(current); err != nil {
			return nil, err
		}
		return current, nil
	}

	identityChanged, err := s.canonicalizeTelegramProviderUserID(verified, current)
	if err != nil {
		return nil, err
	}
	if applyTelegramIdentity(verified, current) || identityChanged {
		current.UpdatedAt = time.Now()
		if err := s.userOAuthIdentityRepo.Update(current); err != nil {
			return nil, err
		}
	}
	return current, nil
}

// UnbindTelegram 解绑 Telegram
func (s *UserAuthService) UnbindTelegram(userID uint) error {
	if userID == 0 {
		return ErrNotFound
	}
	if s.userOAuthIdentityRepo == nil {
		return ErrTelegramAuthConfigInvalid
	}
	user, err := s.getActiveUserByID(userID)
	if err != nil {
		return err
	}
	mode, err := s.ResolveEmailChangeMode(user)
	if err != nil {
		return err
	}
	if mode == EmailChangeModeBindOnly {
		return ErrTelegramUnbindRequiresEmail
	}
	identity, err := s.userOAuthIdentityRepo.GetByUserProvider(userID, constants.UserOAuthProviderTelegram)
	if err != nil {
		return err
	}
	if identity == nil {
		return ErrUserOAuthNotBound
	}
	return s.userOAuthIdentityRepo.DeleteByID(identity.ID)
}

// GetTelegramBinding 获取 Telegram 绑定
func (s *UserAuthService) GetTelegramBinding(userID uint) (*models.UserOAuthIdentity, error) {
	if userID == 0 {
		return nil, ErrNotFound
	}
	if s.userOAuthIdentityRepo == nil {
		return nil, ErrTelegramAuthConfigInvalid
	}
	return s.userOAuthIdentityRepo.GetByUserProvider(userID, constants.UserOAuthProviderTelegram)
}

// ResolveTelegramChannelIdentity 解析 Telegram 渠道身份
func (s *UserAuthService) ResolveTelegramChannelIdentity(input TelegramChannelIdentityInput) (*models.User, *models.UserOAuthIdentity, error) {
	verified, err := normalizeTelegramChannelIdentityInput(input)
	if err != nil {
		return nil, nil, err
	}
	return s.resolveTelegramChannelIdentity(verified)
}

// ProvisionTelegramChannelIdentity 预置 Telegram 渠道身份
func (s *UserAuthService) ProvisionTelegramChannelIdentity(input TelegramChannelIdentityInput) (*models.User, *models.UserOAuthIdentity, bool, error) {
	verified, err := normalizeTelegramChannelIdentityInput(input)
	if err != nil {
		return nil, nil, false, err
	}
	return s.provisionTelegramChannelIdentity(verified)
}

// BindTelegramChannelByEmailCode 使用邮箱验证码绑定 Telegram 渠道身份到既有账号
func (s *UserAuthService) BindTelegramChannelByEmailCode(input BindTelegramChannelByEmailCodeInput) (*models.User, *models.UserOAuthIdentity, uint, error) {
	verified, err := normalizeTelegramChannelIdentityInput(input.Identity)
	if err != nil {
		return nil, nil, 0, err
	}
	if s.userOAuthIdentityRepo == nil || s.userRepo == nil || s.codeRepo == nil {
		return nil, nil, 0, ErrTelegramAuthConfigInvalid
	}

	email, err := normalizeEmail(input.Email)
	if err != nil {
		return nil, nil, 0, err
	}
	if _, err := s.verifyCode(email, constants.VerifyPurposeTelegramBind, input.Code); err != nil {
		return nil, nil, 0, err
	}

	targetUser, err := s.userRepo.GetByEmail(email)
	if err != nil {
		return nil, nil, 0, err
	}
	if targetUser == nil {
		return nil, nil, 0, ErrNotFound
	}
	if strings.ToLower(strings.TrimSpace(targetUser.Status)) != constants.UserStatusActive {
		return nil, nil, 0, ErrUserDisabled
	}

	return s.bindTelegramIdentityToUser(targetUser, verified)
}

func (s *UserAuthService) resolveTelegramChannelIdentity(verified *TelegramIdentityVerified) (*models.User, *models.UserOAuthIdentity, error) {
	if verified == nil {
		return nil, nil, ErrTelegramAuthPayloadInvalid
	}
	if s.userOAuthIdentityRepo == nil || s.userRepo == nil {
		return nil, nil, ErrTelegramAuthConfigInvalid
	}

	identity, err := s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, verified.ProviderUserID)
	if err != nil {
		return nil, nil, err
	}
	if identity == nil {
		return nil, nil, nil
	}

	user, err := s.getActiveUserByID(identity.UserID)
	if err != nil {
		return nil, nil, err
	}
	if applyTelegramIdentity(verified, identity) {
		identity.UpdatedAt = time.Now()
		if err := s.userOAuthIdentityRepo.Update(identity); err != nil {
			return nil, nil, err
		}
	}
	return user, identity, nil
}

func (s *UserAuthService) provisionTelegramChannelIdentity(verified *TelegramIdentityVerified) (*models.User, *models.UserOAuthIdentity, bool, error) {
	if verified == nil {
		return nil, nil, false, ErrTelegramAuthPayloadInvalid
	}
	if s.userOAuthIdentityRepo == nil || s.userRepo == nil {
		return nil, nil, false, ErrTelegramAuthConfigInvalid
	}

	user, identity, err := s.resolveTelegramChannelIdentity(verified)
	if err != nil {
		return nil, nil, false, err
	}
	if identity != nil {
		return user, identity, false, nil
	}

	placeholderUser, err := s.userRepo.GetByEmail(telegramidentity.BuildPlaceholderEmail(verified.ProviderUserID))
	if err != nil {
		return nil, nil, false, err
	}
	created := placeholderUser == nil

	user, err = s.findOrCreateTelegramUser(verified)
	if err != nil {
		return nil, nil, false, err
	}

	identity, err = s.userOAuthIdentityRepo.GetByUserProvider(user.ID, verified.Provider)
	if err != nil {
		return nil, nil, false, err
	}
	if identity != nil {
		if identity.ProviderUserID != verified.ProviderUserID {
			return nil, nil, false, ErrUserOAuthAlreadyBound
		}
		if applyTelegramIdentity(verified, identity) {
			identity.UpdatedAt = time.Now()
			if err := s.userOAuthIdentityRepo.Update(identity); err != nil {
				return nil, nil, false, err
			}
		}
		return user, identity, created, nil
	}

	identity = &models.UserOAuthIdentity{
		UserID:         user.ID,
		Provider:       verified.Provider,
		ProviderUserID: verified.ProviderUserID,
		Username:       verified.Username,
		AvatarURL:      verified.AvatarURL,
		AuthAt:         &verified.AuthAt,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := s.userOAuthIdentityRepo.Create(identity); err != nil {
		existing, getErr := s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, verified.ProviderUserID)
		if getErr != nil {
			return nil, nil, false, err
		}
		if existing == nil {
			return nil, nil, false, err
		}
		identity = existing
		user, err = s.getActiveUserByID(existing.UserID)
		if err != nil {
			return nil, nil, false, err
		}
		return user, identity, false, nil
	}

	return user, identity, created, nil
}

func (s *UserAuthService) bindTelegramIdentityToUser(targetUser *models.User, verified *TelegramIdentityVerified) (*models.User, *models.UserOAuthIdentity, uint, error) {
	if targetUser == nil || verified == nil {
		return nil, nil, 0, ErrNotFound
	}
	if s.userOAuthIdentityRepo == nil {
		return nil, nil, 0, ErrTelegramAuthConfigInvalid
	}

	current, err := s.userOAuthIdentityRepo.GetByUserProvider(targetUser.ID, verified.Provider)
	if err != nil {
		return nil, nil, 0, err
	}
	if current != nil && current.ProviderUserID != verified.ProviderUserID {
		return nil, nil, 0, ErrUserOAuthAlreadyBound
	}

	occupied, err := s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, verified.ProviderUserID)
	if err != nil {
		return nil, nil, 0, err
	}
	if occupied != nil && occupied.UserID == targetUser.ID {
		if applyTelegramIdentity(verified, occupied) {
			occupied.UpdatedAt = time.Now()
			if err := s.userOAuthIdentityRepo.Update(occupied); err != nil {
				return nil, nil, 0, err
			}
		}
		return targetUser, occupied, 0, nil
	}

	if occupied != nil {
		previousUser, err := s.userRepo.GetByID(occupied.UserID)
		if err != nil {
			return nil, nil, 0, err
		}
		if previousUser == nil || !telegramidentity.IsPlaceholderEmail(previousUser.Email) {
			return nil, nil, 0, ErrUserOAuthIdentityExists
		}

		previousUserID := occupied.UserID
		occupied.UserID = targetUser.ID
		applyTelegramIdentity(verified, occupied)
		occupied.UpdatedAt = time.Now()
		if err := s.userOAuthIdentityRepo.Update(occupied); err != nil {
			return nil, nil, 0, err
		}
		return targetUser, occupied, previousUserID, nil
	}

	identity := &models.UserOAuthIdentity{
		UserID:         targetUser.ID,
		Provider:       verified.Provider,
		ProviderUserID: verified.ProviderUserID,
		Username:       verified.Username,
		AvatarURL:      verified.AvatarURL,
		AuthAt:         &verified.AuthAt,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := s.userOAuthIdentityRepo.Create(identity); err != nil {
		return nil, nil, 0, err
	}
	return targetUser, identity, 0, nil
}

func (s *UserAuthService) getActiveUserByID(userID uint) (*models.User, error) {
	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNotFound
	}
	if strings.ToLower(strings.TrimSpace(user.Status)) != constants.UserStatusActive {
		return nil, ErrUserDisabled
	}
	return user, nil
}

func (s *UserAuthService) findOrCreateTelegramUser(verified *TelegramIdentityVerified) (*models.User, error) {
	if verified == nil {
		return nil, ErrTelegramAuthPayloadInvalid
	}
	email := telegramidentity.BuildPlaceholderEmail(verified.ProviderUserID)
	user, err := s.userRepo.GetByEmail(email)
	if err != nil {
		return nil, err
	}
	if user != nil {
		if strings.ToLower(strings.TrimSpace(user.Status)) != constants.UserStatusActive {
			return nil, ErrUserDisabled
		}
		return user, nil
	}
	if s.settingService != nil {
		registrationEnabled, err := s.settingService.GetRegistrationEnabled(true)
		if err != nil {
			return nil, err
		}
		if !registrationEnabled {
			return nil, ErrRegistrationDisabled
		}
	}

	randomSuffix, err := randomNumericCode(16)
	if err != nil {
		return nil, err
	}
	passwordSeed := fmt.Sprintf("tg_%s_%s", verified.ProviderUserID, randomSuffix)
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(passwordSeed), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	user = &models.User{
		Email:                 email,
		PasswordHash:          string(hashedPassword),
		PasswordSetupRequired: true,
		DisplayName:           telegramidentity.ResolveDisplayName(verified.ProviderUserID, verified.Username, verified.FirstName, verified.LastName),
		Status:                constants.UserStatusActive,
		LastLoginAt:           &now,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := s.userRepo.Create(user); err != nil {
		return nil, err
	}
	// 分配默认会员等级
	if s.memberLevelSvc != nil {
		_ = s.memberLevelSvc.AssignDefaultLevel(user.ID)
		// 同步内存对象的等级，避免调用方后续 Update(Save) 用零值覆盖数据库
		if refreshed, err := s.userRepo.GetByID(user.ID); err == nil && refreshed != nil {
			user.MemberLevelID = refreshed.MemberLevelID
		}
	}
	return user, nil
}

// getTelegramIdentityByVerifiedID 按 Telegram 数字 ID 查询绑定，未命中时兼容历史 OIDC subject 绑定。
func (s *UserAuthService) getTelegramIdentityByVerifiedID(verified *TelegramIdentityVerified) (*models.UserOAuthIdentity, error) {
	if verified == nil || s.userOAuthIdentityRepo == nil {
		return nil, ErrTelegramAuthConfigInvalid
	}
	identity, err := s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, verified.ProviderUserID)
	if err != nil || identity != nil {
		return identity, err
	}
	for _, alias := range verified.ProviderUserIDAliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || alias == verified.ProviderUserID {
			continue
		}
		identity, err = s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, alias)
		if err != nil || identity != nil {
			return identity, err
		}
	}
	return nil, nil
}

// canonicalizeTelegramProviderUserID 将历史 OIDC subject 绑定迁移为 Telegram 数字用户 ID。
func (s *UserAuthService) canonicalizeTelegramProviderUserID(verified *TelegramIdentityVerified, identity *models.UserOAuthIdentity) (bool, error) {
	if verified == nil || identity == nil || identity.ProviderUserID == verified.ProviderUserID {
		return false, nil
	}
	occupied, err := s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, verified.ProviderUserID)
	if err != nil {
		return false, err
	}
	if occupied != nil && occupied.ID != identity.ID {
		return false, ErrUserOAuthIdentityExists
	}
	identity.ProviderUserID = verified.ProviderUserID
	return true, nil
}

// telegramProviderUserIDMatchesVerified 判断绑定 ID 是否匹配当前 Telegram 身份或其历史别名。
func telegramProviderUserIDMatchesVerified(providerUserID string, verified *TelegramIdentityVerified) bool {
	providerUserID = strings.TrimSpace(providerUserID)
	if verified == nil || providerUserID == "" {
		return false
	}
	if providerUserID == verified.ProviderUserID {
		return true
	}
	for _, alias := range verified.ProviderUserIDAliases {
		if providerUserID == strings.TrimSpace(alias) {
			return true
		}
	}
	return false
}

func applyTelegramIdentity(verified *TelegramIdentityVerified, identity *models.UserOAuthIdentity) bool {
	if verified == nil || identity == nil {
		return false
	}
	changed := false
	if identity.Provider == "" {
		identity.Provider = verified.Provider
		changed = true
	}
	if identity.ProviderUserID == "" {
		identity.ProviderUserID = verified.ProviderUserID
		changed = true
	}
	if identity.Username != verified.Username {
		identity.Username = verified.Username
		changed = true
	}
	if identity.AvatarURL != verified.AvatarURL {
		identity.AvatarURL = verified.AvatarURL
		changed = true
	}
	if identity.AuthAt == nil || !identity.AuthAt.Equal(verified.AuthAt) {
		authAt := verified.AuthAt
		identity.AuthAt = &authAt
		changed = true
	}
	return changed
}

func normalizeTelegramChannelIdentityInput(input TelegramChannelIdentityInput) (*TelegramIdentityVerified, error) {
	providerUserID := strings.TrimSpace(input.ChannelUserID)
	if providerUserID == "" {
		return nil, ErrTelegramAuthPayloadInvalid
	}
	return &TelegramIdentityVerified{
		Provider:       constants.UserOAuthProviderTelegram,
		ProviderUserID: providerUserID,
		Username:       strings.TrimSpace(input.Username),
		AvatarURL:      strings.TrimSpace(input.AvatarURL),
		FirstName:      strings.TrimSpace(input.FirstName),
		LastName:       strings.TrimSpace(input.LastName),
		AuthAt:         time.Now(),
	}, nil
}
