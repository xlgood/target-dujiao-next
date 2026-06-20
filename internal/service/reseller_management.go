package service

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"strings"
	"time"

	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type ResellerManagementService struct {
	repo repository.ResellerRepository
	cfg  config.ResellerConfig
}

type ResellerApplyInput struct {
	Reason string
}

type ResellerApproveInput struct {
	DefaultMarkupPercent decimal.Decimal
	MaxMarkupPercent     decimal.Decimal
}

type ResellerProfileUpdateInput struct {
	DefaultMarkupPercent decimal.Decimal
	MaxMarkupPercent     decimal.Decimal
	SettlementStatus     string
	Reason               string
}

type ResellerSystemDomainInput struct {
	Subdomain string
}

type ResellerApproveResult struct {
	Profile      *models.ResellerProfile
	SystemDomain *models.ResellerDomain
}

func NewResellerManagementService(repo repository.ResellerRepository, cfg config.ResellerConfig) *ResellerManagementService {
	return &ResellerManagementService{repo: repo, cfg: cfg}
}

func (s *ResellerManagementService) GetUserManagementSnapshot(userID uint) (*models.ResellerProfile, []models.ResellerDomain, bool, error) {
	if s == nil || s.repo == nil || userID == 0 {
		return nil, []models.ResellerDomain{}, false, nil
	}
	profile, err := s.repo.GetProfileByUserID(userID)
	if err != nil {
		return nil, nil, false, err
	}
	if profile == nil {
		return nil, []models.ResellerDomain{}, s.cfg.Enabled && s.cfg.SelfApplyEnabled, nil
	}
	domains, err := s.repo.ListDomainsByResellerID(profile.ID)
	if err != nil {
		return nil, nil, false, err
	}
	canApply := profile.Status == models.ResellerProfileStatusRejected && s.cfg.Enabled && s.cfg.SelfApplyEnabled
	return profile, domains, canApply, nil
}

func (s *ResellerManagementService) ApplyUserReseller(userID uint, input ResellerApplyInput) (*models.ResellerProfile, error) {
	if s == nil || s.repo == nil || userID == 0 {
		return nil, ErrNotFound
	}
	if !s.cfg.Enabled || !s.cfg.SelfApplyEnabled {
		return nil, ErrResellerApplyDisabled
	}
	reason := strings.TrimSpace(input.Reason)
	existing, err := s.repo.GetProfileByUserID(userID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		profile := &models.ResellerProfile{
			UserID:           userID,
			Status:           models.ResellerProfileStatusPendingReview,
			ApplyReason:      reason,
			SettlementStatus: models.ResellerSettlementStatusNormal,
		}
		if err := s.repo.CreateProfile(profile); err != nil {
			return nil, err
		}
		return s.repo.GetProfileByID(profile.ID)
	}
	switch existing.Status {
	case models.ResellerProfileStatusRejected:
		existing.Status = models.ResellerProfileStatusPendingReview
		existing.ApplyReason = reason
		existing.RejectReason = ""
		existing.ReviewedBy = nil
		existing.ReviewedAt = nil
		if err := s.repo.UpdateProfile(existing); err != nil {
			return nil, err
		}
		return s.repo.GetProfileByID(existing.ID)
	case models.ResellerProfileStatusPendingReview, models.ResellerProfileStatusActive:
		return existing, nil
	case models.ResellerProfileStatusDisabled:
		return nil, ErrResellerProfileInactive
	default:
		return nil, ErrResellerProfileStatusInvalid
	}
}

func (s *ResellerManagementService) ApproveProfile(ctx context.Context, adminID, profileID uint, input ResellerApproveInput) (*ResellerApproveResult, error) {
	if s == nil || s.repo == nil || adminID == 0 || profileID == 0 {
		return nil, ErrNotFound
	}
	if err := validateResellerProfileMarkup(input.DefaultMarkupPercent, input.MaxMarkupPercent); err != nil {
		return nil, err
	}
	var result *ResellerApproveResult
	err := s.repo.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		profile, err := repoTx.GetProfileByID(profileID)
		if err != nil {
			return err
		}
		if profile == nil {
			return ErrNotFound
		}
		if profile.Status != models.ResellerProfileStatusPendingReview && profile.Status != models.ResellerProfileStatusRejected {
			return ErrResellerProfileStatusInvalid
		}
		now := time.Now()
		profile.Status = models.ResellerProfileStatusActive
		profile.RejectReason = ""
		profile.DefaultMarkupPercent = models.NewMoneyFromDecimal(input.DefaultMarkupPercent)
		profile.MaxMarkupPercent = models.NewMoneyFromDecimal(input.MaxMarkupPercent)
		profile.SettlementStatus = models.ResellerSettlementStatusNormal
		profile.ReviewedBy = &adminID
		profile.ReviewedAt = &now
		if err := repoTx.UpdateProfile(profile); err != nil {
			return err
		}
		result = &ResellerApproveResult{Profile: profile}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result != nil && result.SystemDomain != nil {
		_ = cache.DelResellerDomain(ctx, result.SystemDomain.Domain)
	}
	return result, nil
}

