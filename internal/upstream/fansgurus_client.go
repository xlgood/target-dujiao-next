package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultFansGurusBaseURL = "https://fansgurus.com/api/v2"

var (
	ErrFansGurusAuth        = errors.New("fansgurus auth error")
	ErrFansGurusValidation  = errors.New("fansgurus validation error")
	ErrFansGurusBusiness    = errors.New("fansgurus business error")
	ErrFansGurusBadJSON     = errors.New("fansgurus bad json")
	ErrFansGurusEmptyResult = errors.New("fansgurus empty result")
)

type FansGurusClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

type FansGurusClientOption func(*FansGurusClient)

func WithFansGurusHTTPClient(client *http.Client) FansGurusClientOption {
	return func(c *FansGurusClient) {
		if client != nil {
			c.client = client
		}
	}
}

func NewFansGurusClient(baseURL, apiKey string, opts ...FansGurusClientOption) *FansGurusClient {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultFansGurusBaseURL
	}
	c := &FansGurusClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  strings.TrimSpace(apiKey),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type FansGurusBalance struct {
	Balance  string `json:"balance"`
	Currency string `json:"currency"`
}

type FansGurusService struct {
	Service  uint   `json:"service"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Category string `json:"category"`
	Rate     string `json:"rate"`
	Min      int    `json:"min"`
	Max      int    `json:"max"`
	Dripfeed bool   `json:"dripfeed"`
	Refill   bool   `json:"refill"`
	Cancel   bool   `json:"cancel"`
	// Raw preserves fields added by the remote API so catalog normalization can
	// use an explicit quantity basis when one is supplied.
	Raw json.RawMessage `json:"-"`
}

func (s *FansGurusService) UnmarshalJSON(data []byte) error {
	type serviceAlias FansGurusService
	var decoded serviceAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = FansGurusService(decoded)
	s.Raw = append(s.Raw[:0], data...)
	return nil
}

func (s FansGurusService) MarshalJSON() ([]byte, error) {
	if len(s.Raw) > 0 && json.Valid(s.Raw) {
		return s.Raw, nil
	}
	type serviceAlias FansGurusService
	return json.Marshal(serviceAlias(s))
}

type FansGurusAddOrderRequest struct {
	Service      uint
	Link         string
	Quantity     int
	Comments     string
	AnswerNumber string
	Groups       string
	Username     string
	Runs         int
	Interval     int
	Min          int
	Max          int
	Posts        int
	OldPosts     int
	Delay        int
	Expiry       string
}

type FansGurusAddOrderResponse struct {
	Order  uint   `json:"order"`
	Charge string `json:"charge,omitempty"`
}

type FansGurusOrderStatus struct {
	Status     string `json:"status"`
	Charge     string `json:"charge,omitempty"`
	StartCount string `json:"start_count,omitempty"`
	Remains    string `json:"remains,omitempty"`
	Currency   string `json:"currency,omitempty"`
}

type FansGurusError struct {
	Kind       error
	StatusCode int
	Message    string
}

func (e *FansGurusError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("fansgurus request failed: status=%d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("fansgurus request failed: %s", e.Message)
}

func (e *FansGurusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

func (c *FansGurusClient) GetBalance(ctx context.Context) (*FansGurusBalance, error) {
	var result FansGurusBalance
	if err := c.postForm(ctx, url.Values{"action": []string{"balance"}}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *FansGurusClient) ListServices(ctx context.Context) ([]FansGurusService, error) {
	var result []FansGurusService
	if err := c.postForm(ctx, url.Values{"action": []string{"services"}}, &result); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, &FansGurusError{Kind: ErrFansGurusEmptyResult, Message: "services returned empty result"}
	}
	return result, nil
}

func (c *FansGurusClient) AddOrder(ctx context.Context, req FansGurusAddOrderRequest) (*FansGurusAddOrderResponse, error) {
	values := url.Values{
		"action":  []string{"add"},
		"service": []string{fmt.Sprintf("%d", req.Service)},
	}
	setNonEmpty(values, "link", req.Link)
	setPositiveInt(values, "quantity", req.Quantity)
	setNonEmpty(values, "comments", req.Comments)
	setNonEmpty(values, "answer_number", req.AnswerNumber)
	setNonEmpty(values, "groups", req.Groups)
	setNonEmpty(values, "username", req.Username)
	setPositiveInt(values, "runs", req.Runs)
	setPositiveInt(values, "interval", req.Interval)
	setPositiveInt(values, "min", req.Min)
	setPositiveInt(values, "max", req.Max)
	setPositiveInt(values, "posts", req.Posts)
	setPositiveInt(values, "old_posts", req.OldPosts)
	setPositiveInt(values, "delay", req.Delay)
	setNonEmpty(values, "expiry", req.Expiry)

	var result FansGurusAddOrderResponse
	if err := c.postForm(ctx, values, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *FansGurusClient) GetOrderStatus(ctx context.Context, upstreamOrderID uint) (*FansGurusOrderStatus, error) {
	values := url.Values{
		"action": []string{"status"},
		"order":  []string{fmt.Sprintf("%d", upstreamOrderID)},
	}
	var result FansGurusOrderStatus
	if err := c.postForm(ctx, values, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *FansGurusClient) postForm(ctx context.Context, values url.Values, result interface{}) error {
	values = cloneURLValues(values)
	values.Set("key", c.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("create fansgurus request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("send fansgurus request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read fansgurus response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &FansGurusError{
			Kind:       classifyFansGurusError(string(body)),
			StatusCode: resp.StatusCode,
			Message:    redactSecret(string(body), c.apiKey),
		}
	}

	if apiErr := decodeFansGurusAPIError(body, c.apiKey); apiErr != nil {
		return apiErr
	}

	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return &FansGurusError{
				Kind:    ErrFansGurusBadJSON,
				Message: redactSecret(err.Error(), c.apiKey),
			}
		}
	}
	return nil
}

func decodeFansGurusAPIError(body []byte, apiKey string) error {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	raw, ok := payload["error"]
	if !ok {
		return nil
	}
	msg := strings.TrimSpace(fmt.Sprint(raw))
	if msg == "" {
		msg = "unknown fansgurus error"
	}
	return &FansGurusError{
		Kind:    classifyFansGurusError(msg),
		Message: redactSecret(msg, apiKey),
	}
}

func classifyFansGurusError(message string) error {
	normalized := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(normalized, "key") ||
		strings.Contains(normalized, "auth") ||
		strings.Contains(normalized, "unauthorized"):
		return ErrFansGurusAuth
	case strings.Contains(normalized, "not enough") ||
		strings.Contains(normalized, "insufficient") ||
		strings.Contains(normalized, "balance"):
		return ErrFansGurusBusiness
	case strings.Contains(normalized, "invalid") ||
		strings.Contains(normalized, "minimum") ||
		strings.Contains(normalized, "maximum") ||
		strings.Contains(normalized, "quantity") ||
		strings.Contains(normalized, "service"):
		return ErrFansGurusValidation
	default:
		return ErrFansGurusBusiness
	}
}

func setNonEmpty(values url.Values, key, value string) {
	if strings.TrimSpace(value) != "" {
		values.Set(key, value)
	}
}

func setPositiveInt(values url.Values, key string, value int) {
	if value > 0 {
		values.Set(key, fmt.Sprintf("%d", value))
	}
}
