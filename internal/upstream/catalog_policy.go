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
	tgTokenRE  = regexp.MustCompile(`(?i)(^|[^a-z0-9])tg([^a-z0-9]|$)`)
	facelookRE = regexp.MustCompile(`(?i)\bfacelook\b`)

	platformOrder = []string{
		"x", "instagram", "facebook", "tiktok", "youtube", "vk", "spotify",
		"discord", "twitch", "reddit", "linkedin", "github", "quora", "whatsapp",
		"line-voom", "threads", "gmail", "outlook", "hotmail", "overseas-email",
	}

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
		"vk":             {"vkontakte", "vk"},
		"spotify":        {"spotify"},
		"discord":        {"discord"},
		"twitch":         {"twitch"},
		"reddit":         {"reddit"},
		"linkedin":       {"linkedin", "linked in"},
		"github":         {"github", "git hub"},
		"quora":          {"quora"},
		"whatsapp":       {"whatsapp", "whats app"},
		"line-voom":      {"line voom", "linevoom"},
		"threads":        {"threads"},
		"gmail":          {"gmail", "google mail", "google account"},
		"outlook":        {"outlook"},
		"hotmail":        {"hotmail"},
		"overseas-email": {"overseas email", "email account", "email", "mailbox"},
		"telegram": {
			"telegram",
			"电报",
			"飞机",
			"纸飞机",
			"t.me",
		},
	}

	fansGurusAllowedPlatforms = platformSetFromNames([]string{
		"x", "instagram", "facebook", "tiktok", "youtube", "vk", "spotify", "discord",
		"twitch", "reddit", "linkedin", "github", "quora", "whatsapp", "line-voom", "threads",
	})
	tgxAllowedPlatforms = platformSetFromNames([]string{
		"x", "facebook", "instagram", "youtube", "tiktok", "gmail", "threads", "linkedin",
		"github", "reddit", "discord", "outlook", "hotmail", "overseas-email",
	})
)

type ProviderCatalogItem struct {
	Provider           string
	Code               string
	Name               string
	Category           string
	Description        string
	Type               string
	Tags               []string
	RawText            []string
	UpstreamPrice      string
	TargetPrice        string
	PriceQuantityBasis int
	MinQuantity        int
	MaxQuantity        int
	Variants           []ProviderCatalogVariant
	ManualSchema       map[string]interface{}
	Images             []string
	SortOrder          int
	Active             bool
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
	return ProviderCatalogItem{
		Provider:      CatalogProviderFansGurus,
		Code:          uintToString(service.Service),
		Name:          service.Name,
		Category:      service.Category,
		Type:          service.Type,
		UpstreamPrice: service.Rate,
		// The connection's configurable exchange/markup settings determine the
		// local sale price during import. Keep the upstream amount unchanged here.
		TargetPrice:        service.Rate,
		PriceQuantityBasis: 1000,
		MinQuantity:        service.Min,
		MaxQuantity:        service.Max,
		Active:             FansGurusServiceTypeSupported(service.Type),
	}, nil
}

// FansGurusServiceTypeSupported is intentionally conservative: procurement
// currently submits only link + quantity, which matches the Default service.
// Other service types require additional user input and must not be sold yet.
func FansGurusServiceTypeSupported(serviceType string) bool {
	normalized := strings.ToLower(strings.TrimSpace(serviceType))
	switch normalized {
	case "", "default", "custom comments", "poll", "invites from groups", "subscriptions":
		return true
	default:
		return false
	}
}

func NewTGXCatalogItem(commodity TGXCommodity) (ProviderCatalogItem, error) {
	variants, err := ParseTGXConfigVariants(commodity.Code, commodity.Price, commodity.Config)
	if err != nil {
		return ProviderCatalogItem{}, err
	}
	manualSchema := ParseTGXWidgetManualSchema(commodity.Widget)
	addTGXContactField(manualSchema, commodity.ContactType)
	images := make([]string, 0, 1)
	if cover := strings.TrimSpace(commodity.Cover); cover != "" {
		images = append(images, cover)
	}
	return ProviderCatalogItem{
		Provider:      CatalogProviderTGX,
		Code:          commodity.Code,
		Name:          normalizeProviderTitle(commodity.Name),
		Category:      commodity.Category,
		Description:   commodity.Description,
		RawText:       []string{string(commodity.Config), string(commodity.Widget)},
		UpstreamPrice: commodity.Price,
		// TGX quotes prices in CNY. Conversion to the site's currency depends on
		// the configured connection rate and happens during catalog import.
		TargetPrice:        commodity.Price,
		PriceQuantityBasis: 1,
		MinQuantity:        commodity.Minimum,
		Variants:           variants,
		ManualSchema:       manualSchema,
		Images:             images,
		SortOrder:          commodity.Sort,
		Active:             true,
	}, nil
}

