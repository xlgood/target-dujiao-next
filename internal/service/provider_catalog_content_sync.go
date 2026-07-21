package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/upstream"

	"gorm.io/gorm"
)

type FansGurusCatalogContentClient interface {
	ListCatalogDetails(ctx context.Context) ([]upstream.FansGurusCatalogDetail, error)
}

type ProviderCatalogContentSyncInput struct {
	FansGurusConnectionID uint
	TGXConnectionID       uint
}

type ProviderCatalogContentSyncResult struct {
	FansGurusPulled  int `json:"fans_gurus_pulled"`
	TGXPulled        int `json:"tgx_pulled"`
	TGXProfilePulled int `json:"tgx_profile_pulled"`
	TGXProfileFailed int `json:"tgx_profile_failed"`
	Matched          int `json:"matched"`
	Updated          int `json:"updated"`
	Skipped          int `json:"skipped"`
}

// TGXCatalogItemClient is optional to preserve compatibility with the list
// endpoint. The real client implements it; tests and legacy adapters can fall
// back to the catalog profile until it is available.
type TGXCatalogItemClient interface {
	GetItem(ctx context.Context, sharedCode string) (*upstream.TGXCommodity, error)
}

type providerCatalogContentSource struct {
	Provider    string
	Code        string
	Name        string
	Category    string
	Description string
	AverageTime string
	ServiceType string
}

type providerCatalogContentLine struct {
	Text        string
	TutorialURL string
}

const providerCatalogContentSanitizerVersion = "v3"

func (s *ProductMappingService) SetProviderCatalogContentSyncRunRepository(repo repository.ProviderCatalogContentSyncRunRepository) {
	s.contentSyncRunRepo = repo
}

func (s *ProductMappingService) ListProviderCatalogContentSyncRuns(filter repository.ProviderCatalogContentSyncRunListFilter) ([]models.ProviderCatalogContentSyncRun, int64, error) {
	if s == nil || s.contentSyncRunRepo == nil {
		return []models.ProviderCatalogContentSyncRun{}, 0, nil
	}
	return s.contentSyncRunRepo.List(filter)
}

// SyncProviderCatalogContentWithClients refreshes only customer-facing
// metadata. It does not touch price, inventory, SKU mappings, forms, review,
// or publication state.
func (s *ProductMappingService) SyncProviderCatalogContentWithClients(
	ctx context.Context,
	input ProviderCatalogContentSyncInput,
	fansClient FansGurusCatalogContentClient,
	tgxClient TGXCatalogClient,
) (*ProviderCatalogContentSyncResult, error) {
	if s == nil || s.mappingRepo == nil || s.productRepo == nil {
		return nil, errorsProductMappingDependencyMissing()
	}
	if fansClient == nil || tgxClient == nil {
		return nil, fmt.Errorf("provider catalog content clients are required")
	}
	startedAt := time.Now()
	result := &ProviderCatalogContentSyncResult{}
	fansDetails, err := fansClient.ListCatalogDetails(ctx)
	if err != nil {
		s.recordProviderCatalogContentSyncRun(startedAt, result, err)
		return nil, fmt.Errorf("list fansgurus public catalog details: %w", err)
	}
	result.FansGurusPulled = len(fansDetails)
	tgxItems, err := tgxClient.ListItems(ctx)
	if err != nil {
		s.recordProviderCatalogContentSyncRun(startedAt, result, err)
		return nil, fmt.Errorf("list tgx catalog details: %w", err)
	}
	if tgxItems != nil {
		result.TGXPulled = len(tgxItems.Items)
	}

	sources := providerCatalogContentSources(fansDetails, tgxItems)
	tgxProfiles, profileResult, err := s.loadTGXCatalogItemProfiles(ctx, input.TGXConnectionID, tgxClient)
	if err != nil {
		s.recordProviderCatalogContentSyncRun(startedAt, result, err)
		return nil, err
	}
	result.TGXProfilePulled = profileResult.pulled
	result.TGXProfileFailed = profileResult.failed
	for _, target := range []struct {
		connectionID uint
		provider     string
	}{
		{input.FansGurusConnectionID, upstream.CatalogProviderFansGurus},
		{input.TGXConnectionID, upstream.CatalogProviderTGX},
	} {
		if target.connectionID == 0 {
			continue
		}
		mappings, err := s.mappingRepo.ListByProvider(target.connectionID, target.provider)
		if err != nil {
			s.recordProviderCatalogContentSyncRun(startedAt, result, err)
			return nil, err
		}
		for i := range mappings {
			if !mappings[i].IsActive {
				result.Skipped++
				continue
			}
			source, ok := sources[providerCatalogContentKey(target.provider, mappings[i].UpstreamProductCode)]
			if target.provider == upstream.CatalogProviderTGX {
				if profile, exists := tgxProfiles[strings.TrimSpace(mappings[i].UpstreamProductCode)]; exists {
					if _, err := s.applyTGXCatalogItemProfile(&mappings[i], profile); err != nil {
						s.recordProviderCatalogContentSyncRun(startedAt, result, err)
						return nil, err
					}
					source = providerCatalogContentSourceFromTGXProfile(profile, source)
					ok = true
				}
			}
			if !ok || source.containsTelegram() {
				result.Skipped++
				continue
			}
			result.Matched++
			changed, err := s.applyProviderCatalogContentSource(&mappings[i], source)
			if err != nil {
				s.recordProviderCatalogContentSyncRun(startedAt, result, err)
				return nil, err
			}
			if changed {
				result.Updated++
			}
		}
	}
	s.recordProviderCatalogContentSyncRun(startedAt, result, nil)
	return result, nil
}

