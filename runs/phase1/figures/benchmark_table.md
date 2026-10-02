Equal-budget benchmark (tag `phase1`, budget 1000 evals/method/seed, seeds [0, 1, 2]).

| method | seeds | best mean | best std | best min | best max | beating human | evals to beat (mean) |
|---|---:|---:|---:|---:|---:|---:|---:|
| human baseline (`textbook_mirror`) | - | -0.290571 | - | - | - | - | - |
| evolution | 3 | 0.864223 | 0.132396 | 0.711652 | 0.948884 | 3/3 | 11.7 |
| evolution_warm | 3 | 0.892386 | 0.017965 | 0.877825 | 0.912462 | 3/3 | 17.3 |
| lhs | 3 | 0.536820 | 0.180258 | 0.328766 | 0.646119 | 3/3 | 40.0 |
| random | 3 | 0.475536 | 0.170851 | 0.285970 | 0.617634 | 3/3 | 9.3 |

Human baseline score: `-0.290571`. Higher is better.

_Aggregate block cross-checked against a recomputation from `runs[]` (tolerance 1e-9 relative)._
