package models

import "strings"

// ProviderCatalogImagePath returns the local fallback image for a supported
// catalog platform when an upstream product does not provide a usable cover.
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
	case "vk":
		return "/uploads/catalog/vk.svg"
	case "spotify":
		return "/uploads/catalog/spotify.svg"
	case "discord":
		return "/uploads/catalog/discord.svg"
	case "twitch":
		return "/uploads/catalog/twitch.svg"
	case "reddit":
		return "/uploads/catalog/reddit.svg"
	case "linkedin":
		return "/uploads/catalog/linkedin.svg"
	case "github":
		return "/uploads/catalog/github.svg"
	case "quora":
		return "/uploads/catalog/quora.svg"
	case "whatsapp":
		return "/uploads/catalog/whatsapp.svg"
	case "line-voom":
		return "/uploads/catalog/line-voom.svg"
	case "threads":
		return "/uploads/catalog/threads.svg"
	case "gmail":
		return "/uploads/catalog/gmail.svg"
	case "outlook":
		return "/uploads/catalog/outlook.svg"
	case "hotmail":
		return "/uploads/catalog/hotmail.svg"
	case "overseas-email":
		return "/uploads/catalog/overseas-email.svg"
	default:
		return "/uploads/catalog/social.svg"
	}
}
