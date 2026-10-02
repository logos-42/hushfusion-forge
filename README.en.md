# HUSHFUSION Forge

[中文](README.md) | **English**

[![Wiki Lint](https://github.com/logos-42/hushfusion-forge/actions/workflows/wiki-lint.yml/badge.svg)](https://github.com/logos-42/hushfusion-forge/actions/workflows/wiki-lint.yml)
[![Phase 0](https://img.shields.io/badge/0.1.2-23%20gates-brightgreen)](scripts/verify.sh)

> Not a machine. An engineering system that keeps producing, validating and
> killing the designs of the *next* machine. Forge is its first version: a
> design → score → record → learn loop for electromagnetic coil configurations.

---

## Why a design loop, not a device

The fusion device is not what a one-person team without a lab should build first.
What compounds is the *design capability itself*:

```
parametric design → field physics → objective → search → design registry → design rules ↺
```

What one experiment really produces is not "a better coil" but **a design rule the
next generation can reuse**. That is the difference from prototype-by-prototype
work: the knowledge is written into the system instead of staying in a head.

Phase 0 has exactly one acceptance question:

> **Can the machine find a better electromagnetic design than a competent
> human-designed baseline — and can that claim be independently reproduced?**

**Phase 0, measured (12 001 records; 4 methods × 3 seeds × 1000 evaluations; 5.5 s of compute)**:
best machine design `score = 1.381419` (`evolution_warm`, seed 0) against a human baseline of
`−0.290571` — Δ = **+1.672**; all four methods beat the human on 3/3 seeds.

**0.1.2 turns the third caveat into a criterion.** The objective gains a fourth penalty term,
`clearance` (conductor surface to the central cell, required ≥ 0.05 m; definition in
[docs/version-0.1.2.md](docs/version-0.1.2.md) §1). Re-running the identical tuple gives
**Δ = +1.2395** (was +1.672) with **7221/12001 feasible**, and the 0.1.0 champion drops from
`1.3814186` to **−13.6099621** under the new criterion (one of its coils sits entirely inside the
constraint region). **That 0.4325 is this repo's first measurement of "what a physical constraint is
worth"** — and it also says plainly that the number is held up by the `MinClearance = 0.05`
*engineering judgement*: change the value and it is a different number. **It is not a physical constant.**

Read it with three caveats, always (full text in `runs/phase0/report.md` §6 and [PLAN.md](PLAN.md)):
the human baseline is weak *inside this box* (random search catches it after ~8 evaluations); the
warm-start gain at a 1000-evaluation budget sits inside the noise (+0.004, with larger variance); and
**the winner parks a coil 4.2 mm from the midplane scoring point** — the objective still has no
conductor-clearance constraint, so it is most likely an unbuildable degenerate solution rather than a
machine-designed device.

Live status, data sources and the gate list: [docs/wiki/](docs/wiki/index.md) (wiki-first, versioned
with the code).

---

## Architecture (Go is the primary stack; Python only assists)

```
cmd/forge/            CLI: baseline / verify / design / run / benchmark / rules / report / registry / xcheck / world
internal/
  config/             single source of truth: constants, search box, objective weights
  physics/            exact circular-filament magnetostatics (elliptic integrals via AGM)
                      + an independent segmented Biot–Savart implementation to check it
  objective/          score = field + mirror ratio + good-field volume − ripple − cost − violations
  baseline/           human baseline: Helmholtz-like cell + mirror throats
                      (the cell current is *solved* to hit the field target, not guessed)
  registry/           design registry (JSONL, parent_design lineage — memory, not a log)
  runner/             score → allocate id → persist, so the registry is complete by construction
  search/             random / lhs / evolution / evolution_warm (compared at equal budget)
  experiment/         equal-budget benchmark + generalisation probe + results JSON
  knowledge/          mines design rules from the registry (per-run Spearman, same sign required)
  report/             Chinese run report, including a mandatory honest-limits section
  rlenv/              the Phase-1 RL environment interface + a random-policy reference
  design/             internal design gate layer: upstream ProjectionPhysics closed forms,
                      the six gates, and its upstream anchors (gate G17)
  world/              the world protocol: rlenv semantics moved outside the process (JSONL lines, gate G18)
  owners/             file-ownership roster + freeze gate (no overlap / no gap / mutation-tested)
python/               auxiliary only: independent scipy oracle (cross-language check) + figures + statistics
```

Two hard rules:

1. **Go is the engine.** Python appears only where it is genuinely better:
   independent numerical verification, figures, statistical cross-checks. It does
   not carry engine logic.
2. **One place for every number.** Constants, bounds and weights live in
   `internal/config/config.go`; the Python side reads the spec from
   `testdata/golden_spec.json` instead of duplicating defaults.

---

## Physics scope and honest limits (v0.1)

`internal/physics` is **exact vacuum magnetostatics**: the closed form for a
circular filament current loop (complete elliptic integrals, computed by AGM),
plus a second, independent segmented Biot–Savart implementation so the two can
check each other.

It does **not** contain: plasma (pressure, diamagnetism, equilibrium, finite
beta), eddy currents, conductor current sharing, or the true field of a
finite-thickness winding pack. Every v0.1 result is therefore a **vacuum-field
design** result — "is this coil configuration's field structure good", not "will
this machine fuse".

The human baseline is not a straw man: its geometry follows textbook mirror
proportions and its current is *solved* so the volume-averaged midplane field
lands exactly on the 1.0 T target. Machine and human are compared under the same
objective.

## Quick start

```bash
# every acceptance gate (cross-language oracle, registry integrity, anti-fabrication, world protocol)
bash scripts/verify.sh phase0

# the world protocol (G18): serve / byte-exact replay / a client in another language
printf '%s\n' '{"op":"hello"}' '{"op":"reset","seed":12345}' '{"op":"close"}' | go run ./cmd/forge world serve
go run ./cmd/forge world serve --replay testdata/world_trace_golden.jsonl
python3 python/aux/world_client.py --replay testdata/world_trace_golden.jsonl

go run ./cmd/forge baseline
go run ./cmd/forge benchmark --budget 1000 --seeds 0,1,2 --methods random,lhs,evolution,evolution_warm
go run ./cmd/forge rules  --registry runs/phase0/registry.jsonl --out knowledge/design_rules.md
go run ./cmd/forge report --results runs/phase0/results.json --out runs/phase0/report.md
```

## Layout

- `runs/<tag>/` — the evidence of a run: `registry.jsonl` (one line per evaluation), `results.json`, `report.md`, `figures/`
- `knowledge/design_rules.md` — the knowledge base: rules with their evidence (rho, same-sign run count, decile contrast)
- `testdata/golden_*.json` — frozen cross-language numeric anchors; `testdata/world_trace_golden.jsonl` — one deterministic world-protocol session (the evidence for G18, written by `--record`; its `runs/` directory stays out of git)
- `CONTRACT.md` — the parallel-build contract (file roster, numeric anchors, acceptance gates, mainline rulings)
- `docs/wiki/` — the **wiki-first knowledge system**: overview / current status / sources / repo strategy / changelog, plus 21 validation scripts
- `manifests/raw_sources.csv` — registry of local raw originals (the originals themselves stay out of git)
- Site: [hushfusion.pages.dev](https://hushfusion.pages.dev) — the brand site; this project (FORGE) is introduced, with its measured numbers, on the **Progress page under `#forge`**

## Version and License

`v0.1.2` — **criterion change (PATCH)**: buildability (conductor-to-constraint-region clearance)
is now part of the objective. Re-running the identical tuple: the machine-vs-human margin goes
**+1.672 → +1.2395**, feasible records **11072/12001 → 7221/12001**, and a new gate **G23** pins the
claim that 0.1.0's degenerate winner must be bitten. Criterion semantics and the release checklist:
[docs/version-0.1.2.md](docs/version-0.1.2.md); what changed and **what it means for older evidence**:
[CHANGELOG.md](CHANGELOG.md).

`v0.1.0` — Phase 0: the design → score → record → learn loop runs (`bash scripts/verify.sh phase0`).
**`runs/phase0/` is evidence under *that* criterion and is never re-run in place** — the default tag
moved from `phase0` to `phase1`.

MIT — Copyright (c) 2026 LIU YUANJIE（刘元杰）
