package ilp

import (
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

// buildChainApp returns A -> B -> C, all 512MB / 100ms.
//
// The optimum is unambiguous: every member is on the same tier, so fusing costs
// nothing in execution and each internalised edge saves one request charge.
// Latency stays at 300ms against a 400ms cap. Expected partition: {A, B, C}.
func buildChainApp() *domain.Application {
	newFn := func(id string) *domain.LambdaFunction {
		return &domain.LambdaFunction{
			ID: id, Name: id, MemoryMB: 512, TimeoutSec: 1, LoadFactor: 1.0,
			AvgDurationMs: 100, DataOutBytes: make(map[string]int64),
		}
	}
	a, b, c := newFn("alpha"), newFn("beta"), newFn("gamma")
	a.AddChild(b, 10*1024*1024)
	b.AddChild(c, 1*1024*1024)

	return &domain.Application{
		Name:            "chain",
		Functions:       []*domain.LambdaFunction{a, b, c},
		CriticalPathIDs: []string{"alpha", "beta", "gamma"},
		MaxMemoryMB:     1024,
		MaxLatencyMS:    400,
		NetworkHopMS:    10,
	}
}

// ── Model generation ─────────────────────────────────────────────────────────

// The regression this guards: the emitter used to write lp_solve syntax while
// the solver was invoked with --lp, which reads CPLEX LP. Nothing caught it
// because generation was only exercised through a solver that isn't installed
// in most environments.
func TestBuildLP_UsesCPLEXSections(t *testing.T) {
	lp, edges := buildLP(buildChainApp())
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}

	for _, section := range []string{"Minimize", "Subject To", "Binary", "End"} {
		if !strings.Contains(lp, section) {
			t.Errorf("LP is missing the %q section:\n%s", section, lp)
		}
	}
	if strings.Contains(lp, ";") {
		t.Error("LP contains a semicolon — that is lp_solve syntax, which glpsol --lp rejects")
	}
	if strings.Contains(lp, "min:") {
		t.Error("LP uses the lp_solve 'min:' header instead of a 'Minimize' section")
	}
}

func TestBuildLP_RespectsLineLengthLimit(t *testing.T) {
	// GLPK's LP reader caps lines at 255 characters. Long expressions must wrap.
	lp, _ := buildLP(buildChainApp())
	for i, line := range strings.Split(lp, "\n") {
		if len(line) > 255 {
			t.Errorf("line %d is %d chars, over GLPK's 255 limit: %q", i+1, len(line), line)
		}
	}
}

