// Package ilp handles LP file generation and external solver invocation for the
// MtxILP algorithm. It replaces PuLP + CBC from the Python implementation.
//
// The file we emit is CPLEX LP format, because that is what `glpsol --lp` reads.
// An earlier version wrote lp_solve syntax (`min: ...;`, semicolon-terminated
// named constraints, `bin`/`end`) which glpsol rejected on line 1 with
// "missing variable name" — MtxILP had therefore never produced a result.
//
// To add a new solver, implement the Solver interface and register it in Solve().
package ilp

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

// Result is the output of the ILP solver.
type Result struct {
	Optimal bool
	Status  string
	Groups  [][]*domain.LambdaFunction
}

// maxLineLen keeps emitted lines inside GLPK's LP reader limit of 255
// characters. Newlines are whitespace within a statement, so wrapping an
// expression across lines is safe.
const maxLineLen = 200

// term is one coefficient/variable pair in a linear expression.
type term struct {
	coef float64
	name string
}

// expr is a linear expression that accumulates duplicate variables rather than
// emitting them twice. CPLEX LP readers reject a variable appearing more than
// once in the same constraint, which the memory constraint would otherwise do:
// it references x[b,f] for every f (including f == b) and x[b,b] again for the
// capacity term.
type expr struct {
	order []string
	byVar map[string]float64
}

func newExpr() *expr {
	return &expr{byVar: make(map[string]float64)}
}

func (e *expr) add(coef float64, name string) {
	if coef == 0 {
		return
	}
	if _, seen := e.byVar[name]; !seen {
		e.order = append(e.order, name)
	}
	e.byVar[name] += coef
}

func (e *expr) terms() []term {
	out := make([]term, 0, len(e.order))
	for _, name := range e.order {
		if c := e.byVar[name]; c != 0 {
			out = append(out, term{c, name})
		}
	}
	return out
}

func (e *expr) empty() bool { return len(e.terms()) == 0 }

// writeStatement emits "<label> <terms> <suffix>", wrapping long expressions.
func writeStatement(sb *strings.Builder, label string, terms []term, suffix string) {
	line := " " + label
	for i, t := range terms {
		var chunk string
		switch {
		case i == 0 && t.coef < 0:
			chunk = fmt.Sprintf(" -%s %s", formatCoef(-t.coef), t.name)
		case i == 0:
			chunk = fmt.Sprintf(" %s %s", formatCoef(t.coef), t.name)
		case t.coef < 0:
			chunk = fmt.Sprintf(" - %s %s", formatCoef(-t.coef), t.name)
		default:
			chunk = fmt.Sprintf(" + %s %s", formatCoef(t.coef), t.name)
		}
		if len(line)+len(chunk) > maxLineLen {
			sb.WriteString(line + "\n")
			line = "   "
		}
		line += chunk
	}
	sb.WriteString(line + suffix + "\n")
}

// formatCoef renders a coefficient in a form the LP reader accepts. %.12g keeps
// small data-transfer costs (order 1e-9) from being flattened to zero.
func formatCoef(c float64) string {
	return strconv.FormatFloat(c, 'g', 12, 64)
}

// dirEdge is a directed call between two functions.
type dirEdge struct{ u, v *domain.LambdaFunction }

