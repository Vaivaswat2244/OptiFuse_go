package algo_test

import (
	"testing"

	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/algo"
	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

func result(name string, cost, latency float64) algo.AlgorithmResult {
	return algo.AlgorithmResult{
		Name:    name,
		Metrics: domain.Metrics{TotalCostUSD: cost, LatencyMS: latency, Feasible: true},
	}
}

func TestDominates(t *testing.T) {
	cases := []struct {
		name string
		a, b algo.AlgorithmResult
		want bool
	}{
		{"cheaper and faster", result("a", 1, 1), result("b", 2, 2), true},
		{"cheaper, same latency", result("a", 1, 2), result("b", 2, 2), true},
		{"same cost, faster", result("a", 2, 1), result("b", 2, 2), true},
		{"cheaper but slower is a real trade", result("a", 1, 3), result("b", 2, 2), false},
		{"identical dominates nothing", result("a", 2, 2), result("b", 2, 2), false},
	}
	for _, c := range cases {
		if got := algo.Dominates(c.a, c.b); got != c.want {
			t.Errorf("%s: Dominates = %v, want %v", c.name, got, c.want)
		}
	}
}

// An infeasible candidate is not an option at all, so it can neither dominate
// nor be dominated. Treating it as dominated would imply it was considered.
func TestDominates_IgnoresInfeasible(t *testing.T) {
	good := result("good", 1, 1)
	bad := result("bad", 5, 5)
	bad.Metrics.Feasible = false

	if algo.Dominates(good, bad) {
		t.Error("a feasible result should not dominate an infeasible one")
	}
	if algo.Dominates(bad, good) {
		t.Error("an infeasible result should never dominate")
	}
}

func TestMarkFrontier(t *testing.T) {
	results := algo.MarkFrontier([]algo.AlgorithmResult{
		result("NoFusion", 8.66, 660),
		result("MinWCut", 8.46, 660),  // cheaper, same latency: dominates NoFusion
		result("GreedyTP", 8.48, 650), // dearer than MinWCut but faster: a real trade
		result("Singleton", 9.20, 630),
	})

	want := map[string]bool{
		"NoFusion": true, "MinWCut": false, "GreedyTP": false, "Singleton": false,
	}
	for _, r := range results {
		if r.Dominated != want[r.Name] {
			t.Errorf("%s: Dominated = %v, want %v", r.Name, r.Dominated, want[r.Name])
		}
	}
	for _, r := range results {
		if r.Name == "NoFusion" && r.DominatedBy != "MinWCut" {
			t.Errorf("NoFusion should be dominated by MinWCut, got %q", r.DominatedBy)
		}
	}
}

// Two algorithms frequently produce the same partition. Neither dominates the
// other, so both belong on the frontier rather than one arbitrarily shadowing it.
func TestMarkFrontier_TiesBothSurvive(t *testing.T) {
	results := algo.MarkFrontier([]algo.AlgorithmResult{
		result("MinWCut", 7.37, 610),
		result("GreedyTP", 7.37, 610),
	})
	for _, r := range results {
		if r.Dominated {
			t.Errorf("%s should not be dominated by an identical result", r.Name)
		}
	}
}

func TestExtremes_UnambiguousWhenOneOptionWinsBoth(t *testing.T) {
	cheapest, fastest, unambiguous := algo.Extremes([]algo.AlgorithmResult{
		result("NoFusion", 8.37, 650),
		result("MinWCut", 7.37, 610), // cheaper and faster
	})
	if cheapest.Name != "MinWCut" || fastest.Name != "MinWCut" {
		t.Fatalf("expected MinWCut on both ends, got %s / %s", cheapest.Name, fastest.Name)
	}
	if !unambiguous {
		t.Error("one option best on both axes means there is no trade to present")
	}
}

func TestExtremes_TradeoffWhenEndsDiffer(t *testing.T) {
	cheapest, fastest, unambiguous := algo.Extremes([]algo.AlgorithmResult{
		result("MinWCut", 8.46, 660),
		result("Singleton", 9.20, 630),
	})
	if cheapest.Name != "MinWCut" || fastest.Name != "Singleton" {
		t.Fatalf("got cheapest=%s fastest=%s", cheapest.Name, fastest.Name)
	}
	if unambiguous {
		t.Error("different ends means the user has a decision to make")
	}
}
