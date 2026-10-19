# reading-dev Shadow A/B Review

## Reports

- Baseline: `results/run-20260924T084948Z-15223.json`
- Shadow application: `results/run-20260924T085008Z-15369.json`
- Both completed all three turns. The corpus and source hashes match (`ef5cae2f...` and `019648e7...`), as do the case, model (`deepseek-v4.1-flash`), provider host, budget (8000), runtime budget, retry policy, Go version, and OS. Owners differ as intended.

## Quality Review

The baseline and applied answers each meet the fixed `reading-dev` expectations:

| Turn | Review |
|---|---|
| 1 | Both report that only lines 1-27 were returned from the 70-line request, identify truncation/incompleteness, and say more content is available (`next=28`). The tool event confirms `HasMore=true` and `ContentTruncated=true`. |
| 2 | Both give `MID-3709` at line 48 and `PAGE-6248` at line 96. Tool events confirm reads of 45-55 and 90-100; the first range has more file content after it, while the tail has no further page. |
| 3 | Both say to validate source location and pagination before considering vectors, state that no release date is given, and cite `reading.md:96`, which contains those facts. |

This is a manual review of these six answers only. The generated reports remain `quality=unreviewed`; this note does not establish general model quality.

## Input and Cost

The 8000-token candidate budget produced no input reduction. The candidate-only report kept every message for all five logical model requests. In the apply report all five adapter calls have `applied=true`, but every call also has `dropped_messages=0`; sent and original estimates are both 16687 in total. The wrapper accepted the candidate, but the candidate was identical in message selection to the full input.

Provider usage was complete for all five calls in both reports, with no child or summary calls. Baseline usage was 17044 prompt and 618 completion tokens; the apply run was 16974 prompt and 508 completion tokens. Fees and cached-token counts are unknown. Since no messages were dropped, the 70-token prompt difference and 110-token completion difference cannot be attributed to the context policy; assistant replies differed between runs. Elapsed time also varied (16904 ms vs. 10529 ms) and is not attributable to pruning or cache behavior.

Conclusion: quality passed for this one scenario, but this pair does not demonstrate token savings. At 8000, each individual candidate estimate was already below budget, so the application path was a no-op.

## 4000 Diagnostic Pair

- Candidate-only: `results/run-20260924T091518Z-15629.json`
- Applied: `results/run-20260924T091530Z-15730.json`
- Both completed three turns with the same corpus/source hashes, model, provider, runtime budget, retry policy, Go version, and OS. Owners differ as intended; report `quality` remains `unreviewed`.

The semantic checks passed in both groups: answers report the 1-27 truncated read and next page, identify `MID-3709` at line 48 and `PAGE-6248` at line 96 based on the recorded 45-55 and 90-100 reads, and preserve the line-96 recommendation without inventing a release date. The candidate-only replies are 67, 95, and 76 Unicode characters. The applied replies are 133, 189, and 90 characters; turns 1 and 2 violate the prompt's 100-character limit. Under the manual-review rules this applied run fails the full quality gate despite correct facts and citations. Both pruned calls set `needs_constraint_review=true`, which appropriately calls for this human check.

Turn 1's overlong applied answer followed a full-input fallback (`mandatory_over_budget`), so the length failure cannot be attributed to pruning from this pair. Turn 2 is overlong after a pruned request, but one run cannot distinguish a policy effect from model variation. The applied strategy is not yet quality-validated for this case.

The applied report shows two real reductions: on turn 2, one adapter call went from 9 to 5 messages and 4073 to 3900 estimated tokens; on turn 3, one went from 11 to 7 messages and 4082 to 3909. Each dropped four older messages and reduced the project's estimate by 173 tokens. The first turn's follow-up call was `mandatory_over_budget` at 4085 tokens, so the wrapper correctly sent the full input. Across the five applied calls, estimated input fell by 346 tokens out of 16892 full-input estimated tokens, about 2.0%; the selected calls each fell by about 4.2%.

Provider usage was complete for five of five calls in both reports. Aggregate Prompt was 17060 baseline and 16734 applied; Completion was 723 and 621. The prompt difference is 326 tokens, but model replies and therefore later conversation histories differed, so this run-level difference is not a causal estimate of savings. Fees and cached-token counts are null. The pair demonstrates that the wrapper can prune in this scenario, but it does not demonstrate acceptable answer quality or material total-task savings.

## 4000 Repeat Pair

- Candidate-only: `results/run-20260924T094106Z-15997.json`
- Applied: `results/run-20260924T094118Z-16092.json`
- The reports have matching corpus/source hashes, model, provider, budget, runtime, retry policy, Go version, and OS; owners differ as intended. Both completed all three turns and report `quality=unreviewed`.

The candidate-only reply lengths are 101, 79, and 48 characters. Turn 1 is one character over its limit. Applied reply lengths are 59, 81, and 80; all three satisfy the 100-character constraint and the expected facts/citations. In turn 2 the model notes that the previous 1-27 body was externalized and not reverified. The fixture's first 27 lines contain 2200 characters, over the 2000-character spill threshold; the earlier tool event still records the 1-27 read, while no readback of that body appears in this turn's tool events. This is a grounded limitation statement, not a false claim that the earlier read never happened.

Only the turn-3 application changed the input: it dropped four messages (11 to 7) and reduced the estimate from 4019 to 3864, a 155-token reduction. Turn 1's continuation fell back at 4120 tokens, and turn 2's continuation also fell back because the candidate remained over budget (4186 original, 4031 candidate). Across the applied run, estimated input fell from 16959 to 16804, about 0.9%.

Provider usage was complete for five of five calls in both reports. Candidate-only Prompt/Completion was 17055/641; applied was 17012/1004. These totals reflect different model replies and histories, so the 43-token Prompt difference is not attributable to pruning. Fees and cached-token counts are null.

Across the two 4000 applied runs, one run failed the response-length requirement on turns 1 and 2, while the repeat met it on all turns. The candidate control also has one overlength turn in the repeat. These small samples show variable length compliance rather than a stable, policy-specific effect. Preserve the failure; do not mark `reading-dev` quality as consistently passed or switch the default.

## Next Measurement

Continue to `resume-dev` at 4000 as the next independent dev scenario, checking retention of the explicit goal, user constraint, next step, and unknown-date boundary. Keep the `reading-dev` length failure in the evaluation record; do not use the passing repeat alone to declare stable quality. `resume-dev` reconstructs the Harness in the same process, not across a real elapsed-time gap, process restart, or zero-cache condition. The real-model commands incur provider usage:

```bash
CONTEXT_BASELINE_SHADOW_BUDGET=4000 bash scripts/context-baseline.sh --real resume-dev
CONTEXT_BASELINE_SHADOW_BUDGET=4000 bash scripts/context-baseline.sh --real resume-dev --apply-shadow
```
