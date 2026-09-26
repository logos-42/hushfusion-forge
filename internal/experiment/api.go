// Package experiment: equal-budget benchmark harness + the Phase-0 pipeline.
//
// FROZEN INTERFACE (v0.1) — owner: stage E.
//
// Three things are measured, and they answer different questions:
//
//	最优性能 (best-of-budget)  how good the best design gets at a fixed cost
//	收敛速度 (evals to beat)   how much design effort the machine needs before it
//	                          is already better than a competent engineer — the
//	                          *rate* of the learning loop, not its endpoint
//	泛化 (robustness probe)   each method's best design re-scored under perturbed
//	                          requirements, against the human baseline re-solved
//	                          for the same perturbation. A design that only wins
//	                          inside the exact box it was searched in has not
//	                          generalised, and saying so is part of the result.
//
// The Report JSON schema is consumed by the Python auxiliary layer
// (python/aux/analyze.py); keys are FROZEN.
package experiment

import (
	"encoding/json"
	"errors"
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
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/registry"
	"github.com/logos-42/hushfusion-forge/internal/runner"
	"github.com/logos-42/hushfusion-forge/internal/search"
)

// Variant is a requirement perturbation: a name plus spec overrides.
type Variant struct {
	Name     string             `json:"name"`
	Override map[string]float64 `json:"override"`
}

// SpecVariants are the six perturbations used by the generalisation probe.
var SpecVariants = []Variant{
	{Name: "b_ref=0.8T", Override: map[string]float64{"b_ref": 0.8}},
	{Name: "b_ref=1.2T", Override: map[string]float64{"b_ref": 1.2}},
	{Name: "z_cell=0.60m", Override: map[string]float64{"z_cell": 0.60}},
	{Name: "z_cell=1.00m", Override: map[string]float64{"z_cell": 1.00}},
	{Name: "r_plasma=0.12m", Override: map[string]float64{"r_plasma": 0.12}},
	{Name: "r_plasma=0.18m", Override: map[string]float64{"r_plasma": 0.18}},
}

// variantKeys are the spec keys a Variant may override. Adding one is a contract
// change (the Python auxiliary layer mirrors the list).
var variantKeys = []string{"b_ref", "z_cell", "r_plasma"}