func providerCatalogContentSources(fansDetails []upstream.FansGurusCatalogDetail, tgxItems *upstream.TGXItemsResponse) map[string]providerCatalogContentSource {
	result := make(map[string]providerCatalogContentSource, len(fansDetails))
	for _, detail := range fansDetails {
		if detail.Service == 0 {
			continue
		}
		code := strconv.FormatUint(uint64(detail.Service), 10)
		result[providerCatalogContentKey(upstream.CatalogProviderFansGurus, code)] = providerCatalogContentSource{
			Provider: upstream.CatalogProviderFansGurus, Code: code, Name: detail.Name, Category: detail.Category,
			Description: detail.Description, AverageTime: detail.AverageTime, ServiceType: detail.ServiceType,
		}
	}
	if tgxItems == nil {
		return result
	}
	for _, item := range tgxItems.Items {
		code := strings.TrimSpace(item.Code)
		if code == "" {
			continue
		}
		result[providerCatalogContentKey(upstream.CatalogProviderTGX, code)] = providerCatalogContentSource{
			Provider: upstream.CatalogProviderTGX, Code: code, Name: item.Name, Category: item.Category,
			Description: item.Description,
		}
	}
	return result
}

func providerCatalogContentKey(provider, code string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + ":" + strings.TrimSpace(code)
}

type tgxCatalogItemProfileResult struct {
	pulled int
	failed int
}

// loadTGXCatalogItemProfiles prefers each mapped product's item endpoint.
// It runs in the dedicated content worker and shares the existing request
// limiter, so it never creates a burst of one request per SKU.
func (s *ProductMappingService) loadTGXCatalogItemProfiles(ctx context.Context, connectionID uint, client TGXCatalogClient) (map[string]upstream.ProviderCatalogItem, tgxCatalogItemProfileResult, error) {
	profiles := make(map[string]upstream.ProviderCatalogItem)
	profileClient, ok := client.(TGXCatalogItemClient)
	if !ok || connectionID == 0 {
		return profiles, tgxCatalogItemProfileResult{}, nil
	}
	mappings, err := s.mappingRepo.ListActiveByProvider(connectionID, upstream.CatalogProviderTGX)
	if err != nil {
		return nil, tgxCatalogItemProfileResult{}, err
	}
	cfg := DefaultUpstreamSyncConfig()
	if s.settingService != nil {
		cfg, _ = s.settingService.GetUpstreamSyncConfig("")
	}
	result := tgxCatalogItemProfileResult{}
	for _, mapping := range mappings {
		code := strings.TrimSpace(mapping.UpstreamProductCode)
		if code == "" {
			result.failed++
			continue
		}
		if err := waitForTGXRequest(ctx, connectionID, cfg.TGXInventoryRateLimit); err != nil {
			return nil, result, err
		}
		commodity, err := profileClient.GetItem(ctx, code)
		if err != nil || commodity == nil {
			result.failed++
			continue
		}
		if strings.TrimSpace(commodity.Code) == "" {
			commodity.Code = code
		}
		if strings.TrimSpace(commodity.Code) != code {
			result.failed++
			continue
		}
		item, err := upstream.NewTGXCatalogItem(*commodity)
		if err != nil || upstream.ContainsTelegramCatalogText(item.Name, item.Category, item.Description) {
			result.failed++
			continue
		}
		profiles[code] = item
		result.pulled++
	}
	return profiles, result, nil
}

