package upstream

import (
	"os"
	"testing"
)

func TestParseFansGurusCatalogDetailsFixture(t *testing.T) {
	path := os.Getenv("FANSGURUS_PUBLIC_CATALOG_FIXTURE")
	if path == "" {
		t.Skip("fixture not configured")
	}
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	details, err := parseFansGurusCatalogDetails(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, detail := range details {
		if detail.Service == 14266 {
			if detail.Category == "" || detail.Description == "" {
				t.Fatalf("incomplete 14266 detail: %+v", detail)
			}
			return
		}
	}
	t.Fatal("service 14266 not found")
}
