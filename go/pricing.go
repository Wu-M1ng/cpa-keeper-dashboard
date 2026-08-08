package main

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

type modelPrice struct {
	Model                string  `json:"model"`
	InputPerMillion      float64 `json:"input_per_million"`
	OutputPerMillion     float64 `json:"output_per_million"`
	CacheReadPerMillion  float64 `json:"cache_read_per_million"`
	CacheWritePerMillion float64 `json:"cache_write_per_million"`
	ReasoningPerMillion  float64 `json:"reasoning_per_million"`
	UpdatedAtMS          int64   `json:"updated_at_ms,omitempty"`
}

type tokenTotals struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Reasoning  int64 `json:"reasoning"`
	Cached     int64 `json:"cached"`
	Total      int64 `json:"total"`
}

func validatePrices(prices []modelPrice) error {
	if len(prices) > 2000 {
		return errors.New("too many model prices")
	}
	seen := make(map[string]struct{}, len(prices))
	for i := range prices {
		prices[i].Model = strings.TrimSpace(prices[i].Model)
		if prices[i].Model == "" || len(prices[i].Model) > 160 {
			return errors.New("model price requires a valid model name")
		}
		if _, exists := seen[prices[i].Model]; exists {
			return errors.New("duplicate model price: " + prices[i].Model)
		}
		seen[prices[i].Model] = struct{}{}
		values := []float64{
			prices[i].InputPerMillion,
			prices[i].OutputPerMillion,
			prices[i].CacheReadPerMillion,
			prices[i].CacheWritePerMillion,
			prices[i].ReasoningPerMillion,
		}
		for _, value := range values {
			if value < 0 || value > 1_000_000 || math.IsNaN(value) || math.IsInf(value, 0) {
				return errors.New("model prices must be finite non-negative values")
			}
		}
	}
	return nil
}

func listPrices(ctx context.Context, store *eventStore) ([]modelPrice, error) {
	prices, _, err := loadPriceSnapshot(ctx, store)
	return prices, err
}

func loadPriceSnapshot(ctx context.Context, store *eventStore) ([]modelPrice, map[string]modelPrice, error) {
	store.priceMu.RLock()
	if store.priceLoaded {
		prices := append([]modelPrice(nil), store.priceList...)
		priceMap := store.priceMap
		store.priceMu.RUnlock()
		return prices, priceMap, nil
	}
	store.priceMu.RUnlock()

	store.priceMu.Lock()
	defer store.priceMu.Unlock()
	if store.priceLoaded {
		return append([]modelPrice(nil), store.priceList...), store.priceMap, nil
	}
	prices, err := queryPrices(ctx, store)
	if err != nil {
		return nil, nil, err
	}
	priceMap := make(map[string]modelPrice, len(prices))
	for _, price := range prices {
		priceMap[price.Model] = price
	}
	store.priceList = append([]modelPrice(nil), prices...)
	store.priceMap = priceMap
	store.priceLoaded = true
	return prices, priceMap, nil
}

func queryPrices(ctx context.Context, store *eventStore) ([]modelPrice, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT model, input_per_million, output_per_million,
		cache_read_per_million, cache_write_per_million, reasoning_per_million, updated_at_ms
		FROM model_prices ORDER BY model COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prices := make([]modelPrice, 0)
	for rows.Next() {
		var price modelPrice
		if err := rows.Scan(&price.Model, &price.InputPerMillion, &price.OutputPerMillion,
			&price.CacheReadPerMillion, &price.CacheWritePerMillion,
			&price.ReasoningPerMillion, &price.UpdatedAtMS); err != nil {
			return nil, err
		}
		prices = append(prices, price)
	}
	return prices, rows.Err()
}

func replacePrices(ctx context.Context, store *eventStore, prices []modelPrice) error {
	if err := validatePrices(prices); err != nil {
		return err
	}
	store.priceMu.Lock()
	defer store.priceMu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM model_prices"); err != nil {
		return err
	}
	if err := insertPricesTx(ctx, tx, prices); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	store.priceLoaded = false
	store.priceList = nil
	store.priceMap = nil
	return nil
}

func insertPricesTx(ctx context.Context, tx *sql.Tx, prices []modelPrice) error {
	statement, err := tx.PrepareContext(ctx, `INSERT INTO model_prices (
		model, input_per_million, output_per_million, cache_read_per_million,
		cache_write_per_million, reasoning_per_million, updated_at_ms
	) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer statement.Close()
	now := time.Now().UTC().UnixMilli()
	for _, price := range prices {
		price.Model = strings.TrimSpace(price.Model)
		updated := price.UpdatedAtMS
		if updated <= 0 {
			updated = now
		}
		if _, err := statement.ExecContext(ctx, price.Model, price.InputPerMillion,
			price.OutputPerMillion, price.CacheReadPerMillion,
			price.CacheWritePerMillion, price.ReasoningPerMillion, updated); err != nil {
			return err
		}
	}
	return nil
}

func loadPriceMap(ctx context.Context, store *eventStore) (map[string]modelPrice, error) {
	_, prices, err := loadPriceSnapshot(ctx, store)
	return prices, err
}

func resolvePrice(model string, prices map[string]modelPrice) modelPrice {
	if price, ok := prices[model]; ok {
		return price
	}
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		if price, ok := prices[model[slash+1:]]; ok {
			return price
		}
	}
	return prices["*"]
}

func calculateCost(tokens tokenTotals, price modelPrice) float64 {
	cacheRead := max64(0, tokens.CacheRead)
	cacheWrite := max64(0, tokens.CacheWrite)
	reasoning := max64(0, tokens.Reasoning)
	regularInput := max64(0, tokens.Input-cacheRead-cacheWrite)
	regularOutput := max64(0, tokens.Output-reasoning)
	cacheWriteRate := price.CacheWritePerMillion
	if cacheWriteRate == 0 {
		cacheWriteRate = price.InputPerMillion
	}
	reasoningRate := price.ReasoningPerMillion
	if reasoningRate == 0 {
		reasoningRate = price.OutputPerMillion
	}
	units := float64(regularInput)*price.InputPerMillion +
		float64(regularOutput)*price.OutputPerMillion +
		float64(cacheRead)*price.CacheReadPerMillion +
		float64(cacheWrite)*cacheWriteRate +
		float64(reasoning)*reasoningRate
	return units / 1_000_000
}

func calculateStandardCost(tokens tokenTotals, price modelPrice) float64 {
	price.CacheReadPerMillion = price.InputPerMillion
	return calculateCost(tokens, price)
}

func sortedPriceModels(prices map[string]modelPrice) []string {
	models := make([]string, 0, len(prices))
	for model := range prices {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}
