### AgentGate: FAIL (regression)

`gpt-4.1-2025-04-14/retail` vs `gpt-4.1-mini-2025-04-14/retail` on 114 tasks. Pass rate 74.1% -> 66.0% (-8.1 points, 90% CI -12.5 to -3.8, one-sided p = 0.00121).

Rule: FAIL if the whole interval is below 0.0 points; PASS if it is entirely above -5.0 points; otherwise INCONCLUSIVE. With these tasks the smallest drop detectable at 80% power is 6.5 points.

| suite | tasks | A | B | diff | CI |
|---|---|---|---|---|---|
| retail | 114 | 74.1% | 66.0% | -8.1 | -12.5 to -3.8 |

|  | pass^1 | pass^2 | pass^3 | pass^4 |
| --- | --- | --- | --- | --- |
| A | 0.741 | 0.642 | 0.579 | 0.526 |
| B | 0.660 | 0.529 | 0.443 | 0.386 |

**13 tasks got worse by more than 25 points** (6 got better). New behaviour in their failures: `loop` in 1.

| task | A | B | new in B's failures |
|---|---|---|---|
| retail/103 | 75% | 0% | loop |
| retail/104 | 75% | 0% |  |
| retail/111 | 75% | 0% |  |
| retail/16 | 100% | 25% |  |
| retail/71 | 75% | 0% |  |
| retail/109 | 50% | 0% |  |
| retail/33 | 100% | 50% |  |
| retail/54 | 100% | 50% |  |
| retail/63 | 100% | 50% |  |
| retail/7 | 100% | 50% |  |
| ... 3 more | | | |

Agent cost: $26.52 -> $6.25.
