package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/upstream"
)

type FansGurusCatalogClient interface {
	ListServices(ctx context.Context) ([]upstream.FansGurusService, error)
}

type TGXCatalogClient interface {
	ListItems(ctx context.Context) (*upstream.TGXItemsResponse, error)
}

type ProviderCatalogSyncInput struct {
	FansGurusConnectionID uint
	TGXConnectionID       uint
}

type ProviderCatalogSyncResult struct {
	FansGurusPulled    int                           `json:"fans_gurus_pulled"`
	TGXPulled          int                           `json:"tgx_pulled"`
	SupportedPlatforms []string                      `json:"supported_platforms"`
	FilteredTelegram   int                           `json:"filtered_telegram"`
	FilteredInactive   int                           `json:"filtered_inactive"`
	FilteredPlatform   int                           `json:"filtered_platform"`
	Imported           int                           `json:"imported"`
	Updated            int                           `json:"updated"`
	Skipped            int                           `json:"skipped"`
	Deactivated        int                           `json:"deactivated"`
	FilterReasons      []ProviderCatalogFilterReason `json:"filter_reasons"`
}

type ProviderCatalogFilterReason struct {
	Provider string `json:"provider"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
}

func (s *ProductMappingService) SyncProviderCatalogWithClients(
	ctx context.Context,
	input ProviderCatalogSyncInput,
	fansClient FansGurusCatalogClient,
	tgxClient TGXCatalogClient,
) (*ProviderCatalogSyncResult, error) {
	if fansClient == nil || tgxClient == nil {
		return nil, fmt.Errorf("provider catalog clients are required")
	}

	startedAt := time.Now()
	fansServices, err := fansClient.ListServices(ctx)
	if err != nil {
		s.recordProviderCatalogSyncRun(startedAt, nil, nil, nil, fmt.Errorf("list fansgurus services: %w", err))
		return nil, fmt.Errorf("list fansgurus services: %w", err)
	}
	cfg := DefaultUpstreamSyncConfig()
	if s.settingService != nil {
		cfg, _ = s.settingService.GetUpstreamSyncConfig("")
	}
	tgxItems, err := s.listTGXCatalogWithRetry(ctx, input.TGXConnectionID, tgxClient, cfg)
	if err != nil {
		s.recordProviderCatalogSyncRun(startedAt, fansServices, nil, nil, fmt.Errorf("list tgx items: %w", err))
		return nil, fmt.Errorf("list tgx items: %w", err)
	}
	if tgxItems == nil {
		tgxItems = &upstream.TGXItemsResponse{}
	}

	fansCatalog := make([]upstream.ProviderCatalogItem, 0, len(fansServices))
	for _, service := range fansServices {
		item, err := upstream.NewFansGurusCatalogItem(service)
		if err != nil {
			return nil, fmt.Errorf("convert fansgurus service %d: %w", service.Service, err)
		}
		fansCatalog = append(fansCatalog, item)
	}

	tgxCatalog := make([]upstream.ProviderCatalogItem, 0, len(tgxItems.Items))
	for _, commodity := range tgxItems.Items {
		item, err := upstream.NewTGXCatalogItem(commodity)
		if err != nil {
			return nil, fmt.Errorf("convert tgx item %s: %w", commodity.Code, err)
		}
		tgxCatalog = append(tgxCatalog, item)
	}

	filtered := upstream.BuildFilteredCatalog(fansCatalog, tgxCatalog)
	importResult, err := s.ImportProviderCatalogByProviderConnections(map[string]uint{
		upstream.CatalogProviderFansGurus: input.FansGurusConnectionID,
		upstream.CatalogProviderTGX:       input.TGXConnectionID,
	}, filtered)
	if err != nil {
		s.recordProviderCatalogSyncRun(startedAt, fansServices, tgxItems.Items, nil, err)
		return nil, err
	}

	deactivated, err := s.deactivateStaleProviderCatalogMappings(input, filtered)
	if err != nil {
		s.recordProviderCatalogSyncRun(startedAt, fansServices, tgxItems.Items, nil, err)
		return nil, err
	}

	result := &ProviderCatalogSyncResult{
		FansGurusPulled:    len(fansServices),
		TGXPulled:          len(tgxItems.Items),
		SupportedPlatforms: filtered.SupportedPlatforms,
		FilteredTelegram:   len(filtered.FilteredTelegram),
		FilteredInactive:   len(filtered.FilteredInactive),
		FilteredPlatform:   len(filtered.FilteredPlatform),
		Imported:           importResult.Imported,
		Updated:            importResult.Updated,
		Skipped:            importResult.Skipped,
		Deactivated:        deactivated,
		FilterReasons:      providerCatalogFilterReasons(filtered),
	}
	s.recordProviderCatalogSyncRun(startedAt, fansServices, tgxItems.Items, result, nil)
	return result, nil
}

func (s *ProductMappingService) listTGXCatalogWithRetry(ctx context.Context, connectionID uint, client TGXCatalogClient, cfg UpstreamSyncConfig) (*upstream.TGXItemsResponse, error) {
	cfg = NormalizeUpstreamSyncConfig(cfg)
	var lastErr error
	for attempt := 0; attempt <= cfg.TGXInventoryRetries; attempt++ {
		if err := waitForTGXRequest(ctx, connectionID, cfg.TGXInventoryRateLimit); err != nil {
			return nil, err
		}
		items, err := client.ListItems(ctx)
		if err == nil && items != nil {
			return items, nil
		}
		if err == nil {
			err = fmt.Errorf("empty TGX catalog response")
		}
		lastErr = err
		if !isRetryableTGXInventoryError(err) || attempt == cfg.TGXInventoryRetries {
			break
		}
		if err := waitTGXRetryBackoff(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func providerCatalogFilterReasons(catalog upstream.FilteredCatalog) []ProviderCatalogFilterReason {
	const maxReasons = 200
	result := make([]ProviderCatalogFilterReason, 0)
	for _, group := range []struct {
		reason string
		items  []upstream.ProviderCatalogItem
	}{
		{"telegram", catalog.FilteredTelegram},
		{"inactive_or_unsupported_service_type", catalog.FilteredInactive},
		{"platform_not_allowlisted", catalog.FilteredPlatform},
	} {
		for _, item := range group.items {
			if len(result) >= maxReasons {
				return result
			}
			result = append(result, ProviderCatalogFilterReason{Provider: item.Provider, Code: item.Code, Name: item.Name, Reason: group.reason})
		}
	}
	return result
}

func (s *ProductMappingService) deactivateStaleProviderCatalogMappings(input ProviderCatalogSyncInput, catalog upstream.FilteredCatalog) (int, error) {
	total := 0
	var excludedCategory *models.Category
	fansCodes := providerCatalogCodes(catalog.FansGurus)
	tgxCodes := providerCatalogCodes(catalog.TGX)
	for _, target := range []struct {
		connectionID uint
		provider     string
		activeCodes  []string
	}{
		{input.FansGurusConnectionID, upstream.CatalogProviderFansGurus, fansCodes},
		{input.TGXConnectionID, upstream.CatalogProviderTGX, tgxCodes},
	} {
		if target.connectionID == 0 {
			continue
		}
		mappings, err := s.mappingRepo.ListByProvider(target.connectionID, target.provider)
		if err != nil {
			return 0, err
		}
		active := make(map[string]struct{}, len(target.activeCodes))
		for _, code := range target.activeCodes {
			active[code] = struct{}{}
		}
		for i := range mappings {
			if _, ok := active[mappings[i].UpstreamProductCode]; ok {
				continue
			}
			if mappings[i].IsActive || mappings[i].UpstreamStatus == models.UpstreamStatusActive {
				mappings[i].IsActive = false
				mappings[i].UpstreamStatus = models.UpstreamStatusInactive
				if err := s.mappingRepo.Update(&mappings[i]); err != nil {
					return 0, err
				}
				total++
			}
			if excludedCategory == nil {
				excludedCategory, err = s.excludedProviderCategory()
				if err != nil {
					return 0, err
				}
			}
			if err := s.cleanExcludedProviderProduct(&mappings[i], excludedCategory.ID); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func (s *ProductMappingService) cleanExcludedProviderProduct(mapping *models.ProductMapping, categoryID uint) error {
	if mapping == nil || mapping.LocalProductID == 0 {
		return nil
	}
	productID := strconv.FormatUint(uint64(mapping.LocalProductID), 10)
	return s.productRepo.QuickUpdate(productID, map[string]interface{}{
		"category_id": categoryID,
		"slug":        "catalog-excluded-" + productID,
		"tags":        models.StringArray{},
		"is_active":   false,
	})
}

func (s *ProductMappingService) excludedProviderCategory() (*models.Category, error) {
	const slug = "provider-catalog-excluded"
	category, err := s.categoryRepo.GetBySlug(slug)
	if err != nil {
		return nil, err
	}
	if category != nil {
		if category.IsActive {
			category.IsActive = false
			if err := s.categoryRepo.Update(category); err != nil {
				return nil, err
			}
		}
		return category, nil
	}
	category = &models.Category{
		Slug: slug,
		NameJSON: models.JSON{
			"zh-CN": "已排除目录",
			"zh-TW": "已排除目錄",
			"en-US": "Excluded catalog",
		},
		IsActive: false,
	}
	if err := s.categoryRepo.Create(category); err != nil {
		return nil, err
	}
	if err := s.categoryRepo.UpdateActive(strconv.FormatUint(uint64(category.ID), 10), false); err != nil {
		return nil, err
	}
	category.IsActive = false
	return category, nil
}

func providerCatalogCodes(items []upstream.ProviderCatalogItem) []string {
	codes := make([]string, 0, len(items))
	for _, item := range items {
		codes = append(codes, item.Code)
	}
	return codes
}

func (s *ProductMappingService) recordProviderCatalogSyncRun(startedAt time.Time, fansServices []upstream.FansGurusService, tgxItems []upstream.TGXCommodity, result *ProviderCatalogSyncResult, syncErr error) {
	if s == nil || s.syncRunRepo == nil {
		return
	}
	status := "success"
	errMsg := ""
	if syncErr != nil {
		status = "failed"
		errMsg = syncErr.Error()
	}
	run := &models.ProviderCatalogSyncRun{
		Status:           status,
		FansGurusPulled:  len(fansServices),
		TGXPulled:        len(tgxItems),
		RawFansGurusJSON: mustJSONMap("services", fansServices),
		RawTGXJSON:       mustJSONMap("items", tgxItems),
		ErrorMessage:     errMsg,
		StartedAt:        startedAt,
		FinishedAt:       time.Now(),
	}
	if result != nil {
		run.Imported = result.Imported
		run.Updated = result.Updated
		run.Skipped = result.Skipped
		run.Deactivated = result.Deactivated
		run.FilteredTelegram = result.FilteredTelegram
		run.FilteredInactive = result.FilteredInactive
		run.FilteredPlatform = result.FilteredPlatform
		run.SupportedJSON = mustJSONMap("platforms", result.SupportedPlatforms)
	}
	_ = s.syncRunRepo.Create(run)
}

func mustJSONMap(key string, value interface{}) models.JSON {
	bytes, err := json.Marshal(value)
	if err != nil {
		return models.JSON{key: []interface{}{}}
	}
	var decoded interface{}
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		return models.JSON{key: []interface{}{}}
	}
	return models.JSON{key: decoded}
}
