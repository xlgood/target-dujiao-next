package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const defaultTGXBaseURL = "https://www.tgxaccount.com/shared"

var (
	ErrTGXAuth     = errors.New("tgx auth error")
	ErrTGXBusiness = errors.New("tgx business error")
	// ErrTGXUnavailable means TGX has authoritatively reported that a
	// commodity is sold out or discontinued, rather than a request failure.
	ErrTGXUnavailable = errors.New("tgx commodity unavailable")
	ErrTGXStock       = errors.New("tgx stock insufficient")
	ErrTGXBadJSON     = errors.New("tgx bad json")
	ErrTGXBadPayload  = errors.New("tgx bad payload")
)

type TGXClient struct {
	baseURL string
	appID   string
	appKey  string
	client  *http.Client
}

type TGXClientOption func(*TGXClient)

func WithTGXHTTPClient(client *http.Client) TGXClientOption {
	return func(c *TGXClient) {
		if client != nil {
			c.client = client
		}
	}
}

func NewTGXClient(baseURL, appID, appKey string, opts ...TGXClientOption) *TGXClient {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultTGXBaseURL
	}
	c := &TGXClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		appID:   strings.TrimSpace(appID),
		appKey:  strings.TrimSpace(appKey),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type TGXConnectResponse struct {
	ShopName string `json:"shopName,omitempty"`
	Balance  string `json:"balance,omitempty"`
}

