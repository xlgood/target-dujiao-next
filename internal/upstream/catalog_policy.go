package upstream

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	CatalogProviderFansGurus = "fansgurus"
	CatalogProviderTGX       = "tgx"
)

var (
	tgTokenRE = regexp.MustCompile(`(?i)(^|[^a-z0-9])tg([^a-z0-9]|$)`)

	platformAliases = map[string][]string{
		"x": {
			"twitter / x",
			"twitter/x",
			"twitter",
			"x",
		},
		"instagram": {
			"instagram",
			"insta",
			"ig",
			"ins",
		},
		"tiktok": {
			"tik tok",
			"tiktok",
		},
		"facebook": {
			"facebook",
			"fb",
		},
		"youtube": {
			"youtube",
			"you tube",
			"yt",
		},
		"telegram": {
			"telegram",
			"电报",
			"飞机",
			"纸飞机",
			"t.me",
		},
	}
)

type ProviderCatalogItem struct {
	Provider      string
	Code          string
	Name          string
	Category      string
	Description   string
	Type          string
	Tags          []string
	RawText       []string
	UpstreamPrice string
	TargetPrice   string
	MinQuantity   int
	MaxQuantity   int
	Variants      []ProviderCatalogVariant
	ManualSchema  map[string]interface{}
	Active        bool
}

type ProviderCatalogVariant struct {
	Code          string
	Name          string
	UpstreamPrice string
	TargetPrice   string
	Stock         int
	Active        bool
}

type FilteredCatalog struct {
	SupportedPlatforms []string
	FansGurus          []ProviderCatalogItem
	TGX                []ProviderCatalogItem
	FilteredTelegram   []ProviderCatalogItem
	FilteredInactive   []ProviderCatalogItem
	FilteredPlatform   []ProviderCatalogItem
}

func NewFansGurusCatalogItem(service FansGurusService) (ProviderCatalogItem, error) {
	targetRate, err := FansGurusTargetRate(service.Rate)
	if err != nil {
		return ProviderCatalogItem{}, err
	}
	return ProviderCatalogItem{
		Provider:      CatalogProviderFansGurus,
		Code:          uintToString(service.Service),
		Name:          service.Name,
		Category:      service.Category,
		Type:          service.Type,
		UpstreamPrice: service.Rate,
		TargetPrice:   targetRate,
		MinQuantity:   service.Min,
		MaxQuantity:   service.Max,
		Active:        true,
	}, nil
}

func NewTGXCatalogItem(commodity TGXCommodity) (ProviderCatalogItem, error) {
	targetPrice, err := TGXTargetPrice(commodity.Price)
	if err != nil {
		return ProviderCatalogItem{}, err
	}
	variants, err := ParseTGXConfigVariants(commodity.Code, commodity.Price, commodity.Config)
	if err != nil {
		return ProviderCatalogItem{}, err
	}
	manualSchema := ParseTGXWidgetManualSchema(commodity.Widget)
	return ProviderCatalogItem{
		Provider:      CatalogProviderTGX,
		Code:          commodity.Code,
		Name:          commodity.Name,
		Description:   commodity.Description,
		RawText:       []string{string(commodity.Config), string(commodity.Widget)},
		UpstreamPrice: commodity.Price,
		TargetPrice:   targetPrice,
		MinQuantity:   commodity.Minimum,
		Variants:      variants,
		ManualSchema:  manualSchema,
		Active:        true,
	}, nil
}

func ParseTGXConfigVariants(sharedCode string, fallbackPrice string, raw json.RawMessage) ([]ProviderCatalogVariant, error) {
	entries := parseTGXConfigEntries(raw)
	if len(entries) == 0 {
		target, err := TGXTargetPrice(fallbackPrice)
		if err != nil {
			return nil, err
		}
		return []ProviderCatalogVariant{{
			Code:          sharedCode,
			Name:          "default",
			UpstreamPrice: fallbackPrice,
			TargetPrice:   target,
			Stock:         -1,
			Active:        true,
		}}, nil
	}

	variants := make([]ProviderCatalogVariant, 0, len(entries))
	for name, price := range entries {
		target, err := TGXTargetPrice(price)
		if err != nil {
			return nil, fmt.Errorf("parse tgx race price %s: %w", name, err)
		}
		variants = append(variants, ProviderCatalogVariant{
			Code:          sharedCode + "|" + name,
			Name:          name,
			UpstreamPrice: price,
			TargetPrice:   target,
			Stock:         -1,
			Active:        true,
		})
	}
	sort.Slice(variants, func(i, j int) bool { return variants[i].Name < variants[j].Name })
	return variants, nil
}