// ApplyVariant returns the spec with the variant's overrides applied.
// Supported keys: b_ref, z_cell, r_plasma. Unknown keys must be reported as an
// error rather than silently ignored.
//
// The override set is validated as a whole BEFORE anything is written, so an
// error guarantees the returned spec is byte-for-byte the input spec: a caller
// that ignores the error still cannot get a half-perturbed device.
func ApplyVariant(spec config.Spec, v Variant) (config.Spec, error) {
	keys := make([]string, 0, len(v.Override))
	for k := range v.Override {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error text
	for _, k := range keys {
		if !isVariantKey(k) {
			return spec, fmt.Errorf("experiment: variant %q overrides unknown spec key %q (supported: %s)",
				v.Name, k, strings.Join(variantKeys, ", "))
		}
	}
	out := spec
	for _, k := range keys {
		switch k {
		case "b_ref":
			out.BRef = v.Override[k]
		case "z_cell":
			out.ZCell = v.Override[k]
		case "r_plasma":
			out.RPlasma = v.Override[k]
		}
	}
	return out, nil
}

func isVariantKey(k string) bool {
	for _, known := range variantKeys {
		if k == known {
			return true
		}
	}
	return false
}

// Meta is the run provenance written into the report.
type Meta struct {
	Tag       string   `json:"tag"`
	Timestamp string   `json:"timestamp"`
	Budget    int      `json:"budget"`
	Seeds     []int    `json:"seeds"`
	Methods   []string `json:"methods"`
	GitCommit string   `json:"git_commit"`
	Platform  string   `json:"platform"`
	GoVersion string   `json:"go_version"`
	Workers   int      `json:"workers"`
}

// Agg is per-method statistics across seeds. Never a single-seed claim.
type Agg struct {
	NSeeds              int     `json:"n_seeds"`
	Budget              int     `json:"budget"`
	BestMean            float64 `json:"best_mean"`
	BestStd             float64 `json:"best_std"`
	BestMin             float64 `json:"best_min"`
	BestMax             float64 `json:"best_max"`
	NBeatingBaseline    int     `json:"n_beating_baseline"`
	FracBeatingBaseline float64 `json:"frac_beating_baseline"`
	EvalsToBeatMean     float64 `json:"evals_to_beat_mean"`   // -1 when no seed beat it
	EvalsToBeatMedian   float64 `json:"evals_to_beat_median"` // -1 when no seed beat it
	BaselineScore       float64 `json:"baseline_score"`
}

// BaseRec is the human baseline as recorded.
type BaseRec struct {
	Name      string             `json:"name"`
	Note      string             `json:"note"`
	Score     float64            `json:"score"`
	Feasible  bool               `json:"feasible"`
	Terms     map[string]float64 `json:"terms"`
	Weighted  map[string]float64 `json:"weighted"`
	Penalties map[string]float64 `json:"penalties"`
	Metrics   physics.Metrics    `json:"metrics"`
	Design    []float64          `json:"design"`
	CostProxy float64            `json:"cost_proxy"`
	DesignID  string             `json:"design_id"`
}

// BestRec is the single best design found by the machine.
type BestRec struct {
	DesignID  string             `json:"design_id"`
	Algorithm string             `json:"algorithm"`
	Seed      int                `json:"seed"`
	Score     float64            `json:"score"`
	Terms     map[string]float64 `json:"terms"`
	Metrics   physics.Metrics    `json:"metrics"`
	Design    []float64          `json:"design"`
	Feasible  bool               `json:"feasible"`
}

// Robustness is the generalisation probe result.
type Robustness struct {
	Variants           []string                      `json:"variants"`
	BaselinePerVariant map[string]float64            `json:"baseline_score_per_variant"`
	PerDesign          map[string]map[string]float64 `json:"per_design"`
	Summary            map[string]RobustSummary      `json:"summary"`
}

// RobustSummary is the per-design roll-up of the probe.
type RobustSummary struct {
	MeanDeltaVsBaseline  float64 `json:"mean_delta_vs_baseline"`
	WorstDeltaVsBaseline float64 `json:"worst_delta_vs_baseline"`
	NVariantsWinning     int     `json:"n_variants_winning"`
	NVariants            int     `json:"n_variants"`
}

// Report is the full artifact written to runs/<tag>/results.json.
type Report struct {
	Meta            Meta                 `json:"meta"`
	Spec            map[string]any       `json:"spec"`
	Solver          string               `json:"solver"`
	CostRef         float64              `json:"cost_ref"`
	Baseline        BaseRec              `json:"baseline"`
	Runs            []search.Result      `json:"runs"`
	History         map[string][]float64 `json:"history"`
	Aggregate       map[string]Agg       `json:"aggregate"`
	Robustness      Robustness           `json:"robustness"`
	Best            *BestRec             `json:"best"`
	RegistrySummary registry.Summary     `json:"registry_summary"`
	RLEnvReference  map[string]any       `json:"rl_env_reference,omitempty"`
}

// Opts configures a benchmark run.
type Opts struct {
	Budget   int
	Seeds    []int
	Methods  []string
	Tag      string
	Workers  int
	Baseline *baseline.Baseline
	Progress func(method string, seed int, res search.Result, seconds float64)
}

const (
	// defaultBudget matches search.DefaultOptions and the report's stated
	// setting (budget 1000 evaluations per run).
	defaultBudget = 1000
	// humanBaselineAlgorithm is the algorithm name the human baseline is
	// recorded under, so "which branch improved on the human?" is answerable.
	humanBaselineAlgorithm = "human_baseline"
	// warmStartMethod is the one method that consumes Opts.WarmStart; the
	// benchmark hands it the human design vector (knowledge reuse).
	warmStartMethod = "evolution_warm"
)

// defaultMethods / defaultSeeds are used when Opts leaves them empty. Three
// seeds is the minimum that makes a spread meaningful; the report refuses
// single-seed claims.
var (
	defaultMethods = []string{"random", "lhs", "evolution", "evolution_warm"}
	defaultSeeds   = []int{0, 1, 2}
)

// RunBenchmark records the human baseline, then runs every method at every seed
// with an identical budget, then aggregates and probes generalisation.
//
// The baseline is recorded as its own algorithm ("human_baseline") and becomes
// design D0001 — the root of the lineage tree, so "which branch improved on the
// human?" is answerable.
//
// Implementation notes (stage E):
//
//   - the registry must be empty: the D0001-root claim is an assertion about the
//     artifact, and it is only true when the baseline is the first record
//     appended. A non-empty registry is refused loudly instead of producing a
//     report whose lineage claims are false;
//   - the baseline is scored through the SAME Evaluator as every machine design
//     (cost_ref = baseline ohmic cost), so the comparison is apples-to-apples;
//   - runs are executed sequentially (opt.Workers only parallelises inside one
//     run) — the wall-clock is dominated by the physics either way, and a
//     deterministic order keeps runs/ comparable across invocations.
func RunBenchmark(reg *registry.Registry, spec config.Spec, opt Opts) (*Report, error) {
	if reg == nil {
		return nil, errors.New("experiment: RunBenchmark needs an open registry")
	}
	if n := reg.Len(); n != 0 {
		return nil, fmt.Errorf("experiment: registry already holds %d records; the benchmark records the human baseline as the lineage root D0001 and needs an empty registry (use a fresh path)", n)
	}

	methods := append([]string(nil), opt.Methods...)
	if len(methods) == 0 {
		methods = append([]string(nil), defaultMethods...)
	}
	seeds := append([]int(nil), opt.Seeds...)
	if len(seeds) == 0 {
		seeds = append([]int(nil), defaultSeeds...)
	}
	budget := opt.Budget
	if budget <= 0 {
		budget = defaultBudget
	}
	workers := opt.Workers
	if workers < 1 {
		workers = 1
	}

	base := opt.Baseline
	if base == nil {
		b, err := baseline.TextbookMirror(spec)
		if err != nil {
			return nil, fmt.Errorf("experiment: solving the human baseline: %w", err)
		}
		base = &b
	}
	baseDesign, err := designVectorOf(*base, spec)
	if err != nil {
		return nil, err
	}

	solver, err := defaultSolver()
	if err != nil {
		return nil, err
	}
	grids := physics.BuildGrids(spec)
	ev := objective.NewEvaluator(spec, solver, base.Cost, grids)
	sc := runner.New(reg, ev, opt.Tag)

	bres := sc.Score(baseDesign, runner.Meta{
		Algorithm: humanBaselineAlgorithm,
		Seed:      0,
		EvalIndex: 0,
		Note:      "human baseline, re-scored by the same evaluator as every machine design",
	})
	baseName := base.Name
	if baseName == "" {
		baseName = humanBaselineAlgorithm
	}
	baseRec := BaseRec{
		Name:      baseName,
		Note:      base.Note,
		Score:     bres.Score,
		Feasible:  bres.Feasible,
		Terms:     bres.Terms,
		Weighted:  bres.Weighted,
		Penalties: bres.Penalties,
		Metrics:   bres.Metrics,
		Design:    bres.Design,
		CostProxy: bres.Metrics.CostProxy,
		DesignID:  bres.DesignID,
	}
	if len(baseRec.Design) == 0 {
		baseRec.Design = baseDesign
	}
	if baseRec.CostProxy == 0 {
		baseRec.CostProxy = base.Cost
	}

	rep := &Report{
		Meta: Meta{
			Tag:       opt.Tag,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Budget:    budget,
			Seeds:     seeds,
			Methods:   methods,
			GitCommit: gitCommit(),
			Platform:  runtime.GOOS + "/" + runtime.GOARCH,
			GoVersion: runtime.Version(),
			Workers:   workers,
		},
		Spec:      spec.AsMap(),
		Solver:    solver.Name(),
		CostRef:   base.Cost,
		Baseline:  baseRec,
		History:   map[string][]float64{},
		Aggregate: map[string]Agg{},
	}

	runs := make([]search.Result, 0, len(methods)*len(seeds))
	for _, m := range methods {
		for _, s := range seeds {
			o := search.DefaultOptions(spec)
			o.Spec = spec
			o.Seed = s
			o.Budget = budget
			o.Algorithm = m
			o.Workers = workers
			o.BaselineScore = baseRec.Score
			if m == warmStartMethod {
				o.WarmStart = append([]float64(nil), baseDesign...)
			}
			started := time.Now()
			res, err := search.Run(m, sc, o)
			if err != nil {
				return nil, fmt.Errorf("experiment: method %q seed %d: %w", m, s, err)
			}
			if opt.Progress != nil {
				opt.Progress(m, s, res, time.Since(started).Seconds())
			}
			runs = append(runs, res)
			rep.History[fmt.Sprintf("%s/seed=%d", m, s)] = res.History
		}
	}
	rep.Runs = runs
	rep.Aggregate = Aggregate(runs, baseRec.Score)

	// Each method's best design across its seeds, re-scored under the perturbed
	// requirements; the recorded human design rides along as the zero-ish
	// reference (it is re-scored by the variant objective, NOT re-solved).
	designs := map[string][]float64{humanBaselineAlgorithm: baseDesign}
	bestRun := map[string]search.Result{}
	for _, r := range runs {
		if b, ok := bestRun[r.Algorithm]; !ok || r.BestScore > b.BestScore {
			bestRun[r.Algorithm] = r
		}
	}
	for _, m := range methods {
		if b, ok := bestRun[m]; ok && len(b.BestDesign) > 0 {
			designs[m] = b.BestDesign
		}
	}
	rob, err := RobustnessProbe(spec, designs, SpecVariants)
	if err != nil {
		return nil, fmt.Errorf("experiment: robustness probe: %w", err)
	}
	rep.Robustness = rob

	if len(runs) > 0 {
		b := runs[0]
		for _, r := range runs[1:] {
			if r.BestScore > b.BestScore {
				b = r
			}
		}
		rep.Best = &BestRec{
			DesignID:  b.BestDesignID,
			Algorithm: b.Algorithm,
			Seed:      b.Seed,
			Score:     b.BestScore,
			Terms:     b.BestTerms,
			Metrics:   b.BestMetrics,
			Design:    b.BestDesign,
			Feasible:  b.BestFeasible,
		}
	}

	rep.RegistrySummary = reg.Summary()
	return rep, nil
}

// designVectorOf returns the canonical design vector of a human baseline: its
// recorded Design when it has one, otherwise the encoding of its coils.
func designVectorOf(b baseline.Baseline, spec config.Spec) ([]float64, error) {
	if len(b.Design) == spec.NParams() {
		return append([]float64(nil), b.Design...), nil
	}
	if len(b.Coils) > 0 {
		if x := physics.CoilsToVector(b.Coils); len(x) == spec.NParams() {
			return x, nil
		}
	}
	return nil, fmt.Errorf("experiment: human baseline %q decoded to %d parameters, spec wants %d",
		b.Name, len(b.Design), spec.NParams())
}

// defaultSolver resolves the stage-A analytic solver at RUN time, not at compile
// time.
//
// Why the indirection: this package is built in parallel with internal/physics.
// While stage A is still a skeleton, the direct form
//
//	var s physics.Solver = physics.AnalyticSolver{}
//
// does not compile ("does not implement physics.Solver"), which would take
// `go build ./...` (gate G1) red for the whole tree because of a *different*
// stage's progress. The assertion below is interface-to-interface (through
// `any`), so it compiles either way, and starts resolving the moment stage A
// lands the methods — no edit needed here. Until then the harness reports
// "not ready" instead of inventing numbers.
//
// Both receiver styles are tried, so the shim survives A implementing Magnitude
// on a value or on a pointer.
func defaultSolver() (physics.Solver, error) {
	for _, candidate := range []any{physics.AnalyticSolver{}, &physics.AnalyticSolver{}} {
		if s, ok := candidate.(physics.Solver); ok {
			return s, nil
		}
	}
	return nil, errors.New("experiment: physics.AnalyticSolver does not implement physics.Solver yet (stage A not landed)")
}

// Aggregate computes per-method statistics across seeds.
//
// Statistics are taken over distinct seeds, sorted by seed for reproducibility;
// a repeated seed is counted once (the last result for that seed wins) so a
// double-run cannot inflate n_seeds. Conventions, stated because the Python
// auxiliary layer recomputes them:
//
//	best_std                 sample standard deviation (ddof = 1); 0 for n < 2
//	best_min / best_max      over seeds
//	n_beating_baseline       seeds with best_score > baseline_score (strict)
//	evals_to_beat_*          mean/median over the seeds that DID beat the
//	                         baseline (best_score > baseline_score AND
//	                         evals_to_beat >= 0); -1 when none did
//	median                   mean of the two central values for even counts
func Aggregate(runs []search.Result, baselineScore float64) map[string]Agg {
	byMethod := map[string][]search.Result{}
	for _, r := range runs {
		byMethod[r.Algorithm] = append(byMethod[r.Algorithm], r)
	}
	out := make(map[string]Agg, len(byMethod))
	for method, rs := range byMethod {
		bySeed := map[int]search.Result{}
		seeds := make([]int, 0, len(rs))
		for _, r := range rs {
			if _, dup := bySeed[r.Seed]; !dup {
				seeds = append(seeds, r.Seed)
			}
			bySeed[r.Seed] = r
		}
		sort.Ints(seeds)

		agg := Agg{NSeeds: len(seeds), BaselineScore: baselineScore}
		scores := make([]float64, 0, len(seeds))
		evals := make([]float64, 0, len(seeds))
		for _, s := range seeds {
			r := bySeed[s]
			scores = append(scores, r.BestScore)
			if r.Budget > agg.Budget {
				agg.Budget = r.Budget
			}
			// The "did it beat the baseline" test is made ONCE, from the score:
			// a run that reports an evals_to_beat while scoring below the
			// baseline is inconsistent input, and letting it into the
			// convergence statistics would inflate the one number that says how
			// fast the loop learns.
			beats := r.BestScore > baselineScore
			if beats {
				agg.NBeatingBaseline++
			}
			if r.EvalsToBeat >= 0 && beats {
				evals = append(evals, float64(r.EvalsToBeat))
			}
		}
		agg.BestMean = mean(scores)
		agg.BestStd = stdSample(scores)
		agg.BestMin, agg.BestMax = minMax(scores)
		if agg.NSeeds > 0 {
			agg.FracBeatingBaseline = float64(agg.NBeatingBaseline) / float64(agg.NSeeds)
		}
		agg.EvalsToBeatMean, agg.EvalsToBeatMedian = -1, -1
		if len(evals) > 0 {
			agg.EvalsToBeatMean = mean(evals)
			agg.EvalsToBeatMedian = median(evals)
		}
		out[method] = agg
	}
	return out
}

// RobustnessProbe re-scores designs under perturbed requirements, relative to
// the human baseline re-solved for each variant (so the zero line is always
// "a human re-designing for the new requirement").
//
// Per design and variant the reported number is
//
//	per_design[design][variant] = score(design, variant) - score(human_variant)
//
// with score(human_variant) the human design RE-SOLVED for the perturbed spec
// (baseline.TextbookMirror under the variant, scored by the variant objective
// and the variant's own cost reference). A positive delta therefore means
// "still better than a human who redesigned for the new requirement".
//
// The recorded baseline design itself is scored under each variant too (pass it
// in under the "human_baseline" key); it was not redesigned for the perturbation,
// so its delta is normally negative and that contrast is the point.
func RobustnessProbe(spec config.Spec, designs map[string][]float64, variants []Variant) (Robustness, error) {
	out := Robustness{
		Variants:           []string{},
		BaselinePerVariant: map[string]float64{},
		PerDesign:          map[string]map[string]float64{},
		Summary:            map[string]RobustSummary{},
	}
	names := make([]string, 0, len(designs))
	for name := range designs {
		names = append(names, name)
	}
	sort.Strings(names)

	// Input validation first, and it is pure: a bad variant key or a
	// wrong-length design vector must fail before any physics is touched.
	vspecs := make([]config.Spec, len(variants))
	for i, v := range variants {
		s, err := ApplyVariant(spec, v)
		if err != nil {
			return out, err
		}
		vspecs[i] = s
	}
	for _, name := range names {
		if len(designs[name]) != spec.NParams() {
			return out, fmt.Errorf("design %q has %d parameters, spec wants %d", name, len(designs[name]), spec.NParams())
		}
	}

	solver, err := defaultSolver()
	if err != nil {
		return out, err
	}
	for i, v := range variants {
		vspec := vspecs[i]
		human, err := baseline.TextbookMirror(vspec)
		if err != nil {
			return out, fmt.Errorf("variant %q: re-solving the human baseline: %w", v.Name, err)
		}
		hx, err := designVectorOf(human, vspec)
		if err != nil {
			return out, fmt.Errorf("variant %q: %w", v.Name, err)
		}
		ev := objective.NewEvaluator(vspec, solver, human.Cost, physics.BuildGrids(vspec))
		humanScore := ev.Score(hx)

		out.Variants = append(out.Variants, v.Name)
		out.BaselinePerVariant[v.Name] = humanScore
		for _, name := range names {
			delta := ev.Score(designs[name]) - humanScore
			if out.PerDesign[name] == nil {
				out.PerDesign[name] = map[string]float64{}
			}
			out.PerDesign[name][v.Name] = delta
			sum := out.Summary[name]
			if sum.NVariants == 0 {
				sum.WorstDeltaVsBaseline = delta
			} else if delta < sum.WorstDeltaVsBaseline {
				sum.WorstDeltaVsBaseline = delta
			}
			sum.MeanDeltaVsBaseline += delta
			sum.NVariants++
			if delta > 0 {
				sum.NVariantsWinning++
			}
			out.Summary[name] = sum
		}
	}
	for name, sum := range out.Summary {
		if sum.NVariants > 0 {
			sum.MeanDeltaVsBaseline /= float64(sum.NVariants)
		}
		out.Summary[name] = sum
	}
	return out, nil
}

// WriteJSON writes the report as indented JSON. Parent directories are created
// so runs/<tag>/results.json can be written directly.
func WriteJSON(rep *Report, path string) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("experiment: marshal report: %w", err)
	}
	data = append(data, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("experiment: create report directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("experiment: write report %s: %w", path, err)
	}
	return nil
}