// buildLP returns the CPLEX LP model text and the edge list it was built from.
//
// Kept separate from Solve so the model can be checked without glpsol installed,
// which is what the tests do — the previous format bug was in generation, not in
// solving, and would have been caught by inspecting the emitted text.
func buildLP(app *domain.Application) (string, []dirEdge) {
	funcs := app.Functions
	critPath := app.CriticalPath()

	// ── Variable naming ───────────────────────────────────────────────────────
	// x_<b>_<f> = 1 if function f is assigned to the group rooted at b
	// c_<e>     = 1 if edge e is cut (its endpoints are in different groups)
	//
	// Indices rather than IDs deliberately. glpsol's printable solution reserves
	// a 12-character column for the variable name and wraps onto the next line
	// when it overflows, which a name like x_orderPlaced_processPayment does.
	// Indices keep every name short, and sidestep escaping IDs entirely.
	//
	// Python: x = pulp.LpVariable.dicts("x", ((b.id, f.id) for b in roots for f in app.functions))
	//         is_cut = pulp.LpVariable.dicts("is_cut", ((e[0].id, e[1].id) for e in all_edges))
	idxOf := make(map[string]int, len(funcs))
	for i, f := range funcs {
		idxOf[f.ID] = i
	}
	xVar := func(b, f int) string { return fmt.Sprintf("x_%d_%d", b, f) }
	cVar := func(e int) string { return fmt.Sprintf("c_%d", e) }

	// Collect all directed edges, and index them so the latency constraint can
	// refer to the same variable as the cut constraints.
	var edges []dirEdge
	edgeIdx := make(map[[2]string]int)
	for _, u := range funcs {
		for _, v := range u.Children {
			if u.ID == v.ID {
				continue // self-edge: never a cut
			}
			key := [2]string{u.ID, v.ID}
			if _, dup := edgeIdx[key]; dup {
				continue
			}
			edgeIdx[key] = len(edges)
			edges = append(edges, dirEdge{u, v})
		}
	}

	// With no edges there is nothing to optimize; Solve handles that case
	// without a solver rather than emitting a degenerate model.
	if len(edges) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("\\* OptiFuse fusion model *\\\n")

	// ── Objective: minimize data transfer cost on cut edges ──────────────────
	// Python: prob += lpSum(u.get_data_transfer_cost(v.id) * is_cut[u.id, v.id] for u, v in all_edges)
	obj := newExpr()
	for i, e := range edges {
		obj.add(e.u.DataTransferCostUSD(e.v.ID), cVar(i))
	}
	sb.WriteString("Minimize\n")
	if obj.empty() {
		// Every edge carries zero bytes. Keep the model well-formed by scoring
		// the first cut variable at zero rather than emitting a bare label.
		writeStatement(&sb, "obj:", []term{{0, cVar(0)}}, "")
	} else {
		writeStatement(&sb, "obj:", obj.terms(), "")
	}

	sb.WriteString("Subject To\n")

	// 1. Each function is assigned to exactly one group root.
	// Python: for f in app.functions: prob += lpSum(x[b.id, f.id] for b in roots) == 1
	for f := range funcs {
		e := newExpr()
		for b := range funcs {
			e.add(1, xVar(b, f))
		}
		writeStatement(&sb, fmt.Sprintf("assign_%d:", f), e.terms(), " = 1")
	}

	// 2. Root integrity: x[b,f] <= x[b,b].
	// Skipped when b == f, where it degenerates to 0 <= 0.
	// Python: for b in roots: for f in app.functions: prob += x[b.id, f.id] <= x[b.id, b.id]
	for b := range funcs {
		for f := range funcs {
			if b == f {
				continue
			}
			e := newExpr()
			e.add(1, xVar(b, f))
			e.add(-1, xVar(b, b))
			writeStatement(&sb, fmt.Sprintf("root_%d_%d:", b, f), e.terms(), " <= 0")
		}
	}

	// 3. Memory per group: sum(mem[f] * x[b,f]) <= maxMemory * x[b,b].
	// Python: prob += lpSum(f.memory * x[b.id, f.id] for f in app.functions) <= max_memory * x[b.id, b.id]
	for b := range funcs {
		e := newExpr()
		for f, fn := range funcs {
			e.add(float64(fn.MemoryMB), xVar(b, f))
		}
		e.add(-float64(app.MaxMemoryMB), xVar(b, b))
		writeStatement(&sb, fmt.Sprintf("mem_%d:", b), e.terms(), " <= 0")
	}

	// 4. Cut definition: c[e] >= x[b,u] - x[b,v] and c[e] >= x[b,v] - x[b,u].
	// Python: for u, v in all_edges: for b in roots:
	//             prob += is_cut[u,v] >= x[b,u] - x[b,v]
	//             prob += is_cut[u,v] >= x[b,v] - x[b,u]
	for i, edge := range edges {
		u, v := idxOf[edge.u.ID], idxOf[edge.v.ID]
		for b := range funcs {
			a := newExpr()
			a.add(1, cVar(i))
			a.add(-1, xVar(b, u))
			a.add(1, xVar(b, v))
			writeStatement(&sb, fmt.Sprintf("cutA_%d_%d:", i, b), a.terms(), " >= 0")

			c := newExpr()
			c.add(1, cVar(i))
			c.add(1, xVar(b, u))
			c.add(-1, xVar(b, v))
			writeStatement(&sb, fmt.Sprintf("cutB_%d_%d:", i, b), c.terms(), " >= 0")
		}
	}

	// 5. Latency on the critical path.
	// Python: prob += runtime_sum + lpSum(hop_delay * is_cut[u,v] for cp_edges) <= max_latency
	//
	// Only consecutive pairs that are real edges get a term. A criticalPath
	// naming functions that do not actually call each other would otherwise
	// reference a variable declared nowhere else, which the reader treats as a
	// fresh continuous variable — the constraint would then be satisfiable by
	// setting it to zero and the latency budget silently ignored.
	runtimeSum := 0
	for _, f := range critPath {
		runtimeSum += f.RuntimeMs()
	}
	lat := newExpr()
	for i := 0; i < len(critPath)-1; i++ {
		key := [2]string{critPath[i].ID, critPath[i+1].ID}
		if ei, ok := edgeIdx[key]; ok {
			lat.add(float64(app.NetworkHopMS), cVar(ei))
		}
	}
	if !lat.empty() {
		writeStatement(&sb, "latency:", lat.terms(),
			fmt.Sprintf(" <= %d", app.MaxLatencyMS-runtimeSum))
	}

	// ── Binary declarations ───────────────────────────────────────────────────
	sb.WriteString("Binary\n")
	for b := range funcs {
		for f := range funcs {
			sb.WriteString(" " + xVar(b, f) + "\n")
		}
	}
	for i := range edges {
		sb.WriteString(" " + cVar(i) + "\n")
	}
	sb.WriteString("End\n")

	return sb.String(), edges
}

