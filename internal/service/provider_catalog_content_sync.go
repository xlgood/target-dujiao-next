package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"net/url"
	"regexp"
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
	FansGurusPulled int `json:"fans_gurus_pulled"`
	TGXPulled       int `json:"tgx_pulled"`
	Matched         int `json:"matched"`
	Updated         int `json:"updated"`
	Skipped         int `json:"skipped"`
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

const providerCatalogContentSanitizerVersion = "v2"

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
		run.Matched = result.Matched
		run.Updated = result.Updated
		run.Skipped = result.Skipped
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
)

func providerCatalogDisplayTitle(value string) string {
	value = strings.TrimSpace(catalogSourceRE.ReplaceAllString(value, ""))
	value = strings.Join(strings.Fields(value), " ")
	return strings.Trim(value, " -|：:")
}

func providerCatalogCustomerContent(source providerCatalogContentSource) (models.JSON, models.JSON) {
	lines := sanitizeProviderCatalogContentLines(source.Description)
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