func (r *TGXConnectResponse) UnmarshalJSON(data []byte) error {
	var decoded struct {
		ShopName       string          `json:"shopName"`
		LegacyShopName string          `json:"shop_name"`
		Balance        json.RawMessage `json:"balance"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	r.ShopName = strings.TrimSpace(decoded.ShopName)
	if r.ShopName == "" {
		r.ShopName = strings.TrimSpace(decoded.LegacyShopName)
	}
	balance, err := decodeTGXStringOrNumber(decoded.Balance)
	if err != nil {
		return err
	}
	r.Balance = balance
	return nil
}

type TGXCommodity struct {
	ID              json.Number     `json:"id,omitempty"`
	Code            string          `json:"code"`
	Name            string          `json:"name"`
	Category        string          `json:"-"`
	Description     string          `json:"description,omitempty"`
	Price           string          `json:"price"`
	UserPrice       string          `json:"user_price,omitempty"`
	FactoryPrice    string          `json:"factory_price,omitempty"`
	Cover           string          `json:"cover,omitempty"`
	DeliveryWay     string          `json:"delivery_way,omitempty"`
	ContactType     string          `json:"contact_type,omitempty"`
	PasswordStatus  string          `json:"password_status,omitempty"`
	Config          json.RawMessage `json:"config,omitempty"`
	Widget          json.RawMessage `json:"widget,omitempty"`
	DraftStatus     string          `json:"draft_status,omitempty"`
	InventoryHidden string          `json:"inventory_hidden,omitempty"`
	Minimum         int             `json:"minimum,omitempty"`
	PurchaseCount   int             `json:"purchase_count,omitempty"`
	Sort            int             `json:"sort,omitempty"`
}

func (c *TGXCommodity) UnmarshalJSON(data []byte) error {
	type commodityAlias TGXCommodity
	var decoded struct {
		*commodityAlias
		Price           json.RawMessage `json:"price"`
		UserPrice       json.RawMessage `json:"user_price"`
		FactoryPrice    json.RawMessage `json:"factory_price"`
		DeliveryWay     json.RawMessage `json:"delivery_way"`
		ContactType     json.RawMessage `json:"contact_type"`
		PasswordStatus  json.RawMessage `json:"password_status"`
		DraftStatus     json.RawMessage `json:"draft_status"`
		InventoryHidden json.RawMessage `json:"inventory_hidden"`
	}
	decoded.commodityAlias = (*commodityAlias)(c)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var err error
	for _, field := range []struct {
		raw  json.RawMessage
		dest *string
	}{
		{decoded.Price, &c.Price},
		{decoded.UserPrice, &c.UserPrice},
		{decoded.FactoryPrice, &c.FactoryPrice},
		{decoded.DeliveryWay, &c.DeliveryWay},
		{decoded.ContactType, &c.ContactType},
		{decoded.PasswordStatus, &c.PasswordStatus},
		{decoded.DraftStatus, &c.DraftStatus},
		{decoded.InventoryHidden, &c.InventoryHidden},
	} {
		*field.dest, err = decodeTGXStringOrNumber(field.raw)
		if err != nil {
			return err
		}
	}
	return nil
}

func decodeTGXStringOrNumber(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return "", fmt.Errorf("decode tgx scalar %s: %w", string(raw), err)
	}
	return number.String(), nil
}

func decodeTGXInt(raw json.RawMessage) (int, error) {
	value, err := decodeTGXStringOrNumber(raw)
	if err != nil || value == "" {
		return 0, err
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse tgx integer %q: %w", value, err)
	}
	return parsed, nil
}

type TGXItemsResponse struct {
	Items      []TGXCommodity  `json:"items,omitempty"`
	Categories json.RawMessage `json:"categories,omitempty"`
	Raw        json.RawMessage `json:"-"`
}

type tgxCatalogCategory struct {
	Name     string         `json:"name"`
	Children []TGXCommodity `json:"children"`
}

type TGXInventoryResponse struct {
	Code   string          `json:"code,omitempty"`
	Race   string          `json:"race,omitempty"`
	Count  int             `json:"count,omitempty"`
	Price  string          `json:"price,omitempty"`
	Widget json.RawMessage `json:"widget,omitempty"`
	Config json.RawMessage `json:"config,omitempty"`
	Raw    json.RawMessage `json:"-"`
}

// UnmarshalJSON accepts the mixed scalar types used by TGX inventory
// responses. The same API may return stock as either 27 or "27".
func (r *TGXInventoryResponse) UnmarshalJSON(data []byte) error {
	type inventoryAlias TGXInventoryResponse
	var decoded struct {
		*inventoryAlias
		Count json.RawMessage `json:"count"`
		Price json.RawMessage `json:"price"`
	}
	decoded.inventoryAlias = (*inventoryAlias)(r)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var err error
	if r.Price, err = decodeTGXStringOrNumber(decoded.Price); err != nil {
		return err
	}
	if r.Count, err = decodeTGXInt(decoded.Count); err != nil {
		return err
	}
	return nil
}

type TGXInventoryStateResponse struct {
	Code      string `json:"code,omitempty"`
	Race      string `json:"race,omitempty"`
	Quantity  int    `json:"quantity,omitempty"`
	Available bool   `json:"available,omitempty"`
	State     string `json:"state,omitempty"`
	Message   string `json:"message,omitempty"`
}

type TGXTradeRequest struct {
	SharedCode string
	Race       string
	Quantity   int
	RequestNo  string
	Widget     map[string]string
}

type TGXTradeResponse struct {
	TradeNo string          `json:"trade_no"`
	Secret  string          `json:"secret,omitempty"`
	Widget  json.RawMessage `json:"widget,omitempty"`
	Status  string          `json:"status,omitempty"`
}

func (r *TGXTradeResponse) UnmarshalJSON(data []byte) error {
	type tradeAlias TGXTradeResponse
	var decoded struct {
		*tradeAlias
		Status json.RawMessage `json:"status"`
	}
	decoded.tradeAlias = (*tradeAlias)(r)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	status, err := decodeTGXStringOrNumber(decoded.Status)
	if err != nil {
		return err
	}
	r.Status = status
	return nil
}

type TGXQueryResponse struct {
	TradeNo        string          `json:"trade_no"`
	Secret         string          `json:"secret,omitempty"`
	Widget         json.RawMessage `json:"widget,omitempty"`
	Status         string          `json:"status,omitempty"`
	DeliveryStatus string          `json:"delivery_status,omitempty"`
}

func (r *TGXQueryResponse) UnmarshalJSON(data []byte) error {
	type queryAlias TGXQueryResponse
	var decoded struct {
		*queryAlias
		Status         json.RawMessage `json:"status"`
		DeliveryStatus json.RawMessage `json:"delivery_status"`
	}
	decoded.queryAlias = (*queryAlias)(r)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	status, err := decodeTGXStringOrNumber(decoded.Status)
	if err != nil {
		return err
	}
	r.Status = status
	if r.DeliveryStatus, err = decodeTGXStringOrNumber(decoded.DeliveryStatus); err != nil {
		return err
	}
	return nil
}

type TGXError struct {
	Kind       error
	StatusCode int
	Code       string
	Message    string
}

func (e *TGXError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("tgx request failed: status=%d code=%s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("tgx request failed: code=%s: %s", e.Code, e.Message)
}

func (e *TGXError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

func (c *TGXClient) Connect(ctx context.Context) (*TGXConnectResponse, error) {
	var result TGXConnectResponse
	if err := c.postForm(ctx, "/authentication/connect", nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *TGXClient) ListItems(ctx context.Context) (*TGXItemsResponse, error) {
	var result TGXItemsResponse
	if err := c.postForm(ctx, "/commodity/items", nil, &result); err != nil {
		return nil, err
	}
	c.normalizeCommodityCovers(result.Items)
	return &result, nil
}

func (c *TGXClient) GetItem(ctx context.Context, sharedCode string) (*TGXCommodity, error) {
	// The single-item endpoint uses snake_case, unlike the legacy inventory
	// endpoint. Keep this aligned with the documented request contract.
	values := url.Values{"shared_code": []string{sharedCode}}
	var result TGXCommodity
	if err := c.postForm(ctx, "/commodity/item", values, &result); err != nil {
		return nil, err
	}
	result.Cover = c.resolvePublicURL(result.Cover)
	return &result, nil
}

func (c *TGXClient) normalizeCommodityCovers(items []TGXCommodity) {
	for i := range items {
		items[i].Cover = c.resolvePublicURL(items[i].Cover)
	}
}

func (c *TGXClient) resolvePublicURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	assetURL, err := url.Parse(raw)
	if err != nil || assetURL.IsAbs() {
		return raw
	}
	baseURL, err := url.Parse(c.baseURL)
	if err != nil {
		return raw
	}
	return baseURL.ResolveReference(assetURL).String()
}

// DownloadImage stores a TGX cover locally so storefront rendering does not
// depend on the upstream server's referrer or hotlink rules.
func (c *TGXClient) DownloadImage(ctx context.Context, rawURL, uploadsDir string) (string, error) {
	imageURL := c.resolvePublicURL(rawURL)
	if imageURL == "" {
		return "", fmt.Errorf("tgx image URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", fmt.Errorf("create TGX image request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download TGX image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download TGX image: status %d", resp.StatusCode)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf("download TGX image: unexpected content type %q", contentType)
	}

	parsed, _ := url.Parse(imageURL)
	ext := strings.ToLower(filepath.Ext(parsed.Path))
	if ext == "" || len(ext) > 6 || mime.TypeByExtension(ext) == "" {
		extensions, _ := mime.ExtensionsByType(contentType)
		if len(extensions) > 0 {
			ext = extensions[0]
		} else {
			ext = ".img"
		}
	}
	dir := filepath.Join(uploadsDir, "upstream")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create TGX image directory: %w", err)
	}
	filename := uuid.NewString() + ext
	path := filepath.Join(dir, filename)
	file, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create TGX image file: %w", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, io.LimitReader(resp.Body, 10<<20)); err != nil {
		return "", fmt.Errorf("write TGX image: %w", err)
	}
	return "/uploads/upstream/" + filename, nil
}

func (c *TGXClient) GetInventory(ctx context.Context, sharedCode, race string) (*TGXInventoryResponse, error) {
	values := url.Values{"sharedCode": []string{sharedCode}}
	setNonEmpty(values, "race", race)
	var result TGXInventoryResponse
	if err := c.postForm(ctx, "/commodity/inventory", values, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *TGXClient) GetInventoryState(ctx context.Context, sharedCode, race string, quantity int) (*TGXInventoryStateResponse, error) {
	values := url.Values{"shared_code": []string{sharedCode}}
	setNonEmpty(values, "race", race)
	setPositiveInt(values, "num", quantity)
	var result TGXInventoryStateResponse
	if err := c.postForm(ctx, "/commodity/inventoryState", values, &result); err != nil {
		var providerErr *TGXError
		if errors.As(err, &providerErr) && providerErr.Code == "500" {
			return nil, fmt.Errorf("%w: %v", ErrTGXStock, err)
		}
		return nil, err
	}
	// TGX documents this endpoint as status-code based: HTTP/API success means
	// the requested quantity is purchasable, while insufficient stock returns
	// code 500. Some installations therefore return no availability field.
	result.Available = true
	return &result, nil
}

func (c *TGXClient) Trade(ctx context.Context, req TGXTradeRequest) (*TGXTradeResponse, error) {
	values := url.Values{"shared_code": []string{req.SharedCode}}
	setNonEmpty(values, "race", req.Race)
	setPositiveInt(values, "num", req.Quantity)
	setNonEmpty(values, "request_no", req.RequestNo)
	for key, value := range req.Widget {
		setNonEmpty(values, key, value)
	}

	var result TGXTradeResponse
	if err := c.postForm(ctx, "/commodity/trade", values, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *TGXClient) QueryTrade(ctx context.Context, tradeNo string) (*TGXQueryResponse, error) {
	values := url.Values{"tradeNo": []string{tradeNo}}
	var result TGXQueryResponse
	if err := c.postForm(ctx, "/commodity/query", values, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *TGXClient) postForm(ctx context.Context, path string, values url.Values, result interface{}) error {
	values = cloneURLValues(values)
	values.Set("app_id", c.appID)
	values.Set("app_key", c.appKey)
	values.Set("sign", SignTGX(values, c.appKey))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("create tgx request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("send tgx request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read tgx response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &TGXError{
			Kind:       ErrTGXBusiness,
			StatusCode: resp.StatusCode,
			Message:    redactSecret(string(body), c.appKey),
		}
	}

	if err := decodeTGXResponse(body, c.appKey, result); err != nil {
		return err
	}
	return nil
}

func decodeTGXResponse(body []byte, appKey string, result interface{}) error {
	var wrapper struct {
		Code    interface{}       `json:"code"`
		Status  interface{}       `json:"status"`
		Message string            `json:"message"`
		Msg     string            `json:"msg"`
		Data    json.RawMessage   `json:"data"`
		Result  json.RawMessage   `json:"result"`
		Items   []TGXCommodity    `json:"items"`
		List    []TGXCommodity    `json:"list"`
		Payload json.RawMessage   `json:"payload"`
		Raw     *json.RawMessage  `json:"-"`
		Extra   map[string]string `json:"-"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&wrapper); err != nil {
		return &TGXError{Kind: ErrTGXBadJSON, Message: redactSecret(err.Error(), appKey)}
	}

	code := normalizeTGXCode(wrapper.Code)
	if code == "" {
		code = normalizeTGXCode(wrapper.Status)
	}
	msg := strings.TrimSpace(wrapper.Message)
	if msg == "" {
		msg = strings.TrimSpace(wrapper.Msg)
	}
	if code != "" && code != "200" && !strings.EqualFold(code, "success") && !strings.EqualFold(code, "ok") {
		return &TGXError{
			Kind:    classifyTGXError(msg),
			Code:    code,
			Message: redactSecret(msg, appKey),
		}
	}

	payload := wrapper.Data
	if len(payload) == 0 || string(payload) == "null" {
		payload = wrapper.Result
	}
	if len(payload) == 0 || string(payload) == "null" {
		payload = wrapper.Payload
	}
	if len(payload) == 0 || string(payload) == "null" {
		payload = body
	}

	if itemsResp, ok := result.(*TGXItemsResponse); ok {
		itemsResp.Raw = append(itemsResp.Raw[:0], payload...)
		if len(wrapper.Items) > 0 {
			itemsResp.Items = wrapper.Items
			return nil
		}
		if len(wrapper.List) > 0 {
			itemsResp.Items = wrapper.List
			return nil
		}
		if items, categories, ok := decodeTGXCatalogCategories(payload); ok {
			itemsResp.Items = items
			itemsResp.Categories = append(itemsResp.Categories[:0], categories...)
			return nil
		}
	}
	if invResp, ok := result.(*TGXInventoryResponse); ok {
		invResp.Raw = append(invResp.Raw[:0], payload...)
	}

	if result != nil {
		if err := json.Unmarshal(payload, result); err != nil {
			return &TGXError{Kind: ErrTGXBadPayload, Message: redactSecret(err.Error(), appKey)}
		}
	}
	return nil
}

