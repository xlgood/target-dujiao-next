package public

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/gin-gonic/gin"
)

func TestLocaleFromCountryCode(t *testing.T) {
	cases := []struct {
		country string
		want    string
	}{
		{country: "CN", want: constants.LocaleZhCN},
		{country: "hk", want: constants.LocaleZhTW},
		{country: "TW", want: constants.LocaleZhTW},
		{country: "US", want: constants.LocaleEnUS},
		{country: "SG", want: constants.LocaleEnUS},
		{country: "JP", want: ""},
		{country: "", want: ""},
	}

	for _, tc := range cases {
		if got := localeFromCountryCode(tc.country); got != tc.want {
			t.Fatalf("localeFromCountryCode(%q)=%q want %q", tc.country, got, tc.want)
		}
	}
}

func TestResolvePublicDefaultLocalePrefersCountryHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/public/config", nil)
	c.Request.Header.Set("Accept-Language", "zh-CN")
	c.Request.Header.Set("CF-IPCountry", "TW")

	if got := resolvePublicDefaultLocale(c); got != constants.LocaleZhTW {
		t.Fatalf("default locale=%q want %q", got, constants.LocaleZhTW)
	}
}

func TestResolvePublicDefaultLocaleFallsBackToAcceptLanguage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/public/config", nil)
	c.Request.Header.Set("Accept-Language", "en-US,en;q=0.9")

	if got := resolvePublicDefaultLocale(c); got != constants.LocaleEnUS {
		t.Fatalf("default locale=%q want %q", got, constants.LocaleEnUS)
	}
}