func providerCatalogContentSourceFromTGXProfile(profile upstream.ProviderCatalogItem, fallback providerCatalogContentSource) providerCatalogContentSource {
	result := fallback
	result.Provider = upstream.CatalogProviderTGX
	if value := strings.TrimSpace(profile.Code); value != "" {
		result.Code = value
	}
	if value := strings.TrimSpace(profile.Name); value != "" {
		result.Name = value
	}
	if value := strings.TrimSpace(profile.Category); value != "" {
		result.Category = value
	}
	if value := strings.TrimSpace(profile.Description); value != "" {
		result.Description = value
	}
	return result
}

func (s *ProductMappingService) applyTGXCatalogItemProfile(mapping *models.ProductMapping, item upstream.ProviderCatalogItem) (bool, error) {
	if mapping == nil || mapping.LocalProductID == 0 || mapping.Provider != upstream.CatalogProviderTGX {
		return false, nil
	}
	profileSchema := providerCatalogManualFormSchema(item)
	profileFulfillment := providerCatalogUpstreamFulfillmentType(item)
	changed := false
	productID := strconv.FormatUint(uint64(mapping.LocalProductID), 10)
	err := s.productRepo.Transaction(func(tx *gorm.DB) error {
		product, err := s.productRepo.WithTx(tx).GetByID(productID)
		if err != nil {
			return err
		}
		if product == nil {
			return nil
		}
		if !mapping.ManualFormSchemaLocked && !sameCatalogJSON(product.ManualFormSchemaJSON, profileSchema) {
			product.ManualFormSchemaJSON = profileSchema
			if err := s.productRepo.WithTx(tx).Update(product); err != nil {
				return err
			}
			changed = true
		}
		now := time.Now()
		if mapping.UpstreamFulfillmentType != profileFulfillment {
			mapping.UpstreamFulfillmentType = profileFulfillment
			changed = true
		}
		if !mapping.ManualFormSchemaLocked {
			mapping.CatalogProfileSource = "item"
		}
		mapping.CatalogProfileSyncedAt = &now
		return s.mappingRepo.WithTx(tx).Update(mapping)
	})
	return changed, err
}

func sameCatalogJSON(left, right models.JSON) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func (source providerCatalogContentSource) containsTelegram() bool {
	return upstream.ContainsTelegramCatalogText(source.Name, source.Category, source.Description)
}

