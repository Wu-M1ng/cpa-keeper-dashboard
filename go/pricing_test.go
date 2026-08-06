package main

import "testing"

func TestCalculateCostAvoidsCacheAndReasoningDoubleCharge(t *testing.T) {
	price := modelPrice{
		InputPerMillion:      2,
		OutputPerMillion:     8,
		CacheReadPerMillion:  0.2,
		CacheWritePerMillion: 2.5,
		ReasoningPerMillion:  10,
	}
	tokens := tokenTotals{
		Input:      1000,
		Output:     500,
		CacheRead:  200,
		CacheWrite: 100,
		Reasoning:  100,
	}
	// 700 input * 2 + 200 cache read * .2 + 100 cache write * 2.5
	// + 400 output * 8 + 100 reasoning * 10, all divided by 1e6.
	want := 0.00589
	got := calculateCost(tokens, price)
	if diff := got - want; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("cost = %.8f, want %.8f", got, want)
	}
}

func TestPriceValidationRejectsNegativeValues(t *testing.T) {
	if err := validatePrices([]modelPrice{{Model: "gpt", InputPerMillion: -1}}); err == nil {
		t.Fatal("negative price should be rejected")
	}
	if err := validatePrices([]modelPrice{{Model: ""}}); err == nil {
		t.Fatal("blank model should be rejected")
	}
}
