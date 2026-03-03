package mapr

import (
	"strconv"
	"testing"
)

func TestGroupSetResultPercentageAndPercentile(t *testing.T) {
	query, err := NewQuery("select percentage(value),percentile(value) from stats group by host order by percentage(value)")
	if err != nil {
		t.Fatalf("Unable to parse query: %v", err)
	}

	groupSet := NewGroupSet()

	setA := groupSet.GetSet("host-a")
	if err := setA.Aggregate("percentage(value)", Percentage, "10", false); err != nil {
		t.Fatalf("Unable to aggregate percentage for host-a: %v", err)
	}
	if err := setA.Aggregate("percentile(value)", Percentile, "10", false); err != nil {
		t.Fatalf("Unable to aggregate percentile for host-a: %v", err)
	}

	setB := groupSet.GetSet("host-b")
	if err := setB.Aggregate("percentage(value)", Percentage, "30", false); err != nil {
		t.Fatalf("Unable to aggregate percentage for host-b: %v", err)
	}
	if err := setB.Aggregate("percentile(value)", Percentile, "30", false); err != nil {
		t.Fatalf("Unable to aggregate percentile for host-b: %v", err)
	}

	rows, _, err := groupSet.result(query, false)
	if err != nil {
		t.Fatalf("Unable to build result rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Expected 2 result rows, got %d", len(rows))
	}

	if rows[0].groupKey != "host-b" {
		t.Fatalf("Expected rows to be ordered by percentage descending, first row=%s", rows[0].groupKey)
	}

	valuesByGroup := map[string][]float64{}
	for _, row := range rows {
		parsedValues := make([]float64, 0, len(row.values))
		for _, value := range row.values {
			parsedValue, err := strconv.ParseFloat(value, 64)
			if err != nil {
				t.Fatalf("Unable to parse result value %q: %v", value, err)
			}
			parsedValues = append(parsedValues, parsedValue)
		}
		valuesByGroup[row.groupKey] = parsedValues
	}

	assertAlmostEqual(t, valuesByGroup["host-a"][0], 25.0, 0.0001, "host-a percentage")
	assertAlmostEqual(t, valuesByGroup["host-a"][1], 50.0, 0.0001, "host-a percentile")
	assertAlmostEqual(t, valuesByGroup["host-b"][0], 75.0, 0.0001, "host-b percentage")
	assertAlmostEqual(t, valuesByGroup["host-b"][1], 100.0, 0.0001, "host-b percentile")
}

func assertAlmostEqual(t *testing.T, got, expected, tolerance float64, label string) {
	t.Helper()

	diff := got - expected
	if diff < 0 {
		diff = -diff
	}
	if diff > tolerance {
		t.Fatalf("Unexpected %s: got=%f expected=%f tolerance=%f", label, got, expected, tolerance)
	}
}