func (s *ResellerManagementService) AssignSystemSubdomain(ctx context.Context, adminID, profileID uint, input ResellerSystemDomainInput) (*models.ResellerDomain, error) {
	if s == nil || s.repo == nil || adminID == 0 || profileID == 0 {
		return nil, ErrNotFound
	}
	base := NormalizeResellerHost(s.cfg.SubdomainBase)
	if base == "" {
		return nil, ErrResellerSubdomainBaseMissing
	}
	nextDomain, err := normalizeAndValidateSystemSubdomain(input.Subdomain, base, s.cfg)
	if err != nil {
		return nil, err
	}
	cacheDomains := make([]string, 0, 3)
	var updatedID uint
	err = s.repo.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		profile, err := repoTx.GetProfileByID(profileID)
		if err != nil {
			return err
		}
		if profile == nil {
			return ErrNotFound
		}
		existingHost, err := repoTx.FindDomainByHost(nextDomain)
		if err != nil {
			return err
		}
		domains, err := repoTx.ListDomainsByResellerID(profile.ID)
		if err != nil {
			return err
		}
		var systemDomain *models.ResellerDomain
		hasPrimary := false
		for i := range domains {
			domain := domains[i]
			if domain.IsPrimary {
				hasPrimary = true
			}
			if domain.Type == models.ResellerDomainTypeSubdomain && systemDomain == nil {
				copyDomain := domain
				systemDomain = &copyDomain
			}
		}
		if existingHost != nil && existingHost.ResellerID != profile.ID {
			return ErrResellerDomainConflict
		}
		if existingHost != nil && systemDomain != nil && existingHost.ID != systemDomain.ID {
			return ErrResellerDomainConflict
		}
		now := time.Now()
		shouldBePrimary := !hasPrimary || (systemDomain != nil && systemDomain.IsPrimary)
		if systemDomain == nil {
			row, err := repoTx.UpsertDomain(models.ResellerDomain{
				ResellerID:         profile.ID,
				Domain:             nextDomain,
				Type:               models.ResellerDomainTypeSubdomain,
				VerificationStatus: models.ResellerDomainVerificationVerified,
				Status:             models.ResellerDomainStatusActive,
				IsPrimary:          shouldBePrimary,
				VerifiedAt:         &now,
			})
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "already exists") ||
					strings.Contains(strings.ToLower(err.Error()), "unique") ||
					strings.Contains(strings.ToLower(err.Error()), "duplicate") {
					return ErrResellerDomainConflict
				}
				return err
			}
			updatedID = row.ID
			cacheDomains = append(cacheDomains, row.Domain)
			return nil
		}
		if systemDomain.Domain != "" {
			cacheDomains = append(cacheDomains, systemDomain.Domain)
		}
		systemDomain.Domain = nextDomain
		systemDomain.Type = models.ResellerDomainTypeSubdomain
		systemDomain.VerificationToken = ""
		systemDomain.VerificationStatus = models.ResellerDomainVerificationVerified
		systemDomain.Status = models.ResellerDomainStatusActive
		systemDomain.IsPrimary = shouldBePrimary
		systemDomain.VerifiedAt = &now
		if err := repoTx.UpdateDomain(systemDomain); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "already exists") ||
				strings.Contains(strings.ToLower(err.Error()), "unique") ||
				strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				return ErrResellerDomainConflict
			}
			return err
		}
		updatedID = systemDomain.ID
		cacheDomains = append(cacheDomains, systemDomain.Domain)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, domain := range cacheDomains {
		if domain != "" {
			_ = cache.DelResellerDomain(ctx, domain)
		}
	}
	return s.repo.GetDomainByID(updatedID)
}

