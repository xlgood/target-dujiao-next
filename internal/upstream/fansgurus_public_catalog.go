package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const maxFansGurusPublicCatalogBytes = 24 << 20

var fansGurusCatalogDataStartRE = regexp.MustCompile(`(?m)\blet\s+data\s*=`)

// FansGurusCatalogDetail is the public catalog metadata that is absent from
// the authenticated services API. It is fetched without credentials.
type FansGurusCatalogDetail struct {
	Service     uint
	Name        string
	Category    string
	Description string
	AverageTime string
	ServiceType string
	Min         int
	Max         int
}

// ListCatalogDetails reads the public catalog page. The page embeds all
// service details in one data block, so this is one bounded request instead of
// a request per SKU.
func (c *FansGurusClient) ListCatalogDetails(ctx context.Context) ([]FansGurusCatalogDetail, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("fansgurus public catalog client is not configured")
	}
	pageURL, err := fansGurusPublicCatalogURL(c.baseURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create fansgurus public catalog request: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; SocialGurusHubCatalog/1.0)")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch fansgurus public catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("fetch fansgurus public catalog: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFansGurusPublicCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read fansgurus public catalog: %w", err)
	}
	if len(body) > maxFansGurusPublicCatalogBytes {
		return nil, fmt.Errorf("fansgurus public catalog exceeds %d bytes", maxFansGurusPublicCatalogBytes)
	}
	details, err := parseFansGurusCatalogDetails(body)
	if err != nil {
		return nil, fmt.Errorf("parse fansgurus public catalog: %w", err)
	}
	if len(details) == 0 {
		return nil, ErrFansGurusEmptyResult
	}
	return details, nil
}

func fansGurusPublicCatalogURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid fansgurus base url")
	}
	parsed.Path = "/zh/services"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func parseFansGurusCatalogDetails(document []byte) ([]FansGurusCatalogDetail, error) {
	match := fansGurusCatalogDataStartRE.FindIndex(document)
	if match == nil {
		return nil, fmt.Errorf("embedded catalog data not found")
	}
	parser := fansGurusLiteralParser{data: document, offset: match[1]}
	value, err := parser.parseValue()
	if err != nil {
		return nil, err
	}
	categories, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("embedded catalog is not an array")
	}
	details := make([]FansGurusCatalogDetail, 0)
	seen := make(map[uint]struct{})
	for _, rawCategory := range categories {
		category, ok := rawCategory.(map[string]any)
		if !ok {
			continue
		}
		categoryName := catalogString(category["name"])
		services, _ := category["services"].([]any)
		for _, rawService := range services {
			service, ok := rawService.(map[string]any)
			if !ok {
				continue
			}
			id, ok := catalogUint(service["id"])
			if !ok || id == 0 {
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			details = append(details, FansGurusCatalogDetail{
				Service:     id,
				Name:        catalogString(service["name"]),
				Category:    categoryName,
				Description: catalogString(service["description"]),
				AverageTime: catalogString(service["average_time"]),
				ServiceType: catalogString(service["type"]),
				Min:         catalogInt(service["min"]),
				Max:         catalogInt(service["max"]),
			})
		}
	}
	return details, nil
}

func catalogString(value any) string {
	return strings.TrimSpace(fmt.Sprint(value))
}

func catalogUint(value any) (uint, bool) {
	parsed, err := strconv.ParseUint(strings.ReplaceAll(catalogString(value), "\u00a0", ""), 10, 64)
	if err != nil || parsed == 0 || parsed > uint64(^uint(0)) {
		return 0, false
	}
	return uint(parsed), true
}

func catalogInt(value any) int {
	text := strings.NewReplacer("\u00a0", "", ",", "", " ", "").Replace(catalogString(value))
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return parsed
}

// fansGurusLiteralParser accepts the limited JavaScript literal emitted by the
// public catalog page. It never evaluates page JavaScript.
type fansGurusLiteralParser struct {
	data   []byte
	offset int
}

func (p *fansGurusLiteralParser) parseValue() (any, error) {
	p.skipSpace()
	if p.offset >= len(p.data) {
		return nil, fmt.Errorf("unexpected end of catalog data")
	}
	switch p.data[p.offset] {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"', '\'':
		return p.parseQuotedString()
	case '`':
		return p.parseTemplateString()
	default:
		return p.parseBareValue()
	}
}

func (p *fansGurusLiteralParser) parseObject() (map[string]any, error) {
	p.offset++
	result := map[string]any{}
	for {
		p.skipSpace()
		if p.offset >= len(p.data) {
			return nil, fmt.Errorf("unterminated object")
		}
		if p.data[p.offset] == '}' {
			p.offset++
			return result, nil
		}
		key, err := p.parseKey()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.offset >= len(p.data) || p.data[p.offset] != ':' {
			return nil, fmt.Errorf("expected colon after object key %q", key)
		}
		p.offset++
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		result[key] = value
		p.skipSpace()
		if p.offset < len(p.data) && p.data[p.offset] == ',' {
			p.offset++
			continue
		}
		if p.offset < len(p.data) && p.data[p.offset] == '}' {
			p.offset++
			return result, nil
		}
		return nil, fmt.Errorf("expected object separator")
	}
}