func (s *ProductMappingService) applyProviderCatalogContentSource(mapping *models.ProductMapping, source providerCatalogContentSource) (bool, error) {
	if mapping == nil || mapping.LocalProductID == 0 || !mapping.IsActive {
		return false, nil
	}
	content, description := providerCatalogCustomerContent(source)
	hash := providerCatalogContentHash(source)
	if hash == mapping.CatalogSourceHash {
		return false, nil
	}
	productID := strconv.FormatUint(uint64(mapping.LocalProductID), 10)
	changed := false
	err := s.productRepo.Transaction(func(tx *gorm.DB) error {
		product, err := s.productRepo.WithTx(tx).GetByID(productID)
		if err != nil {
			return err
		}
		if product == nil {
			return nil
		}
		now := time.Now()
		product.TitleJSON = localizedText(providerCatalogDisplayTitle(source.Name))
		product.DescriptionJSON = description
		product.ContentJSON = content
		if err := s.productRepo.WithTx(tx).Update(product); err != nil {
			return err
		}
		mapping.CatalogSourceCategory = strings.TrimSpace(source.Category)
		mapping.CatalogSourceDescription = strings.TrimSpace(source.Description)
		mapping.CatalogSourceAverageTime = strings.TrimSpace(source.AverageTime)
		mapping.CatalogSourceHash = hash
		mapping.CatalogSourceSyncedAt = &now
		if err := s.mappingRepo.WithTx(tx).Update(mapping); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

func (s *ProductMappingService) recordProviderCatalogContentSyncRun(startedAt time.Time, result *ProviderCatalogContentSyncResult, syncErr error) {
	if s == nil || s.contentSyncRunRepo == nil {
		return
	}
	run := &models.ProviderCatalogContentSyncRun{Status: "success", StartedAt: startedAt, FinishedAt: time.Now()}
	if result != nil {
		run.FansGurusPulled = result.FansGurusPulled
		run.TGXPulled = result.TGXPulled
		run.TGXProfilePulled = result.TGXProfilePulled
		run.TGXProfileFailed = result.TGXProfileFailed
		run.Matched = result.Matched
		run.Updated = result.Updated
		run.Skipped = result.Skipped
	}
	if result != nil && result.TGXProfileFailed > 0 && syncErr == nil {
		run.Status = "partial"
	}
	if syncErr != nil {
		run.Status = "failed"
		run.ErrorMessage = syncErr.Error()
	}
	_ = s.contentSyncRunRepo.Create(run)
}

func providerCatalogContentHash(source providerCatalogContentSource) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		providerCatalogContentSanitizerVersion,
		strings.TrimSpace(source.Provider), strings.TrimSpace(source.Code), strings.TrimSpace(source.Name),
		strings.TrimSpace(source.Category), strings.TrimSpace(source.Description), strings.TrimSpace(source.AverageTime), strings.TrimSpace(source.ServiceType),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

var (
	catalogURLRE      = regexp.MustCompile(`(?i)(?:https?://|www\.)[^\s<]+`)
	catalogEmailRE    = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
	catalogSourceRE   = regexp.MustCompile(`(?i)(?:fans\s*gurus|fansgurus|tgx\s*account|tgx|upstream|supplier|provider|source|merchant|vendor|心蓝|心藍|bhdata|供货|供應|供应商|供應商|服务商|服務商|上游|货源|貨源|平台代理|平台商家|來源|来源|商家)`)
	catalogContactRE  = regexp.MustCompile(`(?i)(?:联系客服|聯繫客服|联系.*客服|聯絡.*客服|contact.*support|customer\s*service|whatsapp|telegram|t\.me)`)
	catalogAnchorRE   = regexp.MustCompile(`(?is)<a\s+[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	catalogToolRE     = regexp.MustCompile(`(?i)(?:心蓝|心藍|bhdata)`)
	catalogToolStepRE = regexp.MustCompile(`(?i)(?:^\s*步骤|^\s*步驟)`)
	catalogTutorialRE = regexp.MustCompile(`(?i)(?:教程|教學|指南|tutorial|guide|how\s+to|login\s+steps?)`)
	catalogDownloadRE = regexp.MustCompile(`(?i)(?:下载|下載|download)`)
	catalogFormatRE   = regexp.MustCompile(`(?i)(?:格式|format)\s*[：:]\s*([^\r\n<]+)`)
)

func providerCatalogDisplayTitle(value string) string {
	value = strings.TrimSpace(catalogSourceRE.ReplaceAllString(value, ""))
	value = strings.Join(strings.Fields(value), " ")
	return strings.Trim(value, " -|：:")
}

func providerCatalogCustomerContent(source providerCatalogContentSource) (models.JSON, models.JSON) {
	lines := sanitizeProviderCatalogContentLines(source.Description)
	if source.requiresExternalToolRewrite() {
		lines = providerCatalogExternalToolSafeLines(source)
	}
	if len(lines) == 0 {
		lines = []providerCatalogContentLine{{Text: "商品资料正在更新，请确认所需信息后提交订单。"}}
	}
	var content strings.Builder
	content.WriteString("<h3>商品说明</h3>")
	for _, line := range lines {
		content.WriteString("<p>")
		content.WriteString(html.EscapeString(line.Text))
		if line.TutorialURL != "" {
			content.WriteString(` <a href="`)
			content.WriteString(html.EscapeString(line.TutorialURL))
			content.WriteString(`" target="_blank" rel="noopener noreferrer nofollow">查看教程</a>`)
		}
		content.WriteString("</p>")
	}
	if category := sanitizeProviderCatalogLabel(source.Category); category != "" || strings.TrimSpace(source.AverageTime) != "" {
		content.WriteString("<h3>服务信息</h3><ul>")
		if category != "" {
			content.WriteString("<li>服务类别：")
			content.WriteString(html.EscapeString(category))
			content.WriteString("</li>")
		}
		if averageTime := sanitizeProviderCatalogLabel(source.AverageTime); averageTime != "" {
			content.WriteString("<li>平均处理时间：")
			content.WriteString(html.EscapeString(averageTime))
			content.WriteString("</li>")
		}
		content.WriteString("</ul>")
	}
	if source.Provider == upstream.CatalogProviderFansGurus && source.isCustomComments() {
		content.WriteString("<h3>填写要求</h3><p>请填写目标链接，并每行填写一条评论。数量和金额会按有效评论条数自动计算。</p>")
	}
	summaryParts := make([]string, 0, len(lines))
	for _, line := range lines {
		summaryParts = append(summaryParts, line.Text)
	}
	summary := strings.Join(summaryParts, " ")
	if len([]rune(summary)) > 180 {
		summary = string([]rune(summary)[:180]) + "..."
	}
	return localizedText(content.String()), localizedText(summary)
}

func (source providerCatalogContentSource) isCustomComments() bool {
	text := strings.ToLower(source.ServiceType + " " + source.Name)
	return strings.Contains(text, "custom comment") || strings.Contains(text, "自定义评论") || strings.Contains(text, "自訂評論")
}

func (source providerCatalogContentSource) requiresExternalToolRewrite() bool {
	return catalogToolRE.MatchString(source.Name) || catalogToolRE.MatchString(source.Description)
}

// providerCatalogExternalToolSafeLines replaces a tool-specific workflow with
// customer-usable delivery fields and neutral configuration guidance.
func providerCatalogExternalToolSafeLines(source providerCatalogContentSource) []providerCatalogContentLine {
	text := source.Name + "\n" + source.Description
	fields := providerCatalogDeliveryFields(source.Description)
	lines := make([]providerCatalogContentLine, 0, 2)
	if len(fields) > 0 {
		lines = append(lines, providerCatalogContentLine{Text: "交付内容：" + strings.Join(fields, "、") + "。"})
	}
	upper := strings.ToUpper(text)
	if strings.Contains(upper, "OAUTH2") || strings.Contains(upper, "POP") || strings.Contains(upper, "IMAP") {
		protocols := make([]string, 0, 2)
		if strings.Contains(upper, "POP") {
			protocols = append(protocols, "POP")
		}
		if strings.Contains(upper, "IMAP") {
			protocols = append(protocols, "IMAP")
		}
		if len(protocols) > 0 {
			lines = append(lines, providerCatalogContentLine{Text: "使用方式：请使用支持 OAuth2 登录的邮件客户端，通过 " + strings.Join(protocols, "/") + " 配置邮箱。"})
		} else {
			lines = append(lines, providerCatalogContentLine{Text: "使用方式：请使用支持 OAuth2 登录的邮件客户端完成配置。"})
		}
	}
	return lines
}

func providerCatalogDeliveryFields(description string) []string {
	match := catalogFormatRE.FindStringSubmatch(description)
	if len(match) < 2 {
		return nil
	}
	value := strings.ToLower(match[1])
	known := []struct {
		contains string
		label    string
	}{
		{"邮箱", "邮箱地址"}, {"email", "邮箱地址"},
		{"账号", "账号"}, {"帳號", "账号"}, {"account", "账号"},
		{"密码", "密码"}, {"密碼", "密码"}, {"password", "密码"},
		{"clientid", "客户端标识"}, {"client_id", "客户端标识"}, {"客户端", "客户端标识"}, {"客戶端", "客户端标识"},
		{"refresh_token", "刷新令牌"}, {"refreshtoken", "刷新令牌"},
		{"授权码", "授权信息"}, {"授權碼", "授权信息"}, {"auth code", "授权信息"},
		{"token", "访问令牌"}, {"令牌", "访问令牌"},
		{"2fa", "两步验证信息"},
		{"手机", "手机号码"}, {"手機", "手机号码"}, {"phone", "手机号码"},
	}
	seen := make(map[string]bool)
	fields := make([]string, 0, len(known))
	for _, field := range known {
		if strings.Contains(value, field.contains) && !seen[field.label] {
			seen[field.label] = true
			fields = append(fields, field.label)
		}
	}
	order := map[string]int{"账号": 1, "邮箱地址": 2, "密码": 3, "客户端标识": 4, "授权信息": 5, "访问令牌": 6, "刷新令牌": 7, "两步验证信息": 8, "手机号码": 9}
	sort.SliceStable(fields, func(i, j int) bool { return order[fields[i]] < order[fields[j]] })
	return fields
}

func sanitizeProviderCatalogLabel(value string) string {
	line := sanitizeProviderCatalogLine(value)
	if catalogSourceRE.MatchString(line) || catalogContactRE.MatchString(line) {
		return ""
	}
	return line
}

func sanitizeProviderCatalogLines(raw string) []string {
	contentLines := sanitizeProviderCatalogContentLines(raw)
	result := make([]string, 0, len(contentLines))
	for _, line := range contentLines {
		result = append(result, line.Text)
	}
	return result
}

func sanitizeProviderCatalogContentLines(raw string) []providerCatalogContentLine {
	plain := strings.NewReplacer(
		"<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n", "</div>", "\n", "</li>", "\n",
	).Replace(raw)
	plain = catalogAnchorRE.ReplaceAllString(plain, "$2 $1")
	plain = regexp.MustCompile(`(?s)<[^>]*>`).ReplaceAllString(plain, "")
	plain = html.UnescapeString(plain)
	result := make([]providerCatalogContentLine, 0)
	externalToolContext := false
	for _, rawLine := range strings.Split(plain, "\n") {
		if catalogToolRE.MatchString(rawLine) {
			externalToolContext = true
		}
		if externalToolContext && catalogToolStepRE.MatchString(rawLine) {
			continue
		}
		if catalogSourceRE.MatchString(rawLine) || catalogContactRE.MatchString(rawLine) || catalogEmailRE.MatchString(rawLine) {
			continue
		}
		tutorialURL := catalogTutorialURL(rawLine)
		if catalogURLRE.MatchString(rawLine) && tutorialURL == "" {
			// Source, download, and unclassified external links do not have a
			// safe customer-facing equivalent, so remove their whole line.
			continue
		}
		line := sanitizeProviderCatalogLine(rawLine)
		if line == "" || catalogSourceRE.MatchString(line) || catalogContactRE.MatchString(line) {
			continue
		}
		result = append(result, providerCatalogContentLine{Text: line, TutorialURL: tutorialURL})
	}
	return result
}

func catalogTutorialURL(rawLine string) string {
	if !catalogTutorialRE.MatchString(rawLine) || catalogDownloadRE.MatchString(rawLine) {
		return ""
	}
	value := strings.TrimRight(catalogURLRE.FindString(rawLine), ".,，。;；:：!！?？)）]】}>\"'")
	if value == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "www.") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return parsed.String()
}

func sanitizeProviderCatalogLine(value string) string {
	value = catalogURLRE.ReplaceAllString(value, "")
	value = catalogEmailRE.ReplaceAllString(value, "")
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	value = strings.Trim(value, "•*#-—–|：:；;，,。 ")
	return value
}
