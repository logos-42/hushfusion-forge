# python/ — auxiliary verification layer (stage G)

Python is **not** part of the engine; it is the independent check on it. Nothing
here imports Go, and nothing here calls a `forge` binary. Everything is driven by
`testdata/golden_spec.json`, which is the single source of truth for the device
parameters, so the two languages cannot drift apart silently.

| file | role |
|---|---|
| `aux/oracle.py` | independent numpy/scipy re-implementation of the physics + objective (gate G5) |
| `aux/schema_check.py` | registry JSONL schema parity (gate G9) |
| `aux/rules_check.py` | re-derives the mined Spearman rules from the registry and reconciles them |
| `aux/analyze.py` | best-so-far / axis-profile figures + equal-budget markdown table |
| `tests/` | pytest suite for all of the above (including the negative cases) |

## Running

```bash
# gate G5: recompute the golden numbers and compare
python3 python/aux/oracle.py --check-golden testdata/

# compare a Go field export with the scipy oracle, point by point.
# `forge xcheck` writes runs/scratch/field_samples.json in exactly this shape.
python3 python/aux/oracle.py --compare-go runs/scratch/field_samples.json
# ... and like-for-like against Go's 5 mm near-wire clamp (see below)
python3 python/aux/oracle.py --compare-go runs/scratch/field_samples.json --proximity-floor 0.005

# regenerate golden files into a scratch dir for human review (refuses testdata/)
python3 python/aux/oracle.py --emit-golden /tmp/golden_review

# gate G9
python3 python/aux/schema_check.py runs/phase0/registry.jsonl

# rule reconciliation (needs what stage E writes); --selftest exercises the gate itself
python3 python/aux/rules_check.py --rules knowledge/design_rules.md \
                                 --registry runs/phase0/registry.jsonl
python3 python/aux/rules_check.py --selftest

# figures + table for one run
python3 python/aux/analyze.py runs/phase0

# tests
python3 -m pytest python/tests -q
```

`--compare-go` accepts either a bare JSON array of cases or a dict keyed
`samples`/`points`/`records`/`cases`; each case needs a 12-entry `design`, the
sample points (`points_r`/`r` and `points_z`/`z`) and the Go-side field values
(`br`, `bz` and/or `b_mag`). `golden_field_samples.json` is a valid input, so the
same path can be tested without Go.

## Contract defects found while implementing the oracle

1. **`internal/physics/api.go` prints the `B_r` closed form without the `1/r`
   factor.** The doc comment says
   `B_r = C*z/(2*alpha2*beta) * [...]`, but the correct expression is
   `C*z/(2*alpha2*beta*r) * [...]`. The frozen golden samples and a direct
   Biot-Savart quadrature both require the `1/r`: at the near-throat probe point
   of `golden_field_samples.json` the doc's form gives `-1.2185 T` where the
   golden file gives `-4.0457 T` (exactly the right value). `oracle.py` implements
   the physical form (golden wins); a Go implementer who codes the comment
   literally will fail G5 on every off-axis sample.
2. **`B_coil_max` is under-specified.** The comment says "field from all *other*
   coils at that coil's location + `spec.SelfField()`", which reads like
   `|sum_j B_j| + self` (gives 3.3346 T for the baseline) but the golden value
   3.4163 T is `sum_j |B_j| + self` (verified to ~4e-13). The oracle uses the
   golden semantics.
3. **Aggregation of the per-run rhos is pinned only by tolerance.** The knowledge
   docs do not say whether the reported `rho` is the mean, median or worst-case of
   the per-run coefficients. `rules_check.py` computes all three and requires the
   published value to match the closest one within 0.02.
4. **The 5 mm near-wire clamp is applied at different scopes.** api.go describes the
   floor for the coil-to-coil singular case in `B_coil_max` ("if another coil sits
   closer than 5e-3 m"). `internal/physics/magnet.go` applies it to *every* sample
   whose squared distance to the nearest wire (`alpha2`) is below the floor. On the
   golden probe set this is invisible (nothing comes within 5 mm of a wire), but the
   `forge xcheck` perturbed design puts one probe 4.896 mm from a wire and the two
   definitions then differ by 4.3% (Go 67.85 T vs exact 70.78 T). The oracle's
   default is the exact closed form; `--proximity-floor 0.005` reproduces Go's
   clamp for a like-for-like cross-check, and `compare_go` always reports how many
   points are inside the floor so the mode cannot hide the difference.

## Measured cross-language agreement

`forge xcheck` (3 designs x 32 points, 96 samples: the golden textbook mirror, a
perturbed design at r+5%/z-5%/I+8%, and a box-centre design):

```
exact closed form           max relative 5.4e-02  (1 point inside the 5 mm wire floor)
--proximity-floor 0.005     max relative 2.2e-12   <-- like-for-like, PASS
```

So Go's analytic solver and this scipy oracle agree to ~1e-12 everywhere the
singular branch is not deliberately clamped, and the only divergence is the
documented near-wire definition above.

## Definitions that the golden files cannot pin (chosen here, documented honestly)

* `ripple`: interior extrema by strict 3-point comparison; consecutive alternating
  (max, min) pairs contribute `|peak - valley|` only when the depth exceeds
  `prominence * B_mid` (prominence = 0.05). The baseline is sensitive to this
  choice -- its in-cell axis profile dips only ~0.030 T below its side lobes, which
  is why the golden `ripple` is 0.0 and not something larger. The `> 0.05` cut is
  implemented as a strict inequality, so a 0.030 T structure is dropped.
* `coil_proximity_floor_hit`: golden only ever shows `0.0` (the baseline never gets
  close). The oracle flags it when two coil centres are closer than 5e-3 m and
  evaluates the singular field at a point displaced to that separation.
* `min_coil_gap_m` is the 3-D centre distance `hypot(dr, dz)` (validated: 0.5 m for
  the baseline).
