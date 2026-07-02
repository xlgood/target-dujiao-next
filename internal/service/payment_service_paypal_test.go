package service

import (
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
)

func TestPickFirstNonEmpty(t *testing.T) {
	if got := pickFirstNonEmpty("", " ", "abc", "def"); got != "abc" {
		t.Fatalf("expected abc, got %s", got)
	}
	if got := pickFirstNonEmpty("", " "); got != "" {
		t.Fatalf("expected empty value, got %s", got)
	}
}

func TestShouldMarkFulfilling(t *testing.T) {
	if shouldMarkFulfilling(nil) {
		t.Fatalf("nil order should not be fulfilling")
	}
	order := &models.Order{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}}
	if shouldMarkFulfilling(order) {
		t.Fatalf("auto items should not require fulfilling")
	}
	order = &models.Order{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeManual}}}
	if !shouldMarkFulfilling(order) {
		t.Fatalf("manual items should require fulfilling")
	}
}

func TestHasManualFulfillmentItems(t *testing.T) {
	if hasManualFulfillmentItems(nil) {
		t.Fatalf("nil order should not have manual items")
	}
	upstreamOnly := &models.Order{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeUpstream}}}
	if hasManualFulfillmentItems(upstreamOnly) {
		t.Fatalf("upstream-only order should not trigger manual fulfillment pending")
	}
	autoOnly := &models.Order{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}}
	if hasManualFulfillmentItems(autoOnly) {
		t.Fatalf("auto-only order should not have manual items")
	}
	manualOnly := &models.Order{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeManual}}}
	if !hasManualFulfillmentItems(manualOnly) {
		t.Fatalf("manual order should have manual items")
	}
	emptyType := &models.Order{Items: []models.OrderItem{{FulfillmentType: "  "}}}
	if !hasManualFulfillmentItems(emptyType) {
		t.Fatalf("empty fulfillment type should be treated as manual")
	}
	mixedUpstreamManual := &models.Order{Items: []models.OrderItem{
		{FulfillmentType: constants.FulfillmentTypeUpstream},
		{FulfillmentType: constants.FulfillmentTypeManual},
	}}
	if !hasManualFulfillmentItems(mixedUpstreamManual) {
		t.Fatalf("mixed upstream+manual order should have manual items")
	}
}

func TestIsOrderFullyAutoFulfill(t *testing.T) {
	if isOrderFullyAutoFulfill(nil) {
		t.Fatalf("nil order should not be fully auto")
	}

	autoSingle := &models.Order{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}}
	if !isOrderFullyAutoFulfill(autoSingle) {
		t.Fatalf("single auto order should be fully auto")
	}

	manualSingle := &models.Order{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeManual}}}
	if isOrderFullyAutoFulfill(manualSingle) {
		t.Fatalf("single manual order should not be fully auto")
	}

	mixedSingle := &models.Order{Items: []models.OrderItem{
		{FulfillmentType: constants.FulfillmentTypeAuto},
		{FulfillmentType: constants.FulfillmentTypeManual},
	}}
	if isOrderFullyAutoFulfill(mixedSingle) {
		t.Fatalf("mixed single order should not be fully auto")
	}

	parentAllAuto := &models.Order{Children: []models.Order{
		{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}},
		{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}},
	}}
	if !isOrderFullyAutoFulfill(parentAllAuto) {
		t.Fatalf("parent with all auto children should be fully auto")
	}

	parentMixed := &models.Order{Children: []models.Order{
		{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeAuto}}},
		{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeManual}}},
	}}
	if isOrderFullyAutoFulfill(parentMixed) {
		t.Fatalf("parent with mixed children should not be fully auto")
	}

	parentAllManual := &models.Order{Children: []models.Order{
		{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeUpstream}}},
		{Items: []models.OrderItem{{FulfillmentType: constants.FulfillmentTypeManual}}},
	}}
	if isOrderFullyAutoFulfill(parentAllManual) {
		t.Fatalf("parent with all manual/upstream children should not be fully auto")
	}

	emptyOrder := &models.Order{}
	if isOrderFullyAutoFulfill(emptyOrder) {
		t.Fatalf("order without items or children should not be fully auto")
	}
}