func (s *ResellerManagementService) RejectProfile(adminID, profileID uint, reason string) (*models.ResellerProfile, error) {
	return s.updateProfileReviewStatus(adminID, profileID, models.ResellerProfileStatusRejected, strings.TrimSpace(reason))
}

func (s *ResellerManagementService) DisableProfile(adminID, profileID uint, reason string) (*models.ResellerProfile, error) {
	return s.updateProfileReviewStatus(adminID, profileID, models.ResellerProfileStatusDisabled, strings.TrimSpace(reason))
}

func (s *ResellerManagementService) RestoreProfile(adminID, profileID uint) (*models.ResellerProfile, error) {
	return s.updateProfileReviewStatus(adminID, profileID, models.ResellerProfileStatusActive, "")
}

func (s *ResellerManagementService) UpdateProfileOperationalConfig(adminID, profileID uint, input ResellerProfileUpdateInput) (*models.ResellerProfile, error) {
	if s == nil || s.repo == nil || adminID == 0 || profileID == 0 {
		return nil, ErrNotFound
	}
	if err := validateResellerProfileMarkup(input.DefaultMarkupPercent, input.MaxMarkupPercent); err != nil {
		return nil, err
	}
	settlementStatus := strings.TrimSpace(input.SettlementStatus)
	if settlementStatus != "" && settlementStatus != models.ResellerSettlementStatusNormal && settlementStatus != models.ResellerSettlementStatusFrozen {
		return nil, ErrResellerProfileStatusInvalid
	}
	var updated *models.ResellerProfile
	err := s.repo.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		profile, err := repoTx.GetProfileByID(profileID)
		if err != nil {
			return err
		}
		if profile == nil {
			return ErrNotFound
		}
		now := time.Now()
		profile.DefaultMarkupPercent = models.NewMoneyFromDecimal(input.DefaultMarkupPercent)
		profile.MaxMarkupPercent = models.NewMoneyFromDecimal(input.MaxMarkupPercent)
		if settlementStatus != "" {
			profile.SettlementStatus = settlementStatus
		}
		profile.ReviewedBy = &adminID
		profile.ReviewedAt = &now
		if err := repoTx.UpdateProfile(profile); err != nil {
			return err
		}
		updated = profile
		return nil
	})
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrNotFound
	}
	return s.repo.GetProfileByID(profileID)
}

func (s *ResellerManagementService) SubmitUserCustomDomain(userID uint, rawDomain string) (*models.ResellerDomain, error) {
	if s == nil || s.repo == nil || userID == 0 {
		return nil, ErrNotFound
	}
	profile, err := s.repo.GetProfileByUserID(userID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, ErrResellerNotOpened
	}
	if profile.Status != models.ResellerProfileStatusActive {
		return nil, ErrResellerProfileInactive
	}
	domain, err := normalizeAndValidateCustomDomain(rawDomain, s.cfg)
	if err != nil {
		return nil, err
	}
	// 暂不启用 DNS 自动校验，自定义域名走后台人工审核，不再生成验证令牌（generateResellerDomainVerificationToken 保留，待接入真实 DNS 校验时恢复）。
	row, err := s.repo.UpsertDomain(models.ResellerDomain{
		ResellerID:         profile.ID,
		Domain:             domain,
		Type:               models.ResellerDomainTypeCustom,
		VerificationStatus: models.ResellerDomainVerificationPending,
		Status:             models.ResellerDomainStatusPendingReview,
		IsPrimary:          false,
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil, ErrResellerDomainConflict
		}
		return nil, err
	}
	return row, nil
}

func (s *ResellerManagementService) ApproveDomain(ctx context.Context, adminID, domainID uint) (*models.ResellerDomain, error) {
	return s.updateDomainStatus(ctx, adminID, domainID, models.ResellerDomainStatusActive)
}

func (s *ResellerManagementService) DisableDomain(ctx context.Context, adminID, domainID uint) (*models.ResellerDomain, error) {
	return s.updateDomainStatus(ctx, adminID, domainID, models.ResellerDomainStatusDisabled)
}