// Every variable used in a constraint must appear in the Binary section.
// A variable referenced but never declared is silently treated as a fresh
// continuous column, which would quietly relax whatever constraint uses it.
func TestBuildLP_DeclaresEveryVariable(t *testing.T) {
	lp, _ := buildLP(buildChainApp())

	body, declarations, found := strings.Cut(lp, "Binary\n")
	if !found {
		t.Fatal("LP has no Binary section")
	}

	varPattern := regexp.MustCompile(`\b[xc]_\d+(?:_\d+)?\b`)
	declared := make(map[string]bool)
	for _, v := range varPattern.FindAllString(declarations, -1) {
		declared[v] = true
	}

	var undeclared []string
	for _, v := range varPattern.FindAllString(body, -1) {
		if !declared[v] {
			undeclared = append(undeclared, v)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		t.Errorf("variables used but not declared binary: %v", undeclared)
	}
}

func TestBuildLP_LatencyBudgetIsRemainingSlack(t *testing.T) {
	// Base runtime along alpha->beta->gamma is 300ms against a 400ms budget,
	// so the hops on cut critical-path edges may consume at most 100ms.
	lp, _ := buildLP(buildChainApp())

	var latency string
	for _, line := range strings.Split(lp, "\n") {
		if strings.Contains(line, "latency:") {
			latency = line
			break
		}
	}
	if latency == "" {
		t.Fatal("no latency constraint emitted")
	}
	if !strings.HasSuffix(strings.TrimSpace(latency), "<= 100") {
		t.Errorf("latency budget should be 400-300=100, got %q", latency)
	}
}

// A criticalPath may name two functions that do not actually call each other.
// Emitting a cut variable for that pair would reference a column declared
// nowhere else, and the solver could zero it to dodge the latency budget.
func TestBuildLP_IgnoresCriticalPathPairsThatAreNotEdges(t *testing.T) {
	app := buildChainApp()
	app.CriticalPathIDs = []string{"alpha", "gamma"} // alpha does not call gamma

	lp, _ := buildLP(app)
	if strings.Contains(lp, "latency:") {
		t.Errorf("expected no latency constraint when no critical-path pair is a real edge:\n%s", lp)
	}
}

func TestBuildLP_NoEdgesYieldsNoModel(t *testing.T) {
	app := buildChainApp()
	for _, f := range app.Functions {
		f.Children = nil
		f.DataOutBytes = make(map[string]int64)
	}

	lp, edges := buildLP(app)
	if len(edges) != 0 || lp != "" {
		t.Errorf("expected an empty model when there are no edges, got %d edges", len(edges))
	}
}

// ── Solution parsing ─────────────────────────────────────────────────────────

func TestParseColumnValues_HandlesIntegerMarker(t *testing.T) {
	// glpsol prints '*' for integer columns. Reading the field straight after
	// the name picks up the marker rather than the value.
	const solution = `Problem:    optifuse
Status:     INTEGER OPTIMAL

   No. Column name       Activity     Lower bound   Upper bound
------ ------------    ------------- ------------- -------------
     1 x_0_0        *              1             0             1
     2 x_0_1        *              0             0             1
     3 c_0          *              1             0             1
`
	values := parseColumnValues(solution)
	for name, want := range map[string]float64{"x_0_0": 1, "x_0_1": 0, "c_0": 1} {
		if got, ok := values[name]; !ok {
			t.Errorf("%s missing from parsed values", name)
		} else if got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestParseColumnValues_HandlesCBCFormat(t *testing.T) {
	// cbc writes "<index> <name> <value> <cost>" with no marker column.
	const solution = `Optimal - objective value 0.00001
      0 x_0_0                  1                       0
      1 c_0                    0                       0
`
	values := parseColumnValues(solution)
	if values["x_0_0"] != 1 || values["c_0"] != 0 {
		t.Errorf("cbc-format parse failed: %v", values)
	}
}

// "INTEGER NON-OPTIMAL" contains "OPTIMAL" as a substring, so a plain
// Contains check accepts a solve that hit the time limit as proven optimal.
func TestSolvedOptimally_RejectsNonOptimal(t *testing.T) {
	cases := map[string]bool{
		"Status:     INTEGER OPTIMAL":     true,
		"Status:     OPTIMAL":             true,
		"Status:     INTEGER NON-OPTIMAL": false,
		"Status:     INFEASIBLE":          false,
		"Status:     UNDEFINED":           false,
	}
	for text, want := range cases {
		if _, got := solvedOptimally(text); got != want {
			t.Errorf("solvedOptimally(%q) = %v, want %v", text, got, want)
		}
	}
}

// ── End to end, when a solver is present ─────────────────────────────────────

func TestSolve_FindsKnownOptimum(t *testing.T) {
	if _, err := exec.LookPath("glpsol"); err != nil {
		t.Skip("glpsol not installed — skipping solver round-trip")
	}

	result, err := Solve(buildChainApp())
	if err != nil {
		t.Fatalf("Solve returned an error: %v", err)
	}
	if !result.Optimal {
		t.Fatalf("solver did not reach optimality: %s", result.Status)
	}

	got := make([]string, 0, len(result.Groups))
	for _, group := range result.Groups {
		ids := make([]string, len(group))
		for i, f := range group {
			ids[i] = f.ID
		}
		sort.Strings(ids)
		got = append(got, strings.Join(ids, "+"))
	}
	sort.Strings(got)

	want := []string{"alpha+beta+gamma"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("optimal partition = %v, want %v", got, want)
	}
}

// buildMismatchedApp returns a chain where the middle function is a 1769MB
// hop between two long-running 128MB functions:
//
//	alpha (128MB, 1000ms) -> beta (1769MB, 10ms) -> gamma (128MB, 1000ms)
//
// Fusing either neighbour with beta re-tiers a full second of work from 128MB
// to 1769MB, roughly $2.7e-5 of execution against a $2e-7 request saving.
// Fusing alpha with gamma directly is impossible (no edge), so the only
// profitable partition is no fusion at all.
func buildMismatchedApp() *domain.Application {
	newFn := func(id string, mem, ms int) *domain.LambdaFunction {
		return &domain.LambdaFunction{
			ID: id, Name: id, MemoryMB: mem, TimeoutSec: 1, LoadFactor: 1.0,
			AvgDurationMs: float64(ms), DataOutBytes: make(map[string]int64),
		}
	}
	a, b, c := newFn("alpha", 128, 1000), newFn("beta", 1769, 10), newFn("gamma", 128, 1000)
	a.AddChild(b, 0)
	b.AddChild(c, 0)

	return &domain.Application{
		Name:            "mismatched",
		Functions:       []*domain.LambdaFunction{a, b, c},
		CriticalPathIDs: []string{"alpha", "beta", "gamma"},
		MaxMemoryMB:     3008,
		MaxLatencyMS:    5000,
		NetworkHopMS:    10,
	}
}

// The regression this guards: with cut-transfer as the only objective term the
// solver had nothing to minimise once transfer was zero, and returned an
// arbitrary partition. With execution in the objective it must refuse a merge
// that costs 100x what it saves.
func TestSolve_RefusesUnprofitableRetiering(t *testing.T) {
	if _, err := exec.LookPath("glpsol"); err != nil {
		t.Skip("glpsol not installed — skipping solver round-trip")
	}

	app := buildMismatchedApp()
	result, err := Solve(app)
	if err != nil {
		t.Fatalf("Solve returned an error: %v", err)
	}
	if !result.Optimal {
		t.Fatalf("solver did not reach optimality: %s", result.Status)
	}
	if len(result.Groups) != 3 {
		t.Fatalf("got %d groups, want 3 singletons: %v", len(result.Groups), groupIDs(result.Groups))
	}

	// And the model's own accounting must agree that nothing cheaper existed.
	cost := app.CalculateMetrics(result.Groups).TotalCostUSD
	all := [][]*domain.LambdaFunction{app.Functions}
	if fused := app.CalculateMetrics(all).TotalCostUSD; fused <= cost {
		t.Errorf("full fusion costs %g, singletons %g; fixture does not exercise the penalty", fused, cost)
	}
}

func groupIDs(groups [][]*domain.LambdaFunction) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		ids := make([]string, len(g))
		for i, f := range g {
			ids[i] = f.ID
		}
		out = append(out, strings.Join(ids, "+"))
	}
	return out
}

// The objective must carry both charges. A model whose objective is only cut
// variables is the zero-transfer bug again; one with only tiering variables
// would never see a reason to fuse.
func TestBuildLP_ObjectivePricesRequestsAndExecution(t *testing.T) {
	lp, _ := buildLP(buildChainApp())

	objective, _, found := strings.Cut(lp, "Subject To\n")
	if !found {
		t.Fatal("LP has no Subject To section")
	}
	if !strings.Contains(objective, " c_") {
		t.Error("objective has no cut (request) terms")
	}
	if !strings.Contains(objective, " y_") {
		t.Error("objective has no tiering (execution) terms")
	}
	if strings.Contains(lp, "excl_") {
		t.Error("pairwise exclusions are still emitted; the objective prices re-tiering now")
	}
	for _, name := range []string{"mem_0_0:", "tier_0_0:"} {
		if !strings.Contains(lp, name) {
			t.Errorf("LP is missing the %s constraint", name)
		}
	}
}