func ParseTGXConfigVariants(sharedCode string, fallbackPrice string, raw json.RawMessage) ([]ProviderCatalogVariant, error) {
	entries := parseTGXConfigEntries(raw)
	if len(entries) == 0 {
		return []ProviderCatalogVariant{{
			Code:          sharedCode,
			Name:          "default",
			UpstreamPrice: fallbackPrice,
			TargetPrice:   fallbackPrice,
			Stock:         -1,
			Active:        true,
		}}, nil
	}

	variants := make([]ProviderCatalogVariant, 0, len(entries))
	for name, price := range entries {
		variants = append(variants, ProviderCatalogVariant{
			Code:          sharedCode + "|" + name,
			Name:          name,
			UpstreamPrice: price,
			TargetPrice:   price,
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
	fansAllowed, fansFiltered := filterAllowedPlatforms(fansActive, fansGurusAllowedPlatforms)
	tgxAllowed, tgxFiltered := filterAllowedPlatforms(tgxActive, tgxAllowedPlatforms)

	result := FilteredCatalog{
		SupportedPlatforms: append(platformNames(fansAllowed), platformNames(tgxAllowed)...),
		FilteredTelegram:   append(fansTelegram, tgxTelegram...),
		FilteredInactive:   append(fansInactive, tgxInactive...),
		FilteredPlatform:   append(fansFiltered, tgxFiltered...),
	}
	result.SupportedPlatforms = uniqueSortedStrings(result.SupportedPlatforms)
	result.FansGurus = fansAllowed
	result.TGX = tgxAllowed
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
	for _, platform := range platformOrder {
		aliases := platformAliases[platform]
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
	if platform := NormalizePlatform(i.Name); platform != "" {
		return platform
	}
	return NormalizePlatform(i.Category)
}

func normalizeProviderTitle(title string) string {
	return facelookRE.ReplaceAllString(strings.TrimSpace(title), "Facebook")
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

func filterAllowedPlatforms(items []ProviderCatalogItem, allowed map[string]struct{}) ([]ProviderCatalogItem, []ProviderCatalogItem) {
	kept := make([]ProviderCatalogItem, 0, len(items))
	filtered := make([]ProviderCatalogItem, 0)
	for _, item := range items {
		if _, ok := allowed[item.Platform()]; ok {
			kept = append(kept, item)
			continue
		}
		filtered = append(filtered, item)
	}
	return kept, filtered
}

func platformSetFromNames(names []string) map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}

func platformNames(items []ProviderCatalogItem) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		if platform := item.Platform(); platform != "" {
			result = append(result, platform)
		}
	}
	return result
}

func uniqueSortedStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
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
	text = decodeJSONStringValue(text)

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
	text = decodeJSONStringValue(text)

	var list []map[string]interface{}
	if err := json.Unmarshal([]byte(text), &list); err == nil {
		return normalizeTGXWidgetFieldList(list)
	}

	var object map[string]interface{}
	if err := json.Unmarshal([]byte(text), &object); err == nil {
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

func decodeJSONStringValue(text string) string {
	var decoded string
	if err := json.Unmarshal([]byte(text), &decoded); err == nil {
		return decoded
	}
	return text
}

func normalizeTGXWidgetFieldList(list []map[string]interface{}) []map[string]interface{} {
	fields := make([]map[string]interface{}, 0, len(list))
	for _, item := range list {
		key := firstStringValue(item, "key", "name", "field")
		if key == "" {
			continue
		}
		fieldType := normalizeTGXWidgetFieldType(firstStringValue(item, "type", "input_type"), item)
		label := firstStringValue(item, "label", "title", "cn", "name")
		if label == "" {
			label = key
		}
		field := map[string]interface{}{
			"key":      key,
			"type":     fieldType,
			"label":    label,
			"required": boolValue(item["required"]),
		}
		if fieldType == "select" {
			field["options"] = tgxWidgetOptions(item)
		}
		fields = append(fields, field)
	}
	return fields
}

func normalizeTGXWidgetFieldType(raw string, item map[string]interface{}) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "select":
		if len(tgxWidgetOptions(item)) > 0 {
			return "select"
		}
		// TGX's documented select example does not include options. A text input
		// preserves a checkout path instead of creating an invalid local schema.
		return "text"
	case "input", "", "text", "textarea", "phone", "email", "number", "radio", "checkbox":
		if strings.TrimSpace(raw) == "" || strings.EqualFold(strings.TrimSpace(raw), "input") {
			return "text"
		}
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "text"
	}
}

func tgxWidgetOptions(item map[string]interface{}) []string {
	for _, key := range []string{"options", "option", "values"} {
		raw, ok := item[key]
		if !ok {
			continue
		}
		var values []string
		switch typed := raw.(type) {
		case []interface{}:
			for _, value := range typed {
				if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
					values = append(values, text)
				}
			}
		case []string:
			for _, value := range typed {
				if text := strings.TrimSpace(value); text != "" {
					values = append(values, text)
				}
			}
		case string:
			for _, value := range strings.Split(typed, ",") {
				if text := strings.TrimSpace(value); text != "" {
					values = append(values, text)
				}
			}
		}
		return values
	}
	return nil
}

func addTGXContactField(schema map[string]interface{}, contactType string) {
	var fieldType string
	switch strings.TrimSpace(contactType) {
	case "1":
		fieldType = "email"
	case "2":
		fieldType = "phone"
	default:
		return
	}

	fields, _ := schema["fields"].([]map[string]interface{})
	for _, field := range fields {
		if strings.TrimSpace(fmt.Sprint(field["key"])) == "contact" {
			return
		}
	}
	schema["fields"] = append(fields, map[string]interface{}{
		"key":      "contact",
		"type":     fieldType,
		"label":    "Contact",
		"required": true,
	})
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
