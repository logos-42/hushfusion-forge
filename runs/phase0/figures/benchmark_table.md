Equal-budget benchmark (tag `phase0`, budget 1000 evals/method/seed, seeds [0, 1, 2]).

| method | seeds | best mean | best std | best min | best max | beating human | evals to beat (mean) |
|---|---:|---:|---:|---:|---:|---:|---:|
| human baseline (`textbook_mirror`) | - | -0.290571 | - | - | - | - | - |
| evolution | 3 | 1.303252 | 0.015924 | 1.291973 | 1.321468 | 3/3 | 8.3 |
| evolution_warm | 3 | 1.307292 | 0.064361 | 1.265627 | 1.381419 | 3/3 | 11.3 |
| lhs | 3 | 0.628765 | 0.021582 | 0.604599 | 0.646119 | 3/3 | 14.0 |
| random | 3 | 0.838502 | 0.191865 | 0.617634 | 0.963950 | 3/3 | 8.0 |

Human baseline score: `-0.290571`. Higher is better.

_Aggregate block cross-checked against a recomputation from `runs[]` (tolerance 1e-9 relative)._
