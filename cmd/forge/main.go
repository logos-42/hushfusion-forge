// Command forge is the single entry point of the HUSHFUSION Forge engine.
//
// It is the *wiring* owner: every other package is reachable from here and
// nothing else is. The commands map onto the acceptance gates in CONTRACT.md:
//
//	baseline    the human the machine is asked to beat, with all its raw terms
//	verify      end-to-end integration gate (G4/G5/G8/G10 inputs, one PASS/FAIL table)
//	xcheck      Br/Bz/|B| export for the independent Python oracle (G5 input)
//	run         one algorithm, one seed, one budget, through the registry (G7)
//	benchmark   equal-budget comparison across methods and seeds
//	rules       mine replicated design rules out of a registry
//	report      render the Chinese run report, honest-limits section included
//	registry    inspect a registry and check its integrity (G8)
//	version     version, spec shape and the frozen schema facts
//
// Exit codes: 0 success, 1 a check failed / a package reported an error,
// 2 bad usage. --help on any command exits 0.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/baseline"
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/experiment"
	"github.com/logos-42/hushfusion-forge/internal/knowledge"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/registry"
	"github.com/logos-42/hushfusion-forge/internal/report"
	"github.com/logos-42/hushfusion-forge/internal/rlenv"
	"github.com/logos-42/hushfusion-forge/internal/runner"
	"github.com/logos-42/hushfusion-forge/internal/search"
)

// Version is the engine version (v0.1 interface freeze).
const Version = "0.1.0"

// buildCommit is injected at build time:
//
//	go build -ldflags "-X main.buildCommit=$(git rev-parse HEAD)" ./cmd/forge
//
// The Go standard library here cannot shell out to git (no os/exec in this
// build's dependency set), so when nothing is injected and FORGE_GIT_COMMIT is
// unset we print "unknown" instead of guessing a commit.
var buildCommit = ""

// Repo-relative defaults. Paths are resolved from the working directory, with an
// upward search for the golden test data (see findFile) so that running from a
// subdirectory does not silently compare against nothing.
const (
	defTag         = "phase0"
	defRunDir      = "runs/phase0"
	defRunTag      = "ad_hoc"
	defRunRegistry = "runs/scratch/registry.jsonl"
	defXcheckOut   = "runs/scratch/field_samples.json"
	defRulesOut    = "knowledge/design_rules.md"
	defReportOut   = "runs/phase0/report.md"
	goldenBaseline = "testdata/golden_baseline.json"
	goldenSamples  = "testdata/golden_field_samples.json"
	goldenSpec     = "testdata/golden_spec.json"
	solverName     = "analytic-vacuum-loops"
)

// Tolerances fixed by CONTRACT.md §5. Loosening these would be falsifying the
// acceptance criterion itself, so they are named constants and never flags.
const (
	tolScore   = 1e-6 // |score - golden_score|
	tolMetrics = 1e-6 // relative, per metric
	tolTerms   = 1e-6 // absolute, per raw score term (see compareBaselineEval)
	tolField   = 1e-9 // relative, per |B| field sample (the contract's criterion)
	tolComp    = 1e-6 // relative, per Br/Bz component (see compareFieldSeries)
	tolSolvers = 1e-9 // relative, analytic vs discrete Biot–Savart
)

var allMethods = []string{"random", "lhs", "evolution", "evolution_warm"}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	case "version", "--version", "-V":
		return cmdVersion(rest)
	case "baseline", "base":
		return cmdBaseline(rest)
	case "verify":
		return cmdVerify(rest)
	case "xcheck":
		return cmdXcheck(rest)
	case "run":
		return cmdRun(rest)
	case "benchmark", "bench":
		return cmdBenchmark(rest)
	case "rules":
		return cmdRules(rest)
	case "report":
		return cmdReport(rest)
	case "registry", "reg":
		return cmdRegistry(rest)
	default:
		fmt.Fprintf(os.Stderr, "forge: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return 2
	}
}

func usage(w *os.File) {
	fmt.Fprintf(w, `usage: forge <command> [flags]

HUSHFUSION Forge v%s — the design loop: parameterise → physics → score → search → registry → rules.

commands:
  baseline    print the human baseline: design, metrics, terms, score
  verify      end-to-end integration gate (spec → baseline → golden → registry → anti-fabrication)
  xcheck      export Br/Bz/|B| samples for the independent Python oracle (G5 input)
  run         run one algorithm at one seed and budget, recording into the registry
  benchmark   equal-budget benchmark across methods and seeds
  rules       mine replicated design rules from a registry
  report      render the markdown run report
  registry    inspect a registry and check its integrity
  version     print version, spec shape and frozen schema facts

exit codes: 0 ok, 1 a gate/command failed, 2 usage error.
'forge <command> --help' prints the flags of one command.
environment: FORGE_GIT_COMMIT overrides the commit reported by 'forge version'.
`, Version)
}

// ---------------------------------------------------------------------------
// flag plumbing
// ---------------------------------------------------------------------------