// Solve formulates the MtxILP problem, writes a CPLEX LP file, invokes glpsol
// (or cbc), parses the solution and returns the resulting partition.
//
// This is a direct translation of the PuLP model in optimal.py.
func Solve(app *domain.Application) (*Result, error) {
	fm := app.FunctionsMap()
	funcs := app.Functions

	if len(funcs) == 0 {
		return &Result{Optimal: true, Status: "OPTIMAL"}, nil
	}

	lpText, edges := buildLP(app)

	// No edges means every feasible partition scores zero, so singletons are as
	// optimal as anything else.
	if len(edges) == 0 {
		groups := make([][]*domain.LambdaFunction, len(funcs))
		for i, f := range funcs {
			groups[i] = []*domain.LambdaFunction{f}
		}
		return &Result{Optimal: true, Status: "OPTIMAL (no edges)", Groups: groups}, nil
	}

	xVar := func(b, f int) string { return fmt.Sprintf("x_%d_%d", b, f) }

	// ── Write to temp file and invoke the solver ──────────────────────────────
	lpFile, err := os.CreateTemp("", "optifuse_*.lp")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp LP file: %w", err)
	}
	defer os.Remove(lpFile.Name())

	if _, err := lpFile.WriteString(lpText); err != nil {
		return nil, fmt.Errorf("failed to write LP file: %w", err)
	}
	lpFile.Close()

	solFile := lpFile.Name() + ".sol"
	defer os.Remove(solFile)

	// Try glpsol first, fall back to cbc.
	if _, err := exec.LookPath("glpsol"); err == nil {
		out, err := exec.Command("glpsol", "--lp", lpFile.Name(), "-o", solFile, "--tmlim", "60").CombinedOutput()
		if err != nil {
			return &Result{Status: fmt.Sprintf("glpsol error: %s\n%s", err, out)}, nil
		}
	} else {
		out, err := exec.Command("cbc", lpFile.Name(), "solve", "solution", solFile).CombinedOutput()
		if err != nil {
			return &Result{Status: fmt.Sprintf("cbc error: %s\n%s", err, out)}, nil
		}
	}

	// ── Parse solution ────────────────────────────────────────────────────────
	solBytes, err := os.ReadFile(solFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read solution file: %w", err)
	}
	solText := string(solBytes)

	if status, ok := solvedOptimally(solText); !ok {
		return &Result{Status: status}, nil
	}

	values := parseColumnValues(solText)

	// Build groups: for each b with x[b,b] = 1, collect every f with x[b,f] = 1.
	var groups [][]*domain.LambdaFunction
	for b := range funcs {
		if values[xVar(b, b)] < 0.5 {
			continue // not a group root
		}
		var group []*domain.LambdaFunction
		for f, fn := range funcs {
			if values[xVar(b, f)] > 0.5 {
				if _, ok := fm[fn.ID]; ok {
					group = append(group, fn)
				}
			}
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
	}

	return &Result{Optimal: true, Status: "OPTIMAL", Groups: groups}, nil
}

// solvedOptimally reports whether the solver proved optimality, returning the
// status text when it did not.
//
// A plain strings.Contains(text, "OPTIMAL") is not enough: glpsol reports
// "INTEGER NON-OPTIMAL" when it hits the time limit with a feasible incumbent,
// which contains "OPTIMAL" as a substring and would be accepted as proven.
func solvedOptimally(solText string) (string, bool) {
	upper := strings.ToUpper(solText)
	if strings.Contains(upper, "NON-OPTIMAL") || strings.Contains(upper, "NOT OPTIMAL") {
		return "solver stopped before proving optimality (time limit?)", false
	}
	if !strings.Contains(upper, "OPTIMAL") {
		return "infeasible or solver did not find an optimal solution", false
	}
	return "", true
}

// parseColumnValues extracts variable values from a solver solution file.
//
// glpsol's printable format is
//
//	No. Column name       Activity     Lower bound   Upper bound
//	  1 x_0_0        *              1             0             1
//
// where the '*' marks an integer column and is absent for continuous ones. cbc
// writes "<index> <name> <value> <cost>". Both are handled by taking the first
// field that parses as a number after the name, rather than assuming a fixed
// column offset.
func parseColumnValues(solText string) map[string]float64 {
	values := make(map[string]float64)
	for _, line := range strings.Split(solText, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// Row index, then variable name.
		if _, err := strconv.Atoi(fields[0]); err != nil {
			continue
		}
		name := fields[1]
		if !strings.HasPrefix(name, "x_") && !strings.HasPrefix(name, "c_") {
			continue
		}
		for _, f := range fields[2:] {
			if v, err := strconv.ParseFloat(f, 64); err == nil {
				values[name] = v
				break
			}
		}
	}
	return values
}
