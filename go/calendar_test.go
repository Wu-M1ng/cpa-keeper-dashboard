package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestEventDaysUseChinaCalendarDates(t *testing.T) {
	store := openTestStore(t)
	for _, instant := range []string{
		"2026-09-24T15:59:59Z", // September 24, 23:59:59 in China.
		"2026-09-24T16:00:00Z", // September 25, 00:00:00 in China.
		"2026-09-24T18:00:00Z",
		"2026-09-26T16:00:00Z", // September 27 in China.
	} {
		at, err := time.Parse(time.RFC3339, instant)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.writeBatch([]usageEvent{fixtureEvent(at, "gpt", false, 10, 2)}); err != nil {
			t.Fatal(err)
		}
	}
	days, err := queryEventDays(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-09-24", "2026-09-25", "2026-09-27"}
	if !reflect.DeepEqual(days, want) {
		t.Fatalf("available days=%v, want %v", days, want)
	}
	if _, err := store.db.Exec("DELETE FROM usage_events"); err != nil {
		t.Fatal(err)
	}
	days, err = queryEventDays(t.Context(), store)
	if err != nil || days == nil || len(days) != 0 {
		t.Fatalf("empty database days=%v err=%v", days, err)
	}
}

func TestEventDaysAcrossSparseYears(t *testing.T) {
	store := openTestStore(t)
	want := []string{"2020-01-01", "2023-06-15", "2026-12-31"}
	for _, day := range want {
		at, err := time.Parse(time.RFC3339, day+"T00:00:00+08:00")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.writeBatch([]usageEvent{fixtureEvent(at, "gpt", false, 10, 2)}); err != nil {
			t.Fatal(err)
		}
	}
	days, err := queryEventDays(t.Context(), store)
	if err != nil || !reflect.DeepEqual(days, want) {
		t.Fatalf("available days=%v err=%v, want %v", days, err, want)
	}
}

func TestManagementEventDaysUsesDetailData(t *testing.T) {
	_, _ = withTestRuntime(t)
	response := handleManagement(managementRequest{Method: http.MethodGet, Path: managementPrefix + "/events/dates"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
	}
	var result struct {
		Days []string `json:"days"`
	}
	if err := json.Unmarshal(response.Body, &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Days, []string{"2026-08-06"}) {
		t.Fatalf("endpoint days=%v", result.Days)
	}
}