// decodeTGXCatalogCategories handles TGX's documented catalog shape: an
// outer category array whose children contain the purchasable products.
func decodeTGXCatalogCategories(payload []byte) ([]TGXCommodity, json.RawMessage, bool) {
	var categories []tgxCatalogCategory
	if err := json.Unmarshal(payload, &categories); err != nil || categories == nil {
		return nil, nil, false
	}

	items := make([]TGXCommodity, 0)
	for _, category := range categories {
		for _, item := range category.Children {
			item.Category = category.Name
			items = append(items, item)
		}
	}
	return items, append(json.RawMessage(nil), payload...), true
}

func normalizeTGXCode(raw interface{}) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		return decimal.NewFromFloat(v).String()
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func classifyTGXError(message string) error {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if strings.Contains(normalized, "app") ||
		strings.Contains(normalized, "key") ||
		strings.Contains(normalized, "sign") ||
		strings.Contains(normalized, "auth") {
		return ErrTGXAuth
	}
	if strings.Contains(normalized, "停售") ||
		strings.Contains(normalized, "暂时缺货") ||
		strings.Contains(normalized, "缺货") ||
		strings.Contains(normalized, "out of stock") ||
		strings.Contains(normalized, "sold out") {
		return ErrTGXUnavailable
	}
	return ErrTGXBusiness
}

func redactSecret(message, secret string) string {
	if strings.TrimSpace(secret) == "" {
		return message
	}
	return strings.ReplaceAll(message, secret, "[REDACTED]")
}

func cloneURLValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, list := range values {
		copied := make([]string, len(list))
		copy(copied, list)
		clone[key] = copied
	}
	return clone
}
