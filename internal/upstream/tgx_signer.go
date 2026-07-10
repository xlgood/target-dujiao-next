package upstream

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
)

func SignTGX(params url.Values, appKey string) string {
	signing := BuildTGXSignString(params, appKey)
	sum := md5.Sum([]byte(signing))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func BuildTGXSignString(params url.Values, appKey string) string {
	keys := make([]string, 0, len(params))
	for key, values := range params {
		if key == "sign" || len(values) == 0 || values[0] == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		parts = append(parts, key+"="+params.Get(key))
	}
	parts = append(parts, "key="+appKey)

	decoded, err := url.QueryUnescape(strings.Join(parts, "&"))
	if err != nil {
		return strings.Join(parts, "&")
	}
	return decoded
}