func (s *ResellerManagementService) SetPrimaryDomain(ctx context.Context, adminID, domainID uint) (*models.ResellerDomain, error) {
	if s == nil || s.repo == nil || adminID == 0 || domainID == 0 {
		return nil, ErrNotFound
	}
	cacheDomains := make([]string, 0, 4)
	err := s.repo.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		target, err := repoTx.GetDomainByIDForUpdate(domainID)
		if err != nil {
			return err
		}
		if target == nil {
			return ErrNotFound
		}
		if target.Status != models.ResellerDomainStatusActive || target.VerificationStatus != models.ResellerDomainVerificationVerified {
			return ErrResellerDomainStatusInvalid
		}
		domains, err := repoTx.ListDomainsByResellerID(target.ResellerID)
		if err != nil {
			return err
		}
		for i := range domains {
			domain := domains[i]
			nextPrimary := domain.ID == target.ID
			if domain.IsPrimary == nextPrimary {
				if domain.Domain != "" {
					cacheDomains = append(cacheDomains, domain.Domain)
				}
				continue
			}
			domain.IsPrimary = nextPrimary
			if err := repoTx.UpdateDomain(&domain); err != nil {
				return err
			}
			if domain.Domain != "" {
				cacheDomains = append(cacheDomains, domain.Domain)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, domain := range cacheDomains {
		_ = cache.DelResellerDomain(ctx, domain)
	}
	return s.repo.GetDomainByID(domainID)
}

func (s *ResellerManagementService) updateProfileReviewStatus(adminID, profileID uint, targetStatus, reason string) (*models.ResellerProfile, error) {
	if s == nil || s.repo == nil || adminID == 0 || profileID == 0 {
		return nil, ErrNotFound
	}
	cacheDomains := make([]string, 0, 4)
	err := s.repo.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		profile, err := repoTx.GetProfileByID(profileID)
		if err != nil {
			return err
		}
		if profile == nil {
			return ErrNotFound
		}
		switch targetStatus {
		case models.ResellerProfileStatusRejected:
			if profile.Status != models.ResellerProfileStatusPendingReview {
				return ErrResellerProfileStatusInvalid
			}
		case models.ResellerProfileStatusDisabled:
			if profile.Status != models.ResellerProfileStatusPendingReview && profile.Status != models.ResellerProfileStatusActive && profile.Status != models.ResellerProfileStatusRejected {
				return ErrResellerProfileStatusInvalid
			}
		case models.ResellerProfileStatusActive:
			if profile.Status != models.ResellerProfileStatusDisabled {
				return ErrResellerProfileStatusInvalid
			}
		default:
			return ErrResellerProfileStatusInvalid
		}
		domains, err := repoTx.ListDomainsByResellerID(profile.ID)
		if err != nil {
			return err
		}
		for i := range domains {
			if domains[i].Domain != "" {
				cacheDomains = append(cacheDomains, domains[i].Domain)
			}
		}
		now := time.Now()
		profile.Status = targetStatus
		if targetStatus == models.ResellerProfileStatusRejected || targetStatus == models.ResellerProfileStatusDisabled {
			profile.RejectReason = strings.TrimSpace(reason)
		} else {
			profile.RejectReason = ""
		}
		profile.ReviewedBy = &adminID
		profile.ReviewedAt = &now
		return repoTx.UpdateProfile(profile)
	})
	if err != nil {
		return nil, err
	}
	for _, domain := range cacheDomains {
		_ = cache.DelResellerDomain(context.Background(), domain)
	}
	return s.repo.GetProfileByID(profileID)
}

func (s *ResellerManagementService) updateDomainStatus(ctx context.Context, adminID, domainID uint, targetStatus string) (*models.ResellerDomain, error) {
	if s == nil || s.repo == nil || adminID == 0 || domainID == 0 {
		return nil, ErrNotFound
	}
	cacheDomains := make([]string, 0, 4)
	err := s.repo.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		domain, err := repoTx.GetDomainByIDForUpdate(domainID)
		if err != nil {
			return err
		}
		if domain == nil {
			return ErrNotFound
		}
		domains, err := repoTx.ListDomainsByResellerID(domain.ResellerID)
		if err != nil {
			return err
		}
		for i := range domains {
			if domains[i].Domain != "" {
				cacheDomains = append(cacheDomains, domains[i].Domain)
			}
		}
		switch targetStatus {
		case models.ResellerDomainStatusActive:
			if domain.Status != models.ResellerDomainStatusPendingReview && domain.Status != models.ResellerDomainStatusDisabled {
				return ErrResellerDomainStatusInvalid
			}
			now := time.Now()
			domain.Status = models.ResellerDomainStatusActive
			domain.VerificationStatus = models.ResellerDomainVerificationVerified
			domain.VerifiedAt = &now
			if !hasActiveVerifiedPrimary(domains, domain.ID) {
				domain.IsPrimary = true
			}
		case models.ResellerDomainStatusDisabled:
			if domain.Status != models.ResellerDomainStatusPendingReview && domain.Status != models.ResellerDomainStatusActive {
				return ErrResellerDomainStatusInvalid
			}
			domain.Status = models.ResellerDomainStatusDisabled
			wasPrimary := domain.IsPrimary
			domain.IsPrimary = false
			if wasPrimary {
				for i := range domains {
					candidate := domains[i]
					if candidate.ID == domain.ID || candidate.Status != models.ResellerDomainStatusActive || candidate.VerificationStatus != models.ResellerDomainVerificationVerified {
						continue
					}
					if !candidate.IsPrimary {
						candidate.IsPrimary = true
						if err := repoTx.UpdateDomain(&candidate); err != nil {
							return err
						}
					}
					break
				}
			}
		default:
			return ErrResellerDomainStatusInvalid
		}
		if err := repoTx.UpdateDomain(domain); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, domain := range cacheDomains {
		_ = cache.DelResellerDomain(ctx, domain)
	}
	return s.repo.GetDomainByID(domainID)
}

