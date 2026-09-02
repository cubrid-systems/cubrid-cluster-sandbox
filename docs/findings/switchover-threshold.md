---
title: What actually decides when a CUBRID HA cluster switches over
category: findings
project: cluster-sandbox
summary: Nineteen runs, one variable at a time, under load, with three repeats at each of the four points that carry the argument. The documented failover arithmetic — heartbeat interval × max gap — does not describe when a role change happens: raising either parameter fourfold leaves the measurement inside its own baseline band. ha_calc_score_interval_in_msecs moves it, by about 2x on means. That discriminates the three explanations the field's own stalled measurement could not separate.
created: 2026-09-02
updated: 2026-09-02
lang: en
---

# What actually decides when a CUBRID HA cluster switches over

A 2021 ticket asked for the settings that can trigger a switchover to be
documented and **validated in a user's environment**, and said in as many words
that developers cannot do it. A test that tried has been open since 2022. It
measured a role change at 8–11 s against an arithmetic 2.5 s and ended on three
candidate explanations of its own result — *"1) 테스트 케이스 문제? 2) 파라미터 값을
너무 작게 설정? 3) 시간에 따라 네트워크 속도 차이 문제?"* — with nothing recorded that
could separate them
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §2).

This is that measurement, run nineteen times through the tool — five points once
each, and then the four that carry the argument three times each, because one
sample per point is the shape of the problem rather than a solution to it.

## Method

Each row is a cluster built from nothing, loaded, partitioned, and destroyed:

```
csb cluster create --set-hidden <param>=<value>   # one variable, everything else fixed
csb load start --profile insert --rate 40/s --batch 50
csb fault partition master                        # heartbeats stop arriving; nothing dies
csb record export                                 # measured, predicted, and the settings
```

The fault is a partition rather than a kill on purpose: the parameters under test
govern **missed heartbeats**, and a dead process is noticed rather than waited
for. *Measured* is the tool's own `fault partition` to the engine's own
`[Failover] [Success]` line, taken from the run record. *Predicted* is the
documented arithmetic, `ha_heartbeat_interval_in_msecs × ha_max_heartbeat_gap`,
which the lab restated to a customer in 2023 as 500 ms × 5.

[`../../harness/sweep-switchover.sh`](../../harness/sweep-switchover.sh) is the
runner; [`../../harness/results/sweep-switchover.tsv`](../../harness/results/sweep-switchover.tsv)
is the output.

## Result

Three runs at each point, each a cluster built from nothing. The first table is
the sweep as it was first run, one sample per point; the second repeats the four
points that carry the argument, because a single sample per point is what left
the field's own measurement arguable.

| Parameter | Value | Predicted | Measured (n=3) | Mean |
|---|---|---|---|---|
| all defaults (`gap` 5, `interval` 500) | — | 2.5 s | 6.9 / 8.8 / **8.9 s** | 8.2 s |
| `ha_max_heartbeat_gap` | 20 | 10 s | 7.9 / 8.8 / **8.8 s** | 8.5 s |
| `ha_heartbeat_interval_in_msecs` | 2000 | 10 s | 6.9 / 6.9 / **6.9 s** | 6.9 s |
| `ha_calc_score_interval_in_msecs` | 15000 | 2.5 s | 12.8 / 17.9 / **18.9 s** | 16.5 s |

The single-sample sweep that preceded it, kept because it covers two values the
repeats drop — `gap` 10 and `interval` 1000 — and agrees with them:
[`../../harness/results/sweep-switchover-n1.tsv`](../../harness/results/sweep-switchover-n1.tsv).

**The documented arithmetic does not describe this transition.** Four times the
gap moves the mean by 0.3 s, and the two ranges overlap; four times the interval
moves it *down*. Predicted spans 2.5 s to 10 s across those three points and
measured stays inside 6.9–8.9 s.

**`ha_calc_score_interval_in_msecs` moves it, and by less than one run
suggested.** Every one of its three samples is above every baseline sample, so
the direction is not in doubt. The size is: 12.8 to 18.9 s against a baseline of
6.9 to 8.9. On means that is 2.0x, on the extremes anywhere from 1.4x to 2.3x —
and the first run of the first sweep, at 18.9 s, would have been reported as
2.7x if it had been the only one.

One detail that is one sample each and is recorded rather than read into: the two
slowest `calc_score` runs ended with `[Failover] [Success]` as the last HA line
while every other run ended with `[Failback] [Cancelled]`.

## What that settles

The third row of the sweep is a discriminator rather than another data point.
Two of the parameters moving nothing is compatible with two very different
stories — they are inert, or the values never reach the process that decides —
and those call for opposite responses. The third parameter travels the same path,
is written to the same file by the same command, and **moves the measurement**.
So the path works, and the first two are inert on it.

Against the three explanations the stalled test left open:

| Its candidate | This says |
|---|---|
| the test case | ruled out — one variable at a time, and the one that should do nothing per the documented model is the one that does everything |
| the value was too small | ruled out — fourfold on both, no movement |
| network variance that afternoon | ruled out — each row is a cluster built from nothing, and 6.9 s reproduces across three independent runs |

## What it does not settle

Three runs per point is enough to separate an effect from the baseline band and
not enough to size the effect: `calc_score` at 15000 spans 12.8 to 18.9 s, and
nothing here says why one run took six seconds less than another. More values
would say whether the relationship is even monotonic — 5× the parameter moved the
mean 2.0×, so it is plainly not proportional — and that is a sweep this tool can
now run unattended rather than a question that needs answering by argument.

Nor does this explain *why*. It locates the mechanism in the score calculation
rather than in heartbeat counting, and that is where reading the code should
start; it is not itself a code finding.

Every run ended with **two masters**, which is the expected consequence of a
partition with `ha_ping_hosts` unset and is not what was being measured here.
