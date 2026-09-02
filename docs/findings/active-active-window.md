---
title: The Active-Active window after a healed partition, and the divergence it leaves
category: findings
project: cluster-sandbox
summary: Six runs, two arms, three repeats each. The window is real and it is as long as ha_calc_score_interval_in_msecs — about 12 s at 15000, about 1 s at the default. What it leaves behind is not "data syncing both ways" but a one-directional merge: the promoted slave's rows reach the restored master, the master's rows never reach the slave, and every gauge afterwards reports a healthy cluster. A master calling itself to-be-master was not observed in any run.
created: 2026-09-03
updated: 2026-09-03
lang: en
---

# The Active-Active window after a healed partition

The split-brain table in [`../design/04-faults.md`](../design/04-faults.md) §5 has
three rows and, until now, one of them was a claim rather than a measurement:

> with `ha_calc_score_interval_in_msecs` raised, a cluster whose slave was
> promoted during a partition runs **Active-Active for the length of that
> interval once the network heals**, with data syncing *both ways* — and,
> separately, a master describing itself as `to-be-master`.

Both are recorded as `특이사항` in the field's own hidden-parameter test, in a run
that could not tell an engine behaviour from a test artefact
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §2).
That ticket has been open since 2022.

This is that measurement.

## Method

Six clusters, each built from nothing and destroyed after
([`../../harness/calc-score-window.sh`](../../harness/calc-score-window.sh)):

```
csb cluster create [--set-hidden ha_calc_score_interval_in_msecs=15000]
csql: CREATE TABLE w; INSERT (1,'before')
csb fault splitbrain                 # two masters, flavour named by the tool
csql on n1: INSERT (101,'n1')        # one row per side, written while neither
csql on n2: INSERT (201,'n2')        # side can see the other
csb fault clear                      # the heal — the clock starts here
```

Then, every second for forty seconds, on **both** nodes: does it accept an
`INSERT`, and what does `changemode` call it. A standby refuses a write and so
does a node in `to_be_active`, so "both accepted" is the line the Active-Active
claim is drawn on. At the end, each node is asked whether it holds the *other*
side's row — a fact about rows rather than an impression about roles.

The arms differ in one parameter. `baseline` leaves
`ha_calc_score_interval_in_msecs` at its default 3000; `raised` sets it to 15000.
Three repeats each, because this project has published an effect from a single
sample before and had to shrink it on repetition
([`switchover-threshold.md`](switchover-threshold.md)).

## The window is real, and it is the length of the interval

| Arm | interval | run 1 | run 2 | run 3 | both nodes accepted writes for |
|---|---|---|---|---|---|
| `baseline` | 3000 ms | 2 s | 1 s | 0 s | **~1 s**, at or below the sampling resolution |
| `raised` | 15000 ms | 11 s | 12 s | 12 s | **~12 s** |

The first second at which only one node still accepted a write was 4, 2, 1 in the
baseline arm and **13, 13, 13** in the raised arm. Three identical figures out of
three is the parameter, not the weather.

So the field's first `특이사항` reproduces, and the mechanism it implies is right:
**the window is how long the group takes to notice it has two masters, and that
is what `ha_calc_score_interval_in_msecs` sets.** Raising it to widen a
switchover threshold ([`switchover-threshold.md`](switchover-threshold.md)) widens
this window by the same amount, which is a trade nobody is currently told they
are making.

All six runs ended with the original roles restored — `n1` active, `n2` standby.
The raised interval does not prevent recovery. It lengthens the interval during
which both nodes accept writes.

## "Syncing both ways" is not what happens, and the truth is worse

Each side wrote one row while neither could see the other. Afterwards:

| | holds the other side's row |
|---|---|
| `n1` (restored master) ← row 201, written on `n2` | **yes**, 5 of 6 runs |
| `n2` (standby) ← row 101, written on `n1` | **no**, 0 of 6 runs |

Rows crossed in exactly one direction, and it is the *opposite* direction from
the settled roles: what the promoted slave wrote came back to the restored
master, and what the master wrote during the split never reached the slave. A
direct read thirty seconds after one heal:

```
rows on cwa2347-n1:  1  101  201
rows on cwa2347-n2:  1       201
```

**That divergence is permanent, and nothing reports it.** On the same cluster, at
the same moment:

```
repl status   n1  apply_lag=0  fail=0
              n2  apply_lag=0  fail=0   copy=2 pages behind applyinfo -r n1
repl check    arrived on n2 in 25.97s
ha resync     would take path "resume" — fail_counter is 0:
              replication is behind at worst, not broken
```

Every gauge says healthy. The canary — a write made now, which has to arrive —
arrives. `ha resync`, asked what repair this cluster needs, correctly reports
that replication is not broken, because it is not: it is carrying new writes
fine. It simply never carried one old one, and no view in the engine remembers
that.

This is the same shape as the applier-stall lie in
[`../design/05-inspect.md`](../design/05-inspect.md) §3 and it is worse, because
there the gauge is frozen by a process anyone can see suspended. Here the gauge is
live, moving, accurate about the present, and silent about a row that is missing
forever. **A canary proves the path is open. It cannot prove the two databases
are the same.**

## `to-be-master` did not appear

Zero of six runs, sampling `cubrid changemode` on both nodes every second through
the whole window. That half of the field's `특이사항` is not reproduced here, which
is not the same as saying it does not happen — a different engine build, a
different node count or a longer interval may produce it. It is recorded as
unreproduced rather than dismissed.

## Limits

- One engine build (11.5.0, `dd15f7f`), one machine, two nodes, containers on one
  docker network.
- The window is measured by **accepted writes**, one probe per node per second,
  so the baseline figure of about a second is at the resolution floor. The
  contrast with 12 s is far outside it; a claim that the baseline window is
  exactly 1 s would not be.
- `both_write_s` is the last second at which both nodes accepted a write, not a
  continuous-occupancy measure. It is an upper bound on the window's end, not a
  guarantee that every second inside it was dual-writable.
- The divergence check is one row per side. It shows that a merge happened in one
  direction and not the other; it does not bound how much data a longer or busier
  split would strand.

## What follows

- **`ha_calc_score_interval_in_msecs` is a two-sided parameter.** It is the one
  setting shown to move the switchover threshold, and it moves this window by the
  same amount. Any recommendation to raise it has to state both.
- **A healed split brain needs a divergence check, not a lag check.** Row counts,
  or a comparison of the two sides — which is what `ha resync --path table`
  compares and what the field does by hand. The gauges are the wrong instrument
  and they answer confidently.
- **`repl check` should not be read as an equality proof.** It proves the path is
  open. The distinction belongs next to the verb, and it now is.