func validateResellerProfileMarkup(defaultMarkup, maxMarkup decimal.Decimal) error {
	if defaultMarkup.LessThan(decimal.Zero) || maxMarkup.LessThan(decimal.Zero) {
		return ErrResellerProfileStatusInvalid
	}
	if maxMarkup.GreaterThan(decimal.Zero) && defaultMarkup.GreaterThan(maxMarkup) {
		return ErrResellerProfileStatusInvalid
	}
	return nil
}

func hasActiveVerifiedPrimary(domains []models.ResellerDomain, excludeID uint) bool {
	for i := range domains {
		domain := domains[i]
		if domain.ID == excludeID {
			continue
		}
		if domain.IsPrimary && domain.Status == models.ResellerDomainStatusActive && domain.VerificationStatus == models.ResellerDomainVerificationVerified {
			return true
		}
	}
	return false
}

func normalizeAndValidateCustomDomain(raw string, cfg config.ResellerConfig) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrResellerDomainInvalid
	}
	if strings.Contains(trimmed, "://") || strings.ContainsAny(trimmed, "/?#") {
		return "", ErrResellerDomainInvalid
	}
	domain := NormalizeResellerHost(trimmed)
	if domain == "" {
		return "", ErrResellerDomainInvalid
	}
	for _, mainHost := range cfg.MainHosts {
		if domain == NormalizeResellerHost(mainHost) {
			return "", ErrResellerDomainMainHostNotAllowed
		}
	}
	base := NormalizeResellerHost(cfg.SubdomainBase)
	if base != "" && (domain == base || strings.HasSuffix(domain, "."+base)) {
		return "", ErrResellerDomainMainHostNotAllowed
	}
	return domain, nil
}

func normalizeAndValidateSystemSubdomain(raw string, base string, cfg config.ResellerConfig) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrResellerDomainInvalid
	}
	if strings.Contains(trimmed, "://") || strings.Contains(trimmed, ":") || strings.ContainsAny(trimmed, "/?#") || strings.ContainsAny(trimmed, " \t\r\n") {
		return "", ErrResellerDomainInvalid
	}
	base = NormalizeResellerHost(base)
	if base == "" {
		return "", ErrResellerSubdomainBaseMissing
	}
	normalized := NormalizeResellerHost(trimmed)
	if normalized == "" {
		return "", ErrResellerDomainInvalid
	}
	domain := normalized
	if !strings.Contains(normalized, ".") {
		if !isValidResellerSubdomainLabel(normalized) {
			return "", ErrResellerDomainInvalid
		}
		domain = normalized + "." + base
	}
	if domain == base || !strings.HasSuffix(domain, "."+base) {
		return "", ErrResellerDomainInvalid
	}
	label := strings.TrimSuffix(domain, "."+base)
	if strings.Contains(label, ".") || !isValidResellerSubdomainLabel(label) {
		return "", ErrResellerDomainInvalid
	}
	for _, mainHost := range cfg.MainHosts {
		if domain == NormalizeResellerHost(mainHost) {
			return "", ErrResellerDomainMainHostNotAllowed
		}
	}
	return domain, nil
}

func isValidResellerSubdomainLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for _, ch := range label {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '-':
		default:
			return false
		}
	}
	return true
}

func generateResellerDomainVerificationToken() (string, error) {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	return "reseller-verify-" + strings.ToLower(token), nil
}
