package upstream

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
)

func SignTGX(params url.Values, appKey string) string {
	signing := BuildTGXSignString(params, appKey)
	sum := md5.Sum([]byte(signing))
	return hex.EncodeToString(sum[:])
}

func BuildTGXSignString(params url.Values, appKey string) string {
	filtered := make(url.Values, len(params))
	for key, values := range params {
		if key == "sign" || len(values) == 0 || values[0] == "" {
			continue
		}
		filtered.Set(key, values[0])
	}

	// TGX signs urldecode(http_build_query(sortedParams) + "&key=app_key").
	// url.Values.Encode provides the same sorted query encoding before decoding.
	encoded := filtered.Encode() + "&key=" + url.QueryEscape(appKey)
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		return encoded
	}
	return decoded
}
