# HUSHFUSION Forge

[中文](README.md) | **English**

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

---

## Architecture (Go is the primary stack; Python only assists)

```
cmd/forge/            CLI: baseline / verify / run / benchmark / rules / report / registry / xcheck
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
# every acceptance gate (cross-language oracle, registry integrity, anti-fabrication)
bash scripts/verify.sh phase0

go run ./cmd/forge baseline
go run ./cmd/forge benchmark --budget 1000 --seeds 0,1,2 --methods random,lhs,evolution,evolution_warm
go run ./cmd/forge rules  --registry runs/phase0/registry.jsonl --out knowledge/design_rules.md
go run ./cmd/forge report --results runs/phase0/results.json --out runs/phase0/report.md
```

## Layout

- `runs/<tag>/` — the evidence of a run: `registry.jsonl` (one line per evaluation), `results.json`, `report.md`, `figures/`
- `knowledge/design_rules.md` — the knowledge base: rules with their evidence (rho, same-sign run count, decile contrast)
- `testdata/golden_*.json` — frozen cross-language numeric anchors
- `CONTRACT.md` — the parallel-build contract (file roster, numeric anchors, acceptance gates)

## License

MIT — Copyright (c) 2026 LIU YUANJIE（刘元杰）