func ParseTGXWidgetManualSchema(raw json.RawMessage) map[string]interface{} {
	fields := parseTGXWidgetFields(raw)
	return map[string]interface{}{"fields": fields}
}

func BuildFilteredCatalog(fansGurus, tgx []ProviderCatalogItem) FilteredCatalog {
	fansClean, fansTelegram := filterTelegramItems(fansGurus)
	tgxClean, tgxTelegram := filterTelegramItems(tgx)
	fansActive, fansInactive := filterActiveItems(fansClean)
	tgxActive, tgxInactive := filterActiveItems(tgxClean)
	supported := intersectPlatforms(platformSet(fansActive), platformSet(tgxActive))

	result := FilteredCatalog{
		SupportedPlatforms: supported,
		FilteredTelegram:   append(fansTelegram, tgxTelegram...),
		FilteredInactive:   append(fansInactive, tgxInactive...),
	}
	result.FansGurus, result.FilteredPlatform = filterSupportedPlatforms(fansActive, supported, result.FilteredPlatform)
	result.TGX, result.FilteredPlatform = filterSupportedPlatforms(tgxActive, supported, result.FilteredPlatform)
	return result
}

func NormalizePlatform(parts ...string) string {
	text := normalizeCatalogText(strings.Join(parts, " "))
	if text == "" {
		return ""
	}
	if ContainsTelegramCatalogText(text) {
		return "telegram"
	}
	for platform, aliases := range platformAliases {
		if platform == "telegram" {
			continue
		}
		for _, alias := range aliases {
			if catalogTextContainsAlias(text, alias) {
				return platform
			}
		}
	}
	return ""
}

func ContainsTelegramCatalogText(parts ...string) bool {
	text := normalizeCatalogText(strings.Join(parts, " "))
	if text == "" {
		return false
	}
	if strings.Contains(text, "telegram") ||
		strings.Contains(text, "t.me") ||
		strings.Contains(text, "电报") ||
		strings.Contains(text, "纸飞机") ||
		strings.Contains(text, "飞机") {
		return true
	}
	return tgTokenRE.MatchString(text)
}

func (i ProviderCatalogItem) Platform() string {
	parts := []string{i.Category, i.Name, i.Description, i.Code, i.Type}
	parts = append(parts, i.Tags...)
	parts = append(parts, i.RawText...)
	return NormalizePlatform(parts...)
}

func (i ProviderCatalogItem) ContainsTelegram() bool {
	parts := []string{i.Category, i.Name, i.Description, i.Code, i.Type}
	parts = append(parts, i.Tags...)
	parts = append(parts, i.RawText...)
	return ContainsTelegramCatalogText(parts...)
}

func filterTelegramItems(items []ProviderCatalogItem) ([]ProviderCatalogItem, []ProviderCatalogItem) {
	kept := make([]ProviderCatalogItem, 0, len(items))
	filtered := make([]ProviderCatalogItem, 0)
	for _, item := range items {
		if item.ContainsTelegram() {
			filtered = append(filtered, item)
			continue
		}
		kept = append(kept, item)
	}
	return kept, filtered
}

func filterActiveItems(items []ProviderCatalogItem) ([]ProviderCatalogItem, []ProviderCatalogItem) {
	kept := make([]ProviderCatalogItem, 0, len(items))
	filtered := make([]ProviderCatalogItem, 0)
	for _, item := range items {
		if !item.Active {
			filtered = append(filtered, item)
			continue
		}
		kept = append(kept, item)
	}
	return kept, filtered
}

func platformSet(items []ProviderCatalogItem) map[string]struct{} {
	set := make(map[string]struct{})
	for _, item := range items {
		if platform := item.Platform(); platform != "" {
			set[platform] = struct{}{}
		}
	}
	return set
}

