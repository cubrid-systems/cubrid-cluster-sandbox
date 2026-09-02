---
title: What actually decides when a CUBRID HA cluster switches over
category: findings
project: cluster-sandbox
summary: Seven runs, one variable at a time, under load. The documented failover arithmetic — heartbeat interval × max gap — does not describe when a role change happens: raising either parameter fourfold moves nothing. ha_calc_score_interval_in_msecs does, from 6.9 s to 18.9 s. That discriminates the three explanations the field's own stalled measurement could not separate.
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

This is that measurement, run seven times through the tool.

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

| Parameter | Value | Predicted | **Measured** |
|---|---|---|---|
| `ha_max_heartbeat_gap` | 5 (default) | 2.5 s | **8.9 s** |
| `ha_max_heartbeat_gap` | 10 | 5 s | **8.0 s** |
| `ha_max_heartbeat_gap` | 20 | 10 s | **8.9 s** |
| `ha_heartbeat_interval_in_msecs` | 1000 | 5 s | **6.9 s** |
| `ha_heartbeat_interval_in_msecs` | 2000 | 10 s | **6.9 s** |
| `ha_calc_score_interval_in_msecs` | 3000 (default) | 2.5 s | **6.9 s** |
| `ha_calc_score_interval_in_msecs` | 15000 | 2.5 s | **18.9 s** |

**The documented arithmetic does not describe this transition.** Predicted spans
2.5 s to 10 s across the first five rows; measured never leaves 6.9–8.9 s. Four
times the gap changes nothing. Four times the interval changes nothing — 6.9 s
twice, to the tenth of a second.

**`ha_calc_score_interval_in_msecs` is what moves it.** Five times the value
takes the role change from 6.9 s to 18.9 s, cleanly outside the band every other
run sat in.

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

Each point is a single run. The baseline is the exception and is the reason to
trust the rest: 6.9 s appears in three runs that varied different parameters,
which is what makes the 18.9 s outlier an effect rather than a fluctuation. A
proper answer for the field wants repeats at each point and more values of
`ha_calc_score_interval_in_msecs` to see whether the relationship is linear —
5× the parameter produced 2.7× the interval, so it is plainly not proportional.

Nor does this explain *why*. It locates the mechanism in the score calculation
rather than in heartbeat counting, and that is where reading the code should
start; it is not itself a code finding.

Every run ended with **two masters**, which is the expected consequence of a
partition with `ha_ping_hosts` unset and is not what was being measured here.
