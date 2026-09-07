---
title: The Active-Active window after a healed partition, and the divergence it leaves
category: findings
project: cluster-sandbox
summary: Six runs, two arms, three repeats each. The window is real and it is as long as ha_calc_score_interval_in_msecs — about 12 s at 15000, about 1 s at the default. What it leaves behind is not "data syncing both ways" but a one-directional merge: the promoted slave's rows reach the restored master, the master's rows never reach the slave, and every gauge afterwards reports a healthy cluster. A master calling itself to-be-master was not observed in any run. Re-measured on 2026-09-07 against a develop build four months newer: seven runs, the same direction every time, and the one-row-per-side limit lifted.
created: 2026-09-03
updated: 2026-09-07
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
arrives, **and takes a very long time to do it**. Measured across three healed
clusters: **0.63 s** on a healthy pair, **25.97 s** on the diverged one above,
and **53 s** on a third where the split brain had healed cleanly with no
divergence at all. So the slowness is not a symptom of the divergence; it is what
a healed split brain does to replication for the best part of a minute, and a
check with the default thirty-second wait reports a stall that is not one. That
is why `repl check --wait` exists and why the scenario in `scenarios/` names a
longer one. `ha resync`, asked what repair this cluster needs, correctly reports
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

## Re-measured, because a reader reported the opposite

A CUBRID engineer who had never used this tool was given the README and asked to
reproduce something. They picked this. They wrote disjoint rows on each master
during a split, healed it, and reported both databases converging to the
**union** — the "both ways" this finding says does not happen — and the same
scenario file passing on one run and failing on the next.

That is the report this project exists to take seriously, so the method above was
run again on 2026-09-07 against **11.5.0 `5f3a30d`**, a develop build four months
newer than the one measured here:

| arm | runs | restored master ← promoted slave's rows | standby ← master's rows |
|---|---|---|---|
| the method above, one row per side | 3 | **yes 3/3** | **no 3/3** |
| two rows on the master's side only | 2 | — | **no 2/2** |
| the reader's shape: 2 rows and 3 rows | 2 | **yes 2/2** | **no 2/2** |

```
during split:  n1=[1 10 11]             n2=[1 20 21 22]
after heal:    n1=[1 10 11 20 21 22]    n2=[1 20 21 22]     n1=active n2=standby
```

Seven runs, one direction, no exceptions. **The finding stands**, and the last
two rows of that table lift the limit below: the merge is one-directional for
several rows per side as well as for one, and the standby's loss scales with what
the master wrote rather than being a single stranded row.

**What the reader actually hit was a defect in this tool, not in the engine.**
`n1` was documented as a selector by both the README and
[`../design/01-cli.md`](../design/01-cli.md) §2 and implemented by neither — only
the full `hadb-n1` resolved. So their divergence INSERTs went into no database at
all, and the two sides being identical after the heal was replication working
normally rather than a bidirectional merge. They said so in their own report
before drawing the conclusion; the failure is that a command which does nothing
looks so much like one that worked. It is fixed, and the reproduction above was
run after the fix.

**A measurement is only as good as the addressing underneath it.** This project
has published a result from a single sample before and had to shrink it
([`switchover-threshold.md`](switchover-threshold.md)); this is the same lesson
from the other side — a tool that silently addresses nothing can manufacture a
contradiction out of a cluster that was never touched.

One thing has changed since this was written, in the right direction. The section
above says the divergence is reported by nothing, and lists the gauges that call
it healthy. `repl diff` was built the same day out of exactly that complaint, and
on the reproduction clusters it says so without being asked:

```
csb: 1 table(s) differ between aawx1-n1 and aawx1-n2: w. Replication may be
perfectly healthy and still never carry what is missing; the field's closure is
a slave rebuild
```

## Limits

- Two engine builds (11.5.0 `dd15f7f` for the six runs here, 11.5.0 `5f3a30d`
  for the seven that re-measured them), one machine, two nodes, containers on one
  docker network.
- The window is measured by **accepted writes**, one probe per node per second,
  so the baseline figure of about a second is at the resolution floor. The
  contrast with 12 s is far outside it; a claim that the baseline window is
  exactly 1 s would not be.
- `both_write_s` is the last second at which both nodes accepted a write, not a
  continuous-occupancy measure. It is an upper bound on the window's end, not a
  guarantee that every second inside it was dual-writable.
- The divergence check was one row per side when this was written. The
  re-measurement above extends it to two and three rows per side with the same
  result, so the direction is not an artefact of writing a single row — but it
  still does not bound how much data a longer or busier split would strand.

## What follows

- **`ha_calc_score_interval_in_msecs` is a two-sided parameter.** It is the one
  setting shown to move the switchover threshold, and it moves this window by the
  same amount. Any recommendation to raise it has to state both.
- **A healed split brain needs a divergence check, not a lag check.** That is
  `repl diff`, built the same day: it takes its table list from the catalog
  rather than from the applier's error log, because a split brain fails nothing and that log
  is empty exactly when the divergence is largest. On the cluster above it
  reports `w  master=3  standby=2  DIFFERENT` while every gauge beside it reads
  healthy ([`../design/05-inspect.md`](../design/05-inspect.md) §4a).
- **`ha resync` no longer concludes "resume" from a zero fail counter.** It
  compares first, and on that cluster now answers `slave` — naming the table and
  saying that replication will not carry it.
- **`repl check` should not be read as an equality proof.** It proves the path is
  open. The distinction belongs next to the verb, and it now is.
- **Nothing closes the gap but a rebuild**, and that is not a limitation of this
  tool. The standby's recorded position has moved past the write it is missing —
  the canary that arrives is the proof — so no re-fetch will ever happen.
  `ha resync --path slave` now performs the engine's own rebuild
  (`ha_make_slavedb.sh`'s steps, without the ssh): online `backupdb` on the
  master, `restoreslave` on the standby, rejoin. Three runs on diverged pairs:
  **19 s, 21 s, 23 s**, each ending with every table matching and a canary
  arriving in 0.08–0.61 s, against 25.97 s on the diverged cluster
  ([`../design/04-faults.md`](../design/04-faults.md) §8).
