package models

import "strings"

// ProviderCatalogImagePath returns the local shared image for a supported
// catalog platform. Provider product covers are intentionally not retained.
func ProviderCatalogImagePath(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "facebook":
		return "/uploads/catalog/facebook.svg"
	case "instagram":
		return "/uploads/catalog/instagram.svg"
	case "tiktok":
		return "/uploads/catalog/tiktok.svg"
	case "youtube":
		return "/uploads/catalog/youtube.svg"
	case "x":
		return "/uploads/catalog/x.svg"
	default:
		return "/uploads/catalog/social.svg"
	}
}