func newFlagSet(name, synopsis, blurb string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: forge %s\n\n%s\n\nflags:\n", synopsis, blurb)
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags returns -1 when the caller should continue, or the process exit
// code (0 for --help, 2 for a parse error or stray positional argument).
func parseFlags(fs *flag.FlagSet, args []string) int {
	err := fs.Parse(args)
	if err == nil {
		if fs.NArg() > 0 {
			fmt.Fprintf(os.Stderr, "forge %s: unexpected argument %q\n\n", fs.Name(), fs.Arg(0))
			fs.Usage()
			return 2
		}
		return -1
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "forge: error: "+format+"\n", args...)
	return 1
}

func note(format string, args ...any) { fmt.Printf(format+"\n", args...) }

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

func defaultSpec() config.Spec { return config.DefaultSpec() }

// analyticEvaluator builds the evaluator over the exact (elliptic-integral)
// solver, using the human baseline's ohmic cost as the cost reference so that
// "cost == 1.0" means "as expensive as the human design".
func analyticEvaluator(spec config.Spec, costRef float64) *objective.Evaluator {
	return objective.NewEvaluator(spec, physics.AnalyticSolver{}, costRef, physics.BuildGrids(spec))
}

func baselineDesign() (baseline.Baseline, error) {
	base, err := baseline.TextbookMirror(config.DefaultSpec())
	if err != nil {
		return baseline.Baseline{}, fmt.Errorf("baseline.TextbookMirror: %w", err)
	}
	return base, nil
}

// findFile resolves a repo-relative path, searching upward from the working
// directory (max 6 levels) so commands still work from a subdirectory.
func findFile(rel string) (string, error) {
	if _, err := os.Stat(rel); err == nil {
		return rel, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return rel, err
	}
	for i := 0; i < 6; i++ {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		cand := filepath.Join(dir, rel)
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return rel, fmt.Errorf("%s not found (searched upward from the working directory)", rel)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func openRegistry(path string) (*registry.Registry, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	reg, err := registry.Open(path)
	if err != nil {
		return nil, fmt.Errorf("registry.Open(%s): %w", path, err)
	}
	return reg, nil
}

func openExistingRegistry(path string) (*registry.Registry, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("registry %s does not exist (run 'forge benchmark' or 'forge run' first)", path)
	}
	return openRegistry(path)
}

func resolveCommit() string {
	if c := os.Getenv("FORGE_GIT_COMMIT"); c != "" {
		return c
	}
	if buildCommit != "" {
		return buildCommit
	}
	return "unknown"
}

// splitList parses a comma-separated flag value ("0,1,2" / "random,lhs"),
// trimming spaces and dropping empty fields.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitInts(s string) ([]int, error) {
	var out []int
	for _, part := range splitList(s) {
		var v int
		if _, err := fmt.Sscanf(part, "%d", &v); err != nil {
			return nil, fmt.Errorf("cannot parse %q as an integer", part)
		}
		out = append(out, v)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// metrics access (frozen JSON tags; a key missing here is a schema drift bug)
// ---------------------------------------------------------------------------

var metricKeys = []string{
	"B_mid_T", "B_throat_T", "z_throat_m", "mirror_ratio", "volume_good",
	"ripple", "B_coil_max_T", "min_coil_gap_m", "cost_proxy", "n_coils", "mu0",
}

// metricBoolKeys are the metrics that are not floats.
var metricBoolKeys = []string{"coil_proximity_floor_hit"}

func metricValue(m physics.Metrics, key string) (float64, bool) {
	switch key {
	case "B_mid_T":
		return m.BMidT, true
	case "B_throat_T":
		return m.BThroatT, true
	case "z_throat_m":
		return m.ZThroatM, true
	case "mirror_ratio":
		return m.MirrorRatio, true
	case "volume_good":
		return m.VolumeGood, true
	case "ripple":
		return m.Ripple, true
	case "B_coil_max_T":
		return m.BCoilMaxT, true
	case "min_coil_gap_m":
		return m.MinCoilGapM, true
	case "cost_proxy":
		return m.CostProxy, true
	case "n_coils":
		return float64(m.NCoils), true
	case "mu0":
		return m.MU0, true
	case "coil_proximity_floor_hit":
		if m.CoilProximityFloorHit {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// relDiff is the comparison used by the golden gates: relative for values of
// real size, absolute for values that are zero by construction (a relative
// comparison against 0 would be undefined, not strict).
func relDiff(got, want float64) float64 {
	den := math.Max(math.Abs(want), 1e-9)
	if den == 0 {
		den = 1e-9
	}
	return math.Abs(got-want) / den
}

func metricMap(m physics.Metrics) map[string]float64 {
	out := map[string]float64{}
	for _, k := range metricKeys {
		if v, ok := metricValue(m, k); ok {
			out[k] = v
		}
	}
	for _, k := range metricBoolKeys {
		if v, ok := metricValue(m, k); ok {
			out[k] = v
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func printMetrics(m physics.Metrics, label string) {
	note("%s:", label)
	for _, k := range metricKeys {
		if v, ok := metricValue(m, k); ok {
			note("  %-24s %v", k, v)
		}
	}
	note("  %-24s %v", "coil_proximity_floor_hit", m.CoilProximityFloorHit)
}

func printTermTable(terms, weighted, penalties map[string]float64) {
	note("terms (raw):")
	for _, k := range sortedKeys(terms) {
		note("  %-24s %v", k, terms[k])
	}
	note("weighted:")
	for _, k := range sortedKeys(weighted) {
		note("  %-24s %v", k, weighted[k])
	}
	note("penalties:")
	if len(penalties) == 0 {
		note("  (none)")
	}
	for _, k := range sortedKeys(penalties) {
		note("  %-24s %v", k, penalties[k])
	}
}

func printDesign(design []float64) {
	n := len(design) / 3
	note("design (canonical, r|z|I), %d coils:", n)
	if n <= 0 {
		note("  (empty)")
		return
	}
	note("  %-4s %-14s %-14s %-16s", "#", "radius_m", "z_m", "current_A")
	for i := 0; i < n; i++ {
		note("  %-4d %-14v %-14v %-16v", i, design[i], design[n+i], design[2*n+i])
	}
	note("  r = %v", design[:n])
	note("  z = %v", design[n:2*n])
	note("  I = %v", design[2*n:])
}

// ---------------------------------------------------------------------------
// baseline
// ---------------------------------------------------------------------------

func cmdBaseline(args []string) int {
	fs := newFlagSet("baseline", "baseline [--json] [--golden PATH]",
		"Print the human baseline (Helmholtz-like cell + mirror throats): its design,\n"+
			"every physics metric, every raw score term and the composite score.")
	asJSON := fs.Bool("json", false, "emit the baseline evaluation as JSON (machine readable)")
	golden := fs.String("golden", goldenBaseline, "golden baseline file to diff against (empty string to skip)")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	spec := defaultSpec()
	base, err := baselineDesign()
	if err != nil {
		return fail("%v", err)
	}
	ev := analyticEvaluator(spec, base.Cost)
	res := ev.Evaluate(base.Design)

	if *asJSON {
		payload := map[string]any{
			"name":      base.Name,
			"note":      base.Note,
			"design":    base.Design,
			"coils":     base.Coils,
			"cost_ref":  base.Cost,
			"score":     res.Score,
			"feasible":  res.Feasible,
			"terms":     res.Terms,
			"weighted":  res.Weighted,
			"penalties": res.Penalties,
			"metrics":   metricMap(res.Metrics),
			"spec":      spec.AsMap(),
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fail("marshal baseline: %v", err)
		}
		os.Stdout.Write(append(b, '\n'))
		return 0
	}

	note("human baseline: %s", base.Name)
	note("note: %s", base.Note)
	note("coils: %d", len(base.Coils))
	printDesign(base.Design)
	note("cost_ref (ohmic cost proxy) = %v", base.Cost)
	printMetrics(res.Metrics, "metrics")
	printTermTable(res.Terms, res.Weighted, res.Penalties)
	note("score = %v", res.Score)
	note("feasible = %v", res.Feasible)

	if *golden != "" {
		path, ferr := findFile(*golden)
		if ferr != nil {
			note("golden: %v (skipped)", ferr)
			return 0
		}
		var gold goldenBaselineFile
		if rerr := readJSON(path, &gold); rerr != nil {
			note("golden: %v (skipped)", rerr)
			return 0
		}
		d := math.Abs(res.Score - gold.Score)
		verdict := "within"
		if d > tolScore {
			verdict = "OUTSIDE"
		}
		note("%s: golden score = %v, |delta| = %v (%s tol %v) — run 'forge verify' for the full gate",
			path, gold.Score, d, verdict, tolScore)
	}
	return 0
}

// ---------------------------------------------------------------------------
// verify — the end-to-end integration gate
// ---------------------------------------------------------------------------

type goldenBaselineFile struct {
	Name      string             `json:"name"`
	Note      string             `json:"note"`
	Coils     []physics.Coil     `json:"coils"`
	CostProxy float64            `json:"cost_proxy"`
	CostRef   float64            `json:"cost_ref"`
	Design    []float64          `json:"design"`
	Feasible  bool               `json:"feasible"`
	Metrics   map[string]float64 `json:"metrics"`
	Penalties map[string]float64 `json:"penalties"`
	Score     float64            `json:"score"`
	Terms     map[string]float64 `json:"terms"`
	Weighted  map[string]float64 `json:"weighted"`
}

type goldenSample struct {
	Design     []float64 `json:"design"`
	DesignName string    `json:"design_name"`
	Solver     string    `json:"solver"`
	PointsR    []float64 `json:"points_r"`
	PointsZ    []float64 `json:"points_z"`
	Br         []float64 `json:"br"`
	Bz         []float64 `json:"bz"`
	BMag       []float64 `json:"b_mag"`
}

type goldenSamplesFile struct {
	Samples []goldenSample `json:"samples"`
}

type gate struct {
	name string
	ok   bool
	msg  string
}

// runGate runs one gate, converting a panic from a not-yet-implemented stage
// into a FAIL row instead of a stack trace: the point of the gate is to say
// which stage is missing, loudly, not to crash the reviewer's terminal.
func runGate(name string, fn func() (string, error)) (g gate) {
	g.name = name
	defer func() {
		if r := recover(); r != nil {
			g.ok = false
			g.msg = fmt.Sprintf("FAIL: panic from an upstream package: %v", r)
		}
	}()
	msg, err := fn()
	if err != nil {
		g.ok = false
		g.msg = err.Error()
		return g
	}
	g.ok = true
	g.msg = msg
	return g
}

type verifyState struct {
	spec    config.Spec
	base    baseline.Baseline
	res     objective.EvalResult
	gold    goldenBaselineFile
	samples goldenSamplesFile
	specKey map[string]any

	// fieldGotBr keeps the computed Br series per golden sample index, so the
	// gate's message can quote the actual number behind its worst deviation.
	fieldGotBr map[int][]float64
}

func cmdVerify(args []string) int {
	fs := newFlagSet("verify", "verify [--verbose]",
		"Run the end-to-end integration gate and print a PASS/FAIL table:\n"+
			"  spec schema parity, baseline solve, golden score/metrics/terms, golden field\n"+
			"  samples, analytic-vs-discrete solver agreement, registry write/read round-trip\n"+
			"  with lineage integrity, the anti-fabrication gate (LoadPolicy must fail), and\n"+
			"  the rlenv observation layout.\n"+
			"Any FAIL exits non-zero.")
	verbose := fs.Bool("verbose", false, "print the full message of every gate, not just the first line")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	st := &verifyState{spec: defaultSpec()}
	note("forge verify — end-to-end integration gate (v%s)", Version)
	note("spec: n_coils=%d n_params=%d b_ref=%v mirror_ref=%v",
		st.spec.NCoils, st.spec.NParams(), st.spec.BRef, st.spec.MirrorRef)
	note("tolerances (CONTRACT.md §5): score %v, metrics/terms rel %v, field rel %v, solvers rel %v",
		tolScore, tolMetrics, tolField, tolSolvers)
	note("")

	gates := []gate{
		runGate("load testdata/golden_*.json", st.loadGolden),
		runGate("spec ↔ golden_spec schema parity", st.gateSpecParity),
		runGate("baseline solve (TextbookMirror)", st.gateBaselineSolve),
		runGate("baseline evaluation vs golden score+terms", st.gateGoldenScore),
		runGate("baseline metrics vs golden metrics", st.gateGoldenMetrics),
		runGate("golden field samples (analytic solver)", st.gateGoldenFields),
		runGate("analytic vs discrete Biot–Savart", st.gateSolvers),
		runGate("registry write → read-back consistency + lineage", st.gateRegistry),
		runGate("rlenv: LoadPolicy must fail (G10 anti-fabrication)", st.gateNoLearnedPolicy),
		runGate("rlenv: observation layout + episode lineage chain", st.gateRLEnvLayout),
	}

	nPass, nFail := 0, 0
	for i, g := range gates {
		status := "[PASS]"
		if !g.ok {
			status = "[FAIL]"
			nFail++
		} else {
			nPass++
		}
		head := strings.SplitN(g.msg, "\n", 2)[0]
		note("%s %2d. %-52s %s", status, i+1, g.name, head)
		if *verbose && strings.Contains(g.msg, "\n") {
			for _, line := range strings.Split(g.msg, "\n")[1:] {
				note("            %s", line)
			}
		}
	}
	note("")
	if nFail == 0 {
		note("verify: PASS (%d/%d gates)", nPass, len(gates))
		return 0
	}
	note("verify: FAIL (%d/%d gates, %d failed)", nPass, len(gates), nFail)
	return 1
}

func (st *verifyState) loadGolden() (string, error) {
	paths := map[string]string{}
	for _, rel := range []string{goldenBaseline, goldenSamples, goldenSpec} {
		p, err := findFile(rel)
		if err != nil {
			return "", err
		}
		paths[rel] = p
	}
	if err := readJSON(paths[goldenBaseline], &st.gold); err != nil {
		return "", err
	}
	if err := readJSON(paths[goldenSamples], &st.samples); err != nil {
		return "", err
	}
	if err := readJSON(paths[goldenSpec], &st.specKey); err != nil {
		return "", err
	}
	if len(st.samples.Samples) == 0 {
		return "", fmt.Errorf("%s contains no samples", paths[goldenSamples])
	}
	return fmt.Sprintf("baseline score %v, %d field-sample designs, spec keys %d",
		st.gold.Score, len(st.samples.Samples), len(st.specKey)), nil
}

// keySetsMatch compares nested key sets; every golden key must exist on the Go
// side (a missing key is a schema break in either direction).
func keySetsMatch(golden, goMap map[string]any, label string) error {
	for _, k := range sortedKeys(golden) {
		if _, ok := goMap[k]; !ok {
			return fmt.Errorf("%s: key %q present in the golden spec but absent from Spec.AsMap()", label, k)
		}
	}
	for _, k := range sortedKeys(goMap) {
		if _, ok := golden[k]; !ok {
			return fmt.Errorf("%s: key %q produced by Spec.AsMap() but absent from the golden spec", label, k)
		}
	}
	return nil
}

func (st *verifyState) gateSpecParity() (string, error) {
	// testdata/golden_spec.json is {"provenance": {...}, "spec": {...}}; the
	// parity gate is about the spec object itself.
	goldenSpec, ok := st.specKey["spec"].(map[string]any)
	if !ok {
		return "", errors.New(`golden spec has no "spec" object`)
	}
	got := st.spec.AsMap()
	if err := keySetsMatch(goldenSpec, got, "spec"); err != nil {
		return "", err
	}
	for _, nested := range []string{"bounds", "weights"} {
		goldenNested, ok := goldenSpec[nested].(map[string]any)
		if !ok {
			return "", fmt.Errorf("golden spec has no %q object", nested)
		}
		goNested, ok := got[nested].(map[string]any)
		if !ok {
			return "", fmt.Errorf("Spec.AsMap() has no %q object", nested)
		}
		if err := keySetsMatch(goldenNested, goNested, nested); err != nil {
			return "", err
		}
	}
	prov := ""
	if p, ok := st.specKey["provenance"].(map[string]any); ok {
		prov = fmt.Sprintf(" (golden provenance: python %v, numpy %v)", p["python"], p["numpy"])
	}
	return fmt.Sprintf("%d spec keys, bounds/weights nested keys match%s", len(got), prov), nil
}

func (st *verifyState) gateBaselineSolve() (string, error) {
	base, err := baselineDesign()
	if err != nil {
		return "", err
	}
	if len(base.Design) != st.spec.NParams() {
		return "", fmt.Errorf("baseline design has %d parameters, want %d", len(base.Design), st.spec.NParams())
	}
	if base.Cost <= 0 {
		return "", fmt.Errorf("baseline cost reference must be positive, got %v", base.Cost)
	}
	st.base = base
	// cost_ref in the golden file is the baseline's own ohmic cost.
	if st.gold.CostRef > 0 {
		if d := relDiff(base.Cost, st.gold.CostRef); d > tolMetrics {
			return "", fmt.Errorf("baseline cost %v vs golden cost_ref %v (rel diff %.3g > %v)",
				base.Cost, st.gold.CostRef, d, tolMetrics)
		}
	}
	return fmt.Sprintf("name=%s cost_ref=%v (golden rel diff %.2g)", base.Name, base.Cost,
		relDiff(base.Cost, st.gold.CostRef)), nil
}

// compareBaselineEval is the golden comparison itself, split out from the gate
// so that it can be tested (and made to go red) without evaluating anything.
//
// Tolerance rule: the composite score is compared absolutely at tolScore (the
// contract's criterion). Raw terms are compared ABSOLUTELY at tolTerms, not
// relatively, because log10 terms are ill-conditioned near 1: the field term is
// log10(B_mid/B_ref) and B_mid is produced by solving a current to ~1e-10
// relative, so a perfectly correct implementation lands ~3e-11 away from the
// golden 9.6e-17 value — a 3e-2 relative error in a term whose absolute size is
// 1e-16. An absolute criterion still catches any real deviation (a 1e-3 term
// error fails) without reading floating-point noise as a disagreement.
func compareBaselineEval(res objective.EvalResult, gold goldenBaselineFile) (string, error) {
	dScore := math.Abs(res.Score - gold.Score)
	if dScore > tolScore {
		return "", fmt.Errorf("score %v vs golden %v: |delta| %.3g > %v", res.Score, gold.Score, dScore, tolScore)
	}
	worstKey, worst := "", 0.0
	for _, k := range sortedKeys(gold.Terms) {
		want := gold.Terms[k]
		got, ok := res.Terms[k]
		if !ok {
			return "", fmt.Errorf("term %q present in golden but missing from EvalResult.Terms", k)
		}
		if d := math.Abs(got - want); d > worst {
			worstKey, worst = k, d
		}
	}
	if worst > tolTerms {
		return "", fmt.Errorf("term %q absolute diff %.3g > %v", worstKey, worst, tolTerms)
	}
	if res.Feasible != gold.Feasible {
		return "", fmt.Errorf("baseline feasibility %v vs golden %v", res.Feasible, gold.Feasible)
	}
	return fmt.Sprintf("score %v (|delta| %.2g), worst term %s |diff| %.2g, feasible %v",
		res.Score, dScore, worstKey, worst, res.Feasible), nil
}

// compareBaselineMetrics compares every metric the golden file carries, and
// fails on a golden metric the Go side does not produce (schema drift).
func compareBaselineMetrics(m physics.Metrics, goldenMetrics map[string]float64) (string, error) {
	got := metricMap(m)
	worstKey, worst := "", 0.0
	checked := 0
	for _, k := range sortedKeys(goldenMetrics) {
		want := goldenMetrics[k]
		v, ok := got[k]
		if !ok {
			return "", fmt.Errorf("metric %q present in golden but not produced by physics.Metrics", k)
		}
		checked++
		if d := relDiff(v, want); d > worst {
			worstKey, worst = k, d
		}
	}
	if worst > tolMetrics {
		return "", fmt.Errorf("metric %q rel diff %.3g > %v", worstKey, worst, tolMetrics)
	}
	return fmt.Sprintf("%d metrics within rel %v, worst %s rel %.2g", checked, tolMetrics, worstKey, worst), nil
}

func (st *verifyState) gateGoldenScore() (string, error) {
	if len(st.base.Design) == 0 {
		return "", errors.New("no baseline available (earlier gate failed)")
	}
	ev := analyticEvaluator(st.spec, st.base.Cost)
	res := ev.Evaluate(st.base.Design)
	st.res = res
	return compareBaselineEval(res, st.gold)
}

func (st *verifyState) gateGoldenMetrics() (string, error) {
	if len(st.base.Design) == 0 {
		return "", errors.New("no baseline evaluation available (earlier gate failed)")
	}
	return compareBaselineMetrics(st.res.Metrics, st.gold.Metrics)
}

// fieldDiff is the worst relative deviation found in one design's field series.
type fieldDiff struct {
	Mag, Br, Bz         float64
	MagAt, BrAt, BzAt   int
	MagSample, BrSample int
}

// compareFieldSeries compares one design's computed field against its golden
// series.
//
// The contract's 1e-9 criterion is on |B| (AnalyticSolver.Magnitude), and that
// is what this gate fails on. The Cartesian components are compared at tolComp:
// B_r vanishes on the axis and is computed there by a difference of nearly equal
// terms, so near-axis points carry a conditioning floor far above 1e-9 relative
// — a 1e-8 excursion in Br at r=0.02 m is arithmetic, not a wrong field. A
// component error of 1e-6 relative does fail, which is what catches a real bug.
func compareFieldSeries(s goldenSample, gotMag, gotBr, gotBz []float64) (fieldDiff, error) {
	var d fieldDiff
	if len(gotMag) != len(s.BMag) || len(gotBr) != len(s.Br) || len(gotBz) != len(s.Bz) {
		return d, fmt.Errorf("design %s: solver returned %d/%d/%d values, golden has %d/%d/%d",
			s.DesignName, len(gotMag), len(gotBr), len(gotBz), len(s.BMag), len(s.Br), len(s.Bz))
	}
	for i := range gotMag {
		if r := relDiff(gotMag[i], s.BMag[i]); r > d.Mag {
			d.Mag, d.MagAt = r, i
		}
		if r := relDiff(gotBr[i], s.Br[i]); r > d.Br {
			d.Br, d.BrAt = r, i
		}
		if r := relDiff(gotBz[i], s.Bz[i]); r > d.Bz {
			d.Bz, d.BzAt = r, i
		}
	}
	if d.Mag > tolField {
		return d, fmt.Errorf("design %s: |B| rel %.3g > %v at point %d (r=%v z=%v: got %v, golden %v)",
			s.DesignName, d.Mag, tolField, d.MagAt, s.PointsR[d.MagAt], s.PointsZ[d.MagAt],
			gotMag[d.MagAt], s.BMag[d.MagAt])
	}
	if d.Br > tolComp {
		return d, fmt.Errorf("design %s: Br rel %.3g > %v (component tolerance) at point %d (r=%v z=%v: got %v, golden %v)",
			s.DesignName, d.Br, tolComp, d.BrAt, s.PointsR[d.BrAt], s.PointsZ[d.BrAt], gotBr[d.BrAt], s.Br[d.BrAt])
	}
	if d.Bz > tolComp {
		return d, fmt.Errorf("design %s: Bz rel %.3g > %v (component tolerance) at point %d (r=%v z=%v: got %v, golden %v)",
			s.DesignName, d.Bz, tolComp, d.BzAt, s.PointsR[d.BzAt], s.PointsZ[d.BzAt], gotBz[d.BzAt], s.Bz[d.BzAt])
	}
	return d, nil
}

func (st *verifyState) gateGoldenFields() (string, error) {
	worst := fieldDiff{}
	nPoints := 0
	solver := physics.AnalyticSolver{}
	for si, s := range st.samples.Samples {
		if len(s.PointsR) != len(s.PointsZ) {
			return "", fmt.Errorf("sample %d: %d r-points vs %d z-points", si, len(s.PointsR), len(s.PointsZ))
		}
		coils, err := physics.VectorToCoils(s.Design, st.spec)
		if err != nil {
			return "", fmt.Errorf("sample %d (%s): VectorToCoils: %w", si, s.DesignName, err)
		}
		mag := solver.Magnitude(coils, s.PointsR, s.PointsZ)
		br, bz := physics.CoilsetField(coils, s.PointsR, s.PointsZ)
		d, err := compareFieldSeries(s, mag, br, bz)
		if err != nil {
			return "", err
		}
		if st.fieldGotBr == nil {
			st.fieldGotBr = map[int][]float64{}
		}
		st.fieldGotBr[si] = br
		nPoints += len(mag)
		if d.Mag > worst.Mag {
			worst.Mag, worst.MagAt, worst.MagSample = d.Mag, d.MagAt, si
		}
		if d.Br > worst.Br {
			worst.Br, worst.BrAt, worst.BrSample = d.Br, d.BrAt, si
		}
		if d.Bz > worst.Bz {
			worst.Bz, worst.BzAt = d.Bz, d.BzAt
		}
	}
	ws := st.samples.Samples[worst.MagSample]
	bs := st.samples.Samples[worst.BrSample]
	// Print the golden value beside the worst component deviation: when the
	// golden field is exactly zero there (a symmetry point), a "relative"
	// deviation is really an absolute one of ~1e-17 and should read that way.
	return fmt.Sprintf("%d designs × %d points: max rel |B| %.2g (tol %v) [%s at r=%v z=%v], max rel Br %.2g [%s at r=%v z=%v: %v vs golden %v], Bz %.2g (components tol %v)",
		len(st.samples.Samples), nPoints, worst.Mag, tolField, ws.DesignName,
		ws.PointsR[worst.MagAt], ws.PointsZ[worst.MagAt],
		worst.Br, bs.DesignName, bs.PointsR[worst.BrAt], bs.PointsZ[worst.BrAt],
		st.fieldGotBr[worst.BrSample][worst.BrAt], bs.Br[worst.BrAt],
		worst.Bz, tolComp), nil
}

func (st *verifyState) gateSolvers() (string, error) {
	// Cross-check the two independent implementations on the anchor points of
	// the first golden design (far from the conductors, where the discrete sum
	// converges fastest).
	s := st.samples.Samples[0]
	coils, err := physics.VectorToCoils(s.Design, st.spec)
	if err != nil {
		return "", err
	}
	analytic := physics.AnalyticSolver{}
	discrete := physics.DiscreteSolver{NSeg: 512}
	a := analytic.Magnitude(coils, s.PointsR, s.PointsZ)
	d := discrete.Magnitude(coils, s.PointsR, s.PointsZ)
	if len(a) != len(d) {
		return "", fmt.Errorf("solvers disagree on output length: %d vs %d", len(a), len(d))
	}
	worst := 0.0
	for i := range a {
		if rd := relDiff(d[i], a[i]); rd > worst {
			worst = rd
		}
	}
	if worst > tolSolvers {
		return "", fmt.Errorf("analytic vs discrete max rel diff %.3g > %v (nSeg=%d)", worst, tolSolvers, discrete.NSeg)
	}
	return fmt.Sprintf("%d points, max rel %.2g (nSeg=%d)", len(a), worst, discrete.NSeg), nil
}

func (st *verifyState) gateRegistry() (string, error) {
	if len(st.base.Design) == 0 || len(st.res.Terms) == 0 {
		return "", errors.New("no baseline evaluation available (earlier gate failed)")
	}
	dir, err := os.MkdirTemp("", "forge-verify-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "registry.jsonl")

	reg, err := openRegistry(path)
	if err != nil {
		return "", err
	}
	if reg.Len() != 0 {
		return "", fmt.Errorf("fresh registry reports Len()=%d, want 0", reg.Len())
	}
	wantExp, wantDesign := reg.NextIDs()
	if wantExp != 1 || wantDesign == "" {
		return "", fmt.Errorf("NextIDs() on an empty registry = (%d, %q), want (1, non-empty)", wantExp, wantDesign)
	}

	rec := baselineRecord(st.res, st.base)
	if err := reg.Append(rec); err != nil {
		return "", fmt.Errorf("Append: %w", err)
	}
	if reg.Len() != 1 {
		return "", fmt.Errorf("after one Append, Len()=%d, want 1", reg.Len())
	}

	recs, err := reg.Records()
	if err != nil {
		return "", fmt.Errorf("Records: %w", err)
	}
	if len(recs) != 1 {
		return "", fmt.Errorf("read back %d records, want 1", len(recs))
	}
	got := recs[0]
	if got.ExperimentID != wantExp {
		return "", fmt.Errorf("read-back ExperimentID=%d, want %d (gap-free ids)", got.ExperimentID, wantExp)
	}
	if got.DesignID != wantDesign {
		return "", fmt.Errorf("read-back DesignID=%q, want %q", got.DesignID, wantDesign)
	}
	if math.Abs(got.Score-st.res.Score) > 1e-12 {
		return "", fmt.Errorf("read-back score %v != evaluated score %v", got.Score, st.res.Score)
	}
	if diff := relDiff(got.Metrics.BMidT, st.res.Metrics.BMidT); diff > 1e-12 {
		return "", fmt.Errorf("read-back B_mid_T %v != %v", got.Metrics.BMidT, st.res.Metrics.BMidT)
	}
	for _, k := range sortedKeys(st.res.Terms) {
		if math.Abs(got.Terms[k]-st.res.Terms[k]) > 1e-12 {
			return "", fmt.Errorf("read-back term %q = %v != %v", k, got.Terms[k], st.res.Terms[k])
		}
	}
	if len(got.Params.RadiusM) != st.spec.NCoils || len(got.Params.ZM) != st.spec.NCoils ||
		len(got.Params.CurrentA) != st.spec.NCoils {
		return "", fmt.Errorf("read-back params: %d/%d/%d arrays, want %d each",
			len(got.Params.RadiusM), len(got.Params.ZM), len(got.Params.CurrentA), st.spec.NCoils)
	}

	// A second, child record: lineage integrity must survive the round trip.
	child := rec
	child.ParentDesign = got.DesignID
	child.Generation = got.Generation + 1
	if err := reg.Append(child); err != nil {
		return "", fmt.Errorf("Append child: %w", err)
	}
	problems, err := reg.Check()
	if err != nil {
		return "", fmt.Errorf("Check: %w", err)
	}
	if len(problems) != 0 {
		return "", fmt.Errorf("registry integrity problems: %v", problems)
	}
	fam := reg.Lineage()
	if len(fam[got.DesignID]) != 1 {
		return "", fmt.Errorf("lineage: %s has %d children, want 1", got.DesignID, len(fam[got.DesignID]))
	}
	return fmt.Sprintf("%d records, ids gap-free from 1, parent link intact, Check() clean", reg.Len()), nil
}

// baselineRecord turns an evaluated baseline into a registry record.
func baselineRecord(res objective.EvalResult, base baseline.Baseline) registry.Record {
	design := base.Design
	k := len(design) / 3
	params := registry.Params{
		RadiusM:  append([]float64(nil), design[:k]...),
		ZM:       append([]float64(nil), design[k:2*k]...),
		CurrentA: append([]float64(nil), design[2*k:]...),
	}
	return registry.Record{
		Algorithm:  "human_baseline",
		Seed:       0,
		Generation: 0,
		EvalIndex:  0,
		Tag:        "forge_verify",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Score:      res.Score,
		Feasible:   res.Feasible,
		Params:     params,
		Terms:      res.Terms,
		Weighted:   res.Weighted,
		Penalties:  res.Penalties,
		Metrics:    res.Metrics,
		Note:       "human baseline recorded by 'forge verify' round-trip gate",
	}
}

func (st *verifyState) gateNoLearnedPolicy() (string, error) {
	for _, p := range []string{"", "policy.json", "checkpoints/ppo.bin"} {
		err := rlenv.LoadPolicy(p)
		if err == nil {
			return "", fmt.Errorf("LoadPolicy(%q) returned nil — a learned policy must never be fabricated", p)
		}
		if !errors.Is(err, rlenv.ErrNoLearnedPolicy) {
			return "", fmt.Errorf("LoadPolicy(%q) = %v, want ErrNoLearnedPolicy", p, err)
		}
	}
	if !strings.Contains(rlenv.ErrNoLearnedPolicy.Error(), "Phase 1") {
		return "", errors.New("ErrNoLearnedPolicy does not name Phase 1")
	}
	return "LoadPolicy fails with ErrNoLearnedPolicy for every input (no learned policy exists)", nil
}

func (st *verifyState) gateRLEnvLayout() (string, error) {
	env := rlenv.NewEnv(nullScorer{}, st.spec, 0, 0)
	wantAction := st.spec.NParams()
	wantObs := wantAction + len(rlenv.ObsMetricKeys)
	if env.ActionDim() != wantAction {
		return "", fmt.Errorf("ActionDim=%d, want %d", env.ActionDim(), wantAction)
	}
	if env.ObservationDim() != wantObs {
		return "", fmt.Errorf("ObservationDim=%d, want %d", env.ObservationDim(), wantObs)
	}
	if len(rlenv.ObsMetricKeys) != len(rlenv.ObsMetricRefs) {
		return "", fmt.Errorf("%d metric keys vs %d refs", len(rlenv.ObsMetricKeys), len(rlenv.ObsMetricRefs))
	}

	// End-to-end: an episode evaluated through the real runner must land in the
	// registry as a connected lineage chain (env → runner → registry).
	if len(st.base.Design) == 0 {
		return "", errors.New("no baseline design available (earlier gate failed)")
	}
	dir, err := os.MkdirTemp("", "forge-verify-rlenv-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	reg, err := openRegistry(filepath.Join(dir, "registry.jsonl"))
	if err != nil {
		return "", err
	}
	ev := analyticEvaluator(st.spec, st.base.Cost)
	rn := runner.New(reg, ev, "forge_verify_rlenv")

	const steps = 3
	ep := rlenv.NewEnv(rn, st.spec, steps, 0.15)
	ep.Reset(st.base.Design)
	action := make([]float64, ep.ActionDim())
	rngState := uint64(12345)
	for i := 0; i < steps; i++ {
		for j := range action {
			// xorshift64: deterministic, no extra imports needed
			rngState ^= rngState << 13
			rngState ^= rngState >> 7
			rngState ^= rngState << 17
			action[j] = 2*(float64(rngState%1_000_000)/1_000_000) - 1
		}
		_, _, terminated, truncated, _ := ep.Step(action)
		if terminated {
			return "", fmt.Errorf("episode reported terminated at step %d: the design space has no absorbing state", i)
		}
		if truncated && i != steps-1 {
			return "", fmt.Errorf("episode truncated at step %d of %d", i, steps)
		}
	}
	recs, err := reg.Records()
	if err != nil {
		return "", err
	}
	if len(recs) != steps+1 {
		return "", fmt.Errorf("episode wrote %d records, want %d", len(recs), steps+1)
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].ParentDesign != recs[i-1].DesignID {
			return "", fmt.Errorf("record %d parent %q != previous design %q (lineage must be a chain)",
				i, recs[i].ParentDesign, recs[i-1].DesignID)
		}
	}
	if recs[0].ParentDesign != "" {
		return "", fmt.Errorf("episode root has parent %q, want none", recs[0].ParentDesign)
	}
	problems, err := reg.Check()
	if err != nil {
		return "", err
	}
	if len(problems) != 0 {
		return "", fmt.Errorf("episode registry problems: %v", problems)
	}
	return fmt.Sprintf("action_dim=%d obs_dim=%d; %d-step episode wrote a connected lineage chain",
		wantAction, wantObs, steps), nil
}

// nullScorer satisfies runner.Scorer for structural checks that never evaluate.
type nullScorer struct{}

func (nullScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	return objective.EvalResult{Design: x}
}

// ---------------------------------------------------------------------------
// xcheck — the export the Python oracle recomputes independently
// ---------------------------------------------------------------------------

type xcheckSample struct {
	// Field order matches testdata/golden_field_samples.json (alphabetical).
	BMag       []float64 `json:"b_mag"`
	Br         []float64 `json:"br"`
	Bz         []float64 `json:"bz"`
	Design     []float64 `json:"design"`
	DesignName string    `json:"design_name"`
	PointsR    []float64 `json:"points_r"`
	PointsZ    []float64 `json:"points_z"`
	Solver     string    `json:"solver"`
}

type xcheckFile struct {
	Samples []xcheckSample `json:"samples"`
}

// xcheckPoints mirrors the golden sample layout: on-axis points (the r = 0
// branch of the closed form), near-axis points, stratified interior points and
// far-field probes well outside the coil box.
func xcheckPoints() (r, z []float64) {
	r = []float64{0.0, 0.0, 0.0, 0.0, 0.02, 0.05, 0.10, 0.15}
	z = []float64{0.0, 0.25, 0.75, 1.35, -0.05, 0.0, 0.05, 0.10}
	// 20 deterministic interior points
	state := uint64(0x5EEDF00D)
	next := func() float64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return float64(state%1_000_000) / 1_000_000
	}
	for i := 0; i < 20; i++ {
		r = append(r, 1.25*next())
		z = append(z, -1.5+3.0*next())
	}
	// far-field probes
	r = append(r, 0.79, 0.81, 1.19, 1.21)
	z = append(z, 0.0, 0.3, -0.6, 0.9)
	return r, z
}

// designInBox reports whether a design vector starts inside the search box (a
// design outside it would be clipped by VectorToCoils, i.e. evaluated as a
// different machine).
func designInBox(design, lo, hi []float64) bool {
	for i := range design {
		if design[i] < lo[i] || design[i] > hi[i] {
			return false
		}
	}
	return true
}

// probe is one design plus the sample points it was evaluated at.
type probe struct {
	name     string
	design   []float64
	ptsR     []float64
	ptsZ     []float64
	inBox    bool // whether the design came in as-is (vs clipped to the box)
	fromGold bool
}

func cmdXcheck(args []string) int {
	fs := newFlagSet("xcheck", "xcheck [--out FILE] [--set probes|golden] [--designs N] [--points]",
		"Export Br, Bz and |B| for several designs × sample points in exactly the JSON\n"+
			"schema of testdata/golden_field_samples.json, so that python/aux/oracle.py can\n"+
			"recompute them independently (acceptance gate G5 input).\n\n"+
			"--set probes (default): the frozen golden baseline design, a deterministic in-box\n"+
			"  perturbation, and the box centre, all on the same 32-point layout.\n"+
			"--set golden: the exact designs AND points of testdata/golden_field_samples.json,\n"+
			"  which the oracle already has — so Go, the oracle and the frozen file can be put\n"+
			"  side by side on identical inputs.")
	out := fs.String("out", defXcheckOut, "output JSON file")
	set := fs.String("set", "probes", "which designs to export: probes | golden")
	nDesigns := fs.Int("designs", 3, "number of probe designs (--set probes only), 1..3")
	dumpPoints := fs.Bool("points", false, "also print the sample point list")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	spec := defaultSpec()
	lo, hi := spec.Lower(), spec.Upper()
	var probes []probe

	switch *set {
	case "probes":
		goldPath, err := findFile(goldenBaseline)
		if err != nil {
			return fail("%v", err)
		}
		var gold goldenBaselineFile
		if err := readJSON(goldPath, &gold); err != nil {
			return fail("read %s: %v", goldPath, err)
		}
		if len(gold.Design) != spec.NParams() {
			return fail("golden design has %d parameters, want %d", len(gold.Design), spec.NParams())
		}
		n := *nDesigns
		if n < 1 || n > 3 {
			return fail("--designs must be 1..3, got %d", n)
		}
		ptsR, ptsZ := xcheckPoints()
		add := func(name string, design []float64) {
			inBox := true
			for i := range design {
				if design[i] < lo[i] {
					design[i], inBox = lo[i], false
				}
				if design[i] > hi[i] {
					design[i], inBox = hi[i], false
				}
			}
			probes = append(probes, probe{name: name, design: design, ptsR: ptsR, ptsZ: ptsZ, inBox: inBox})
		}

		add("golden:"+gold.Name, append([]float64(nil), gold.Design...))
		if n >= 2 {
			pert := append([]float64(nil), gold.Design...)
			k := spec.NCoils
			for i := 0; i < k; i++ { // radii +5%
				pert[i] *= 1.05
			}
			for i := 0; i < k; i++ { // z compressed by 5%
				pert[k+i] *= 0.95
			}
			for i := 0; i < k; i++ { // currents +8%
				pert[2*k+i] *= 1.08
			}
			add("golden_perturbed_r+5%_z-5%_I+8%", pert)
		}
		if n >= 3 {
			centre := make([]float64, spec.NParams())
			for i := range centre {
				centre[i] = 0.5 * (lo[i] + hi[i])
			}
			add("box_centre", centre)
		}
	case "golden":
		goldPath, err := findFile(goldenSamples)
		if err != nil {
			return fail("%v", err)
		}
		var gold goldenSamplesFile
		if err := readJSON(goldPath, &gold); err != nil {
			return fail("read %s: %v", goldPath, err)
		}
		if len(gold.Samples) == 0 {
			return fail("%s contains no samples", goldPath)
		}
		for _, s := range gold.Samples {
			if len(s.Design) != spec.NParams() {
				return fail("golden sample %q has %d parameters, want %d",
					s.DesignName, len(s.Design), spec.NParams())
			}
			if len(s.PointsR) != len(s.PointsZ) {
				return fail("golden sample %q: %d r-points vs %d z-points",
					s.DesignName, len(s.PointsR), len(s.PointsZ))
			}
			probes = append(probes, probe{
				name: s.DesignName, design: append([]float64(nil), s.Design...),
				ptsR: s.PointsR, ptsZ: s.PointsZ,
				inBox: designInBox(s.Design, lo, hi), fromGold: true,
			})
		}
	default:
		return fail("--set must be probes or golden, got %q", *set)
	}

	solver := physics.AnalyticSolver{}
	samples := make([]xcheckSample, 0, len(probes))
	totalPoints := 0
	for i, p := range probes {
		coils, err := physics.VectorToCoils(p.design, spec)
		if err != nil {
			return fail("design %d (%s): VectorToCoils: %v", i+1, p.name, err)
		}
		br, bz := physics.CoilsetField(coils, p.ptsR, p.ptsZ)
		mag := solver.Magnitude(coils, p.ptsR, p.ptsZ)
		if len(br) != len(p.ptsR) || len(bz) != len(p.ptsR) || len(mag) != len(p.ptsR) {
			return fail("design %d: solver returned %d/%d/%d values for %d points",
				i+1, len(br), len(bz), len(mag), len(p.ptsR))
		}
		samples = append(samples, xcheckSample{
			Design: p.design, DesignName: p.name, Solver: solverName,
			PointsR: p.ptsR, PointsZ: p.ptsZ, Br: br, Bz: bz, BMag: mag,
		})
		totalPoints += len(p.ptsR)
	}

	if err := writeJSON(*out, xcheckFile{Samples: samples}); err != nil {
		return fail("write %s: %v", *out, err)
	}
	note("xcheck: set=%s, wrote %d designs (%d point evaluations) to %s",
		*set, len(samples), totalPoints, *out)
	note("  schema: %s", "identical key set to testdata/golden_field_samples.json")
	note("  %-4s %-40s %-8s %-16s %-16s %s", "#", "design", "points", "max|B|", "min|B|", "note")
	for i, d := range samples {
		mn, mx := math.Inf(1), math.Inf(-1)
		for _, v := range d.BMag {
			mn = math.Min(mn, v)
			mx = math.Max(mx, v)
		}
		tag := ""
		switch {
		case !probes[i].inBox:
			// VectorToCoils clips to the box: say so, because then the exported
			// design is not the design as written.
			tag = "OUT OF BOX — evaluated as the clipped design"
		case probes[i].fromGold:
			tag = "golden design + golden points (direct 3-way comparison)"
		}
		note("  %-4d %-40s %-8d %-16g %-16g %s", i+1, d.DesignName, len(d.PointsR), mx, mn, tag)
	}
	note("  solver field: %q", solverName)
	if *dumpPoints {
		note("  points of design 1 (%d):", len(samples[0].PointsR))
		for i := range samples[0].PointsR {
			note("    r=%-12v z=%-12v", samples[0].PointsR[i], samples[0].PointsZ[i])
		}
	}
	note("  the Python oracle recomputes Br/Bz/|B| for these designs and points from scratch (G5).")
	return 0
}

// ---------------------------------------------------------------------------
// run / benchmark
// ---------------------------------------------------------------------------

func cmdRun(args []string) int {
	fs := newFlagSet("run", "run --method M --seed S --budget N [--registry PATH] [--tag T] [--workers N] [--out FILE]",
		"Run one search algorithm through the registry at an equal budget and print the\n"+
			"result: best score, best design, terms and convergence. Reproducibility gate G7:\n"+
			"the same method/seed/budget must print an identical best_score.")
	method := fs.String("method", "evolution", "search algorithm: random | lhs | evolution | evolution_warm")
	seed := fs.Int("seed", 0, "random seed")
	budget := fs.Int("budget", 1000, "evaluation budget")
	regPath := fs.String("registry", defRunRegistry, "registry JSONL to append to")
	tag := fs.String("tag", defRunTag, "run tag written into every record")
	workers := fs.Int("workers", 1, "evaluation workers (>1 evaluates a generation concurrently)")
	out := fs.String("out", "", "optional path to write the search.Result as JSON")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	if *budget <= 0 {
		return fail("--budget must be positive, got %d", *budget)
	}
	spec := defaultSpec()

	base, err := baselineDesign()
	if err != nil {
		return fail("%v", err)
	}
	ev := analyticEvaluator(spec, base.Cost)
	baseScore := ev.Evaluate(base.Design).Score

	reg, err := openRegistry(*regPath)
	if err != nil {
		return fail("%v", err)
	}

	opt := runOptions(spec, *method, *seed, *budget, *workers, baseScore, base.Design)

	rn := runner.New(reg, ev, *tag)
	before := reg.Len()
	start := time.Now()
	res, err := search.Run(*method, rn, opt)
	if err != nil {
		return fail("search.Run(%s): %v", *method, err)
	}
	elapsed := time.Since(start)

	note("run: method=%s seed=%d budget=%d workers=%d", res.Algorithm, res.Seed, res.Budget, *workers)
	note("registry: %s (%d records before, %d after this run)", *regPath, before, reg.Len())
	note("human baseline score: %v", baseScore)
	note("elapsed: %.2fs", elapsed.Seconds())
	note("evals: %d (budget %d)", res.NEvals, *budget)
	note("best_design_id=%s", res.BestDesignID)
	note("best_feasible=%v", res.BestFeasible)
	note("evals_to_beat=%d", res.EvalsToBeat)
	// The machine-readable line: 'forge run' twice with the same seed must print
	// the same string (G7).
	note("best_score=%v", res.BestScore)
	note("delta_vs_human=%v", res.BestScore-baseScore)
	if len(res.BestTerms) > 0 {
		note("best_terms:")
		for _, k := range sortedKeys(res.BestTerms) {
			note("  %-24s %v", k, res.BestTerms[k])
		}
	}
	printDesign(res.BestDesign)

	if *out != "" {
		if err := writeJSON(*out, res); err != nil {
			return fail("write %s: %v", *out, err)
		}
		note("result JSON: %s", *out)
	}
	return 0
}

// runOptions builds the search options for one run.
//
// The human baseline design is injected as WarmStart ONLY for evolution_warm.
// The reason to run both evolution and evolution_warm is to measure what
// inherited design knowledge buys; injecting the baseline into the cold variant
// as well would silently delete that comparison and make the two methods
// identical (stage E's harness applies the same rule).
func runOptions(spec config.Spec, method string, seed, budget, workers int,
	baselineScore float64, warmDesign []float64) search.Options {
	opt := search.DefaultOptions(spec)
	opt.Algorithm = method
	opt.Seed = seed
	opt.Budget = budget
	opt.Workers = workers
	opt.BaselineScore = baselineScore
	if method == search.AlgorithmEvolutionWarm {
		opt.WarmStart = append([]float64(nil), warmDesign...)
	}
	return opt
}

func cmdBenchmark(args []string) int {
	fs := newFlagSet("benchmark", "benchmark [--budget N] [--seeds 0,1,2] [--methods random,lhs,evolution,evolution_warm] [--out DIR] [--tag T] [--workers N]",
		"Run every method at every seed with an identical budget, aggregate across seeds\n"+
			"(never a single-seed claim), probe generalisation under perturbed requirements\n"+
			"and write runs/<tag>/results.json plus the registry it came from.")
	budget := fs.Int("budget", 1000, "evaluation budget per (method, seed)")
	seeds := fs.String("seeds", "0,1,2", "comma-separated seeds")
	methods := fs.String("methods", strings.Join(allMethods, ","), "comma-separated methods")
	out := fs.String("out", defRunDir, "run directory: registry.jsonl and results.json land here")
	tag := fs.String("tag", "", "run tag (default: the base name of --out)")
	workers := fs.Int("workers", 1, "evaluation workers")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	if *budget <= 0 {
		return fail("--budget must be positive, got %d", *budget)
	}
	seedList, err := splitInts(*seeds)
	if err != nil {
		return fail("--seeds: %v", err)
	}
	if len(seedList) == 0 {
		return fail("--seeds must list at least one seed")
	}
	methodList := splitList(*methods)
	if len(methodList) == 0 {
		return fail("--methods must list at least one method")
	}
	for _, m := range methodList {
		if _, ok := search.Methods[m]; !ok {
			return fail("unknown method %q (known: %s)", m, strings.Join(searchMethodNames(), ", "))
		}
	}
	runTag := *tag
	if runTag == "" {
		runTag = filepath.Base(*out)
		if runTag == "." || runTag == string(filepath.Separator) || runTag == "" {
			runTag = defTag
		}
	}

	spec := defaultSpec()
	base, err := baselineDesign()
	if err != nil {
		return fail("%v", err)
	}
	ev := analyticEvaluator(spec, base.Cost)
	baseScore := ev.Evaluate(base.Design).Score

	regPath := filepath.Join(*out, "registry.jsonl")
	reg, err := openRegistry(regPath)
	if err != nil {
		return fail("%v", err)
	}

	note("benchmark: tag=%s budget=%d seeds=%v methods=%v workers=%d", runTag, *budget, seedList, methodList, *workers)
	note("human baseline score: %v", baseScore)
	note("registry: %s", regPath)
	start := time.Now()
	rep, err := experiment.RunBenchmark(reg, spec, experiment.Opts{
		Budget:   *budget,
		Seeds:    seedList,
		Methods:  methodList,
		Tag:      runTag,
		Workers:  *workers,
		Baseline: &base,
		Progress: func(method string, seed int, res search.Result, seconds float64) {
			note("  %-16s seed=%-4d evals=%-6d best_score=%-24v %.1fs",
				method, seed, res.NEvals, res.BestScore, seconds)
		},
	})
	if err != nil {
		return fail("RunBenchmark: %v", err)
	}

	resultsPath := filepath.Join(*out, "results.json")
	if err := experiment.WriteJSON(rep, resultsPath); err != nil {
		return fail("write %s: %v", resultsPath, err)
	}

	note("")
	note("%-16s %-6s %-24s %-12s %-8s %-8s %-8s %-16s", "method", "seeds", "best_mean", "best_std", "n_beat", "frac", "to_beat", "budget")
	for _, m := range sortedKeys(rep.Aggregate) {
		a := rep.Aggregate[m]
		note("%-16s %-6d %-24v %-12g %-8d %-8.3f %-8.0f %-16d",
			m, a.NSeeds, a.BestMean, a.BestStd, a.NBeatingBaseline, a.FracBeatingBaseline,
			a.EvalsToBeatMean, a.Budget)
	}
	note("")
	note("baseline: %s score=%v feasible=%v design_id=%s",
		rep.Baseline.Name, rep.Baseline.Score, rep.Baseline.Feasible, rep.Baseline.DesignID)
	if rep.Best != nil {
		note("best: %s score=%v algorithm=%s seed=%d feasible=%v",
			rep.Best.DesignID, rep.Best.Score, rep.Best.Algorithm, rep.Best.Seed, rep.Best.Feasible)
		note("delta_vs_human=%v", rep.Best.Score-rep.Baseline.Score)
	} else {
		note("best: (none reported)")
	}
	note("registry summary: %d records, %d feasible, best=%v (%s, %s)",
		rep.RegistrySummary.NRecords, rep.RegistrySummary.NFeasible, rep.RegistrySummary.BestScore,
		rep.RegistrySummary.BestDesignID, rep.RegistrySummary.BestAlgorithm)
	note("elapsed: %.2fs", time.Since(start).Seconds())
	note("results JSON: %s", resultsPath)
	note("next: forge rules --registry %s --out %s", regPath, defRulesOut)
	note("next: forge report --results %s --out %s", resultsPath, filepath.Join(*out, "report.md"))
	return 0
}

func searchMethodNames() []string {
	names := make([]string, 0, len(search.Methods))
	for k := range search.Methods {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// methodListOrNone names the registered algorithms, or says so when stage D has
// not populated search.Methods yet (an honest "not there" beats an empty line).
func methodListOrNone() string {
	names := searchMethodNames()
	if len(names) == 0 {
		return "(none registered yet: search.Methods is empty)"
	}
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// rules / report / registry / version
// ---------------------------------------------------------------------------

func cmdRules(args []string) int {
	fs := newFlagSet("rules", "rules --registry PATH --out PATH [--min-n N] [--min-abs-rho R] [--top-k K]",
		"Mine replicated design rules from a registry: a rule survives only if the\n"+
			"Spearman sign of (parameter, score term) is identical in every run, and it is\n"+
			"reported with its decile contrast and its scope.")
	regPath := fs.String("registry", filepath.Join(defRunDir, "registry.jsonl"), "registry JSONL to mine")
	out := fs.String("out", defRulesOut, "markdown knowledge base to write")
	minN := fs.Int("min-n", 150, "minimum designs per run for a run to count")
	minRho := fs.Float64("min-abs-rho", 0.20, "minimum worst-case |rho| for a rule to be kept")
	topK := fs.Int("top-k", 12, "maximum number of rules to keep")
	tag := fs.String("tag", "", "tag recorded in the knowledge base (default: the registry's directory)")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	spec := defaultSpec()

	reg, err := openExistingRegistry(*regPath)
	if err != nil {
		return fail("%v", err)
	}
	recs, err := reg.Records()
	if err != nil {
		return fail("registry.Records: %v", err)
	}
	if len(recs) == 0 {
		return fail("registry %s holds no records", *regPath)
	}
	runTag := *tag
	if runTag == "" {
		runTag = filepath.Base(filepath.Dir(*regPath))
	}

	rules := knowledge.MineRules(recs, spec, knowledge.MineOpts{
		MinN: *minN, MinAbsRho: *minRho, TopK: *topK,
	})
	if err := knowledge.WriteRulesMD(rules, *out, spec, len(recs), runTag); err != nil {
		return fail("knowledge.WriteRulesMD: %v", err)
	}

	note("rules: %d mined from %d records (%s)", len(rules), len(recs), *regPath)
	note("thresholds: min_n=%d min_abs_rho=%v top_k=%d", *minN, *minRho, *topK)
	if len(rules) > 0 {
		note("%-8s %-10s %-10s %-10s %-10s %-8s %-8s", "rule", "parameter", "term", "rho", "agree", "n_design", "n_runs")
		for _, r := range rules {
			note("%-8s %-10s %-10s %-10.4f %-10.3f %-8d %-8d",
				r.RuleID, r.Parameter, r.Term, r.Rho, r.SignAgreement, r.NDesigns, r.NRuns)
		}
	} else {
		note("no rule passed the replication test — that is a result, not a failure: report it as such.")
	}
	note("knowledge base: %s", *out)
	return 0
}

func cmdReport(args []string) int {
	fs := newFlagSet("report", "report --results PATH --out PATH [--rules PATH]",
		"Render the markdown run report (Chinese, English technical terms kept) including\n"+
			"the mandatory 诚实边界 honest-limits section.")
	results := fs.String("results", filepath.Join(defRunDir, "results.json"), "experiment report JSON to render")
	out := fs.String("out", defReportOut, "markdown report to write")
	rulesPath := fs.String("rules", "", "rules markdown to reference and load (default: knowledge/design_rules.md if present)")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	rep, err := experiment.LoadReport(*results)
	if err != nil {
		return fail("experiment.LoadReport(%s): %v", *results, err)
	}

	path := *rulesPath
	if path == "" {
		if _, statErr := os.Stat(defRulesOut); statErr == nil {
			path = defRulesOut
		}
	}
	var rules []knowledge.Rule
	if path != "" {
		rules, err = knowledge.LoadRules(path)
		if err != nil {
			return fail("knowledge.LoadRules(%s): %v", path, err)
		}
	}

	md := report.RenderMarkdown(rep, rules, path)
	if err := report.Write(*out, md); err != nil {
		return fail("report.Write(%s): %v", *out, err)
	}
	note("report: %s (%d bytes)", *out, len(md))
	note("rules referenced: %s (%d rules)", pathOrNone(path), len(rules))
	note("baseline score: %v", rep.Baseline.Score)
	if rep.Best != nil {
		note("best score:     %v (delta %v)", rep.Best.Score, rep.Best.Score-rep.Baseline.Score)
	}
	return 0
}

func pathOrNone(p string) string {
	if p == "" {
		return "(none)"
	}
	return p
}

func cmdRegistry(args []string) int {
	fs := newFlagSet("registry", "registry [--registry PATH] [--check=true]",
		"Inspect a registry: record counts, per-algorithm counts, best design, and (with\n"+
			"--check) the integrity gate: ids sequential from 1, required fields present,\n"+
			"every parent_design referencing an existing design. Problems exit non-zero.")
	regPath := fs.String("registry", filepath.Join(defRunDir, "registry.jsonl"), "registry JSONL to inspect")
	check := fs.Bool("check", true, "run the integrity gate and exit non-zero on problems")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	reg, err := openExistingRegistry(*regPath)
	if err != nil {
		return fail("%v", err)
	}
	sum := reg.Summary()
	note("registry: %s", *regPath)
	note("records:  %d", sum.NRecords)
	note("feasible: %d", sum.NFeasible)
	note("best:     %v (%s, %s)", sum.BestScore, sum.BestDesignID, sum.BestAlgorithm)
	if len(sum.PerAlgo) > 0 {
		note("per algorithm:")
		for _, a := range sortedKeys(sum.PerAlgo) {
			note("  %-16s %d", a, sum.PerAlgo[a])
		}
	}
	if branches := reg.BranchImprovement(); len(branches) > 0 {
		top := branches
		if len(top) > 5 {
			top = top[:5]
		}
		note("top branch improvements:")
		note("  %-10s %-8s %-24s %-24s %-12s", "design", "children", "parent_score", "best_child", "gain")
		for _, b := range top {
			note("  %-10s %-8d %-24v %-24v %-12v", b.DesignID, b.NChildren, b.ParentScore, b.BestChildScore, b.Gain)
		}
	}
	if !*check {
		return 0
	}
	problems, err := reg.Check()
	if err != nil {
		return fail("registry.Check: %v", err)
	}
	if len(problems) > 0 {
		note("")
		note("registry --check: FAIL (%d problem(s))", len(problems))
		for _, p := range problems {
			note("  - %s", p)
		}
		return 1
	}
	note("")
	note("registry --check: PASS (ids sequential from 1, required fields present, all parents resolved)")
	return 0
}

func cmdVersion(args []string) int {
	fs := newFlagSet("version", "version", "Print version, build and frozen-schema facts.")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	spec := defaultSpec()
	env := rlenv.NewEnv(nullScorer{}, spec, 0, 0)
	note("forge %s", Version)
	note("  module      github.com/logos-42/hushfusion-forge")
	note("  go          %s (%s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	commit := resolveCommit()
	note("  commit      %s", commit)
	if commit == "unknown" {
		note("              (set FORGE_GIT_COMMIT or build with -ldflags \"-X main.buildCommit=<sha>\")")
	}
	note("  spec        n_coils=%d n_params=%d b_ref=%v mirror_ref=%v (internal/config DefaultSpec)",
		spec.NCoils, spec.NParams(), spec.BRef, spec.MirrorRef)
	note("  rlenv       action_dim=%d observation_dim=%d (design + %d metric slots)",
		env.ActionDim(), env.ObservationDim(), len(rlenv.ObsMetricKeys))
	note("  rlenv       learned policy: none — LoadPolicy returns ErrNoLearnedPolicy (Phase 1)")
	note("  registry    schema v0.1, %d required fields: %s",
		len(registry.RequiredFields), strings.Join(registry.RequiredFields, ", "))
	note("  methods     %s", methodListOrNone())
	note("  golden      score anchor %v, field samples vs Python oracle rel < %v", -0.2905708160753513, tolField)
	return 0
}