func intersectPlatforms(left, right map[string]struct{}) []string {
	result := make([]string, 0)
	for platform := range left {
		if _, ok := right[platform]; ok {
			result = append(result, platform)
		}
	}
	sort.Strings(result)
	return result
}

func filterSupportedPlatforms(items []ProviderCatalogItem, supported []string, filtered []ProviderCatalogItem) ([]ProviderCatalogItem, []ProviderCatalogItem) {
	supportedSet := make(map[string]struct{}, len(supported))
	for _, platform := range supported {
		supportedSet[platform] = struct{}{}
	}
	kept := make([]ProviderCatalogItem, 0, len(items))
	for _, item := range items {
		if _, ok := supportedSet[item.Platform()]; ok {
			kept = append(kept, item)
			continue
		}
		filtered = append(filtered, item)
	}
	return kept, filtered
}

func catalogTextContainsAlias(text, alias string) bool {
	alias = normalizeCatalogText(alias)
	if alias == "" {
		return false
	}
	if len(alias) <= 2 && isASCIIAlphaNum(alias) {
		pattern := regexp.MustCompile(`(^|[^a-z0-9])` + regexp.QuoteMeta(alias) + `([^a-z0-9]|$)`)
		return pattern.MatchString(text)
	}
	return strings.Contains(text, alias)
}

func normalizeCatalogText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func parseTGXConfigEntries(raw json.RawMessage) map[string]string {
	result := make(map[string]string)
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return result
	}

	var object map[string]interface{}
	if err := json.Unmarshal(raw, &object); err == nil {
		for key, value := range object {
			if race := extractTGXRaceName(key); race != "" {
				result[race] = strings.TrimSpace(fmt.Sprint(value))
			}
		}
		return result
	}

	var list []map[string]interface{}
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, item := range list {
			race := firstStringValue(item, "race", "name", "label", "title")
			price := firstStringValue(item, "price", "amount", "value")
			if race != "" && price != "" {
				result[race] = price
			}
		}
		return result
	}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if race := extractTGXRaceName(parts[0]); race != "" {
			result[race] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

func extractTGXRaceName(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if strings.HasPrefix(key, "category[") && strings.HasSuffix(key, "]") {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(key, "category["), "]"))
	}
	if strings.HasPrefix(key, "race[") && strings.HasSuffix(key, "]") {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(key, "race["), "]"))
	}
	return key
}

func parseTGXWidgetFields(raw json.RawMessage) []map[string]interface{} {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return []map[string]interface{}{}
	}

	var list []map[string]interface{}
	if err := json.Unmarshal(raw, &list); err == nil {
		return normalizeTGXWidgetFieldList(list)
	}

	var object map[string]interface{}
	if err := json.Unmarshal(raw, &object); err == nil {
		if nested, ok := object["fields"].([]interface{}); ok {
			list = make([]map[string]interface{}, 0, len(nested))
			for _, rawItem := range nested {
				if item, ok := rawItem.(map[string]interface{}); ok {
					list = append(list, item)
				}
			}
			return normalizeTGXWidgetFieldList(list)
		}
		return normalizeTGXWidgetFieldList([]map[string]interface{}{object})
	}

	return []map[string]interface{}{}
}

func normalizeTGXWidgetFieldList(list []map[string]interface{}) []map[string]interface{} {
	fields := make([]map[string]interface{}, 0, len(list))
	for _, item := range list {
		key := firstStringValue(item, "key", "name", "field")
		if key == "" {
			continue
		}
		fieldType := strings.ToLower(firstStringValue(item, "type", "input_type"))
		if fieldType == "" {
			fieldType = "text"
		}
		label := firstStringValue(item, "label", "title", "name")
		if label == "" {
			label = key
		}
		fields = append(fields, map[string]interface{}{
			"key":      key,
			"type":     fieldType,
			"label":    label,
			"required": boolValue(item["required"]),
		})
	}
	return fields
}

func firstStringValue(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func boolValue(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		normalized := strings.ToLower(strings.TrimSpace(v))
		return normalized == "true" || normalized == "1" || normalized == "yes" || normalized == "required"
	case float64:
		return v != 0
	default:
		return false
	}
}

func isASCIIAlphaNum(value string) bool {
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return value != ""
}

func uintToString(value uint) string {
	return strconv.FormatUint(uint64(value), 10)
}