// LoadReport reads a report back.
func LoadReport(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("experiment: parse report %s: %w", path, err)
	}
	return &rep, nil
}

// --- statistics helpers -----------------------------------------------------

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// stdSample is the unbiased sample standard deviation (ddof = 1).
func stdSample(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	ss := 0.0
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

// median sorts a copy; even counts average the two central values.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return 0.5 * (cp[n/2-1] + cp[n/2])
}

func minMax(xs []float64) (lo, hi float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	lo, hi = xs[0], xs[0]
	for _, x := range xs[1:] {
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	return lo, hi
}

// --- provenance helpers -----------------------------------------------------

// gitCommit is a best-effort HEAD sha of the checkout this run happens in: it
// walks up from the working directory and resolves .git/HEAD (both a .git
// directory and the "gitdir: ..." file a worktree uses). It never shells out,
// and it returns "" rather than inventing a value when the run is not inside a
// git checkout — a fabricated commit hash in the provenance block would be
// worse than an empty one.
func gitCommit() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 64; i++ {
		git := filepath.Join(dir, ".git")
		if info, err := os.Stat(git); err == nil {
			gitDir := git
			if !info.IsDir() {
				body, err := os.ReadFile(git)
				if err != nil {
					return ""
				}
				rest, ok := strings.CutPrefix(strings.TrimSpace(string(body)), "gitdir:")
				if !ok {
					return ""
				}
				gitDir = strings.TrimSpace(rest)
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(dir, gitDir)
				}
			}
			head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
			if err != nil {
				return ""
			}
			return resolveGitHead(gitDir, strings.TrimSpace(string(head)))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

func resolveGitHead(gitDir, head string) string {
	ref, ok := strings.CutPrefix(head, "ref:")
	if !ok {
		return head // detached HEAD: HEAD is already the sha
	}
	ref = strings.TrimSpace(ref)
	if body, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
		return strings.TrimSpace(string(body))
	}
	if body, err := os.ReadFile(filepath.Join(gitDir, "packed-refs")); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
				continue
			}
			if f := strings.Fields(line); len(f) == 2 && f[1] == ref {
				return f[0]
			}
		}
	}
	return ""
}