func (p *fansGurusLiteralParser) parseArray() ([]any, error) {
	p.offset++
	result := []any{}
	for {
		p.skipSpace()
		if p.offset >= len(p.data) {
			return nil, fmt.Errorf("unterminated array")
		}
		if p.data[p.offset] == ']' {
			p.offset++
			return result, nil
		}
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		result = append(result, value)
		p.skipSpace()
		if p.offset < len(p.data) && p.data[p.offset] == ',' {
			p.offset++
			continue
		}
		if p.offset < len(p.data) && p.data[p.offset] == ']' {
			p.offset++
			return result, nil
		}
		return nil, fmt.Errorf("expected array separator")
	}
}

func (p *fansGurusLiteralParser) parseKey() (string, error) {
	p.skipSpace()
	if p.offset >= len(p.data) {
		return "", fmt.Errorf("unexpected end while reading object key")
	}
	if p.data[p.offset] == '"' || p.data[p.offset] == '\'' {
		return p.parseQuotedString()
	}
	if p.data[p.offset] == '`' {
		return p.parseTemplateString()
	}
	start := p.offset
	for p.offset < len(p.data) && (unicode.IsLetter(rune(p.data[p.offset])) || unicode.IsDigit(rune(p.data[p.offset])) || p.data[p.offset] == '_' || p.data[p.offset] == '$') {
		p.offset++
	}
	if start == p.offset {
		return "", fmt.Errorf("invalid object key")
	}
	return string(p.data[start:p.offset]), nil
}

func (p *fansGurusLiteralParser) parseQuotedString() (string, error) {
	quote := p.data[p.offset]
	start := p.offset
	p.offset++
	escaped := false
	for p.offset < len(p.data) {
		current := p.data[p.offset]
		p.offset++
		if escaped {
			if current == 'u' && p.offset+3 < len(p.data) && isHex(p.data[p.offset]) && isHex(p.data[p.offset+1]) && isHex(p.data[p.offset+2]) && isHex(p.data[p.offset+3]) {
				p.offset += 4
			}
			if current == 'x' && p.offset+1 < len(p.data) && isHex(p.data[p.offset]) && isHex(p.data[p.offset+1]) {
				p.offset += 2
			}
			escaped = false
			continue
		}
		if current == '\\' {
			escaped = true
			continue
		}
		if current == quote {
			literal := string(p.data[start:p.offset])
			if quote == '\'' {
				literal = `"` + strings.ReplaceAll(strings.ReplaceAll(literal[1:len(literal)-1], `"`, `\\"`), `'`, `\"`) + `"`
			}
			// JavaScript permits escaped slashes while Go string literals do not.
			literal = strings.ReplaceAll(literal, `\/`, `/`)
			literal = normalizeJavaScriptStringEscapes(literal)
			var value string
			err := json.Unmarshal([]byte(literal), &value)
			if err != nil {
				return "", fmt.Errorf("decode string near offset %d: %w", start, err)
			}
			return value, nil
		}
	}
	return "", fmt.Errorf("unterminated string")
}

func normalizeJavaScriptStringEscapes(literal string) string {
	if len(literal) < 2 {
		return literal
	}
	var result strings.Builder
	result.Grow(len(literal))
	for index := 0; index < len(literal); index++ {
		if literal[index] != '\\' || index+1 >= len(literal) {
			result.WriteByte(literal[index])
			continue
		}
		next := literal[index+1]
		if next == 'x' && index+3 < len(literal) && isHex(literal[index+2]) && isHex(literal[index+3]) {
			result.WriteString(`\u00`)
			result.WriteByte(literal[index+2])
			result.WriteByte(literal[index+3])
			index += 3
			continue
		}
		if next == 'v' || next == '0' || (next >= '1' && next <= '7') {
			// Preserve the escaped character instead of rejecting the full
			// catalog on JavaScript-only legacy escape syntax.
			result.WriteByte(next)
			index++
			continue
		}
		result.WriteByte(literal[index])
	}
	return result.String()
}

func isHex(value byte) bool {
	return (value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')
}

func (p *fansGurusLiteralParser) parseTemplateString() (string, error) {
	p.offset++
	var result strings.Builder
	for p.offset < len(p.data) {
		current := p.data[p.offset]
		p.offset++
		if current == '`' {
			return result.String(), nil
		}
		if current != '\\' || p.offset >= len(p.data) {
			result.WriteByte(current)
			continue
		}
		next := p.data[p.offset]
		p.offset++
		switch next {
		case 'n':
			result.WriteByte('\n')
		case 'r':
			result.WriteByte('\r')
		case 't':
			result.WriteByte('\t')
		default:
			result.WriteByte(next)
		}
	}
	return "", fmt.Errorf("unterminated template string")
}

func (p *fansGurusLiteralParser) parseBareValue() (any, error) {
	start := p.offset
	for p.offset < len(p.data) && !strings.ContainsRune(",]}\r\n\t ", rune(p.data[p.offset])) {
		p.offset++
	}
	if start == p.offset {
		return nil, fmt.Errorf("invalid value")
	}
	text := string(p.data[start:p.offset])
	switch text {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if number, err := strconv.ParseFloat(text, 64); err == nil {
		return number, nil
	}
	return text, nil
}

func (p *fansGurusLiteralParser) skipSpace() {
	for p.offset < len(p.data) && unicode.IsSpace(rune(p.data[p.offset])) {
		p.offset++
	}
}
