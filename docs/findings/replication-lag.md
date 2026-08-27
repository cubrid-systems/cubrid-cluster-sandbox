# OQ7 — how is lag injected, and does the heartbeat permit it?

**Answers: suspend a stage; and yes, the heartbeat permits it.** Measured
2026-08-27, two-node containerised HA pair, CUBRID 11.5.0, one cluster and seven
phases (`harness/oq7-lag.sh`; console output, marks and 0.5 s samples kept at
`harness/results/oq7-*`).

The run also produced something nobody asked for and that changes G9: **the
observable the foundation chose does not observe what it was chosen to
observe.**

## The two mechanisms

| Mechanism | Stage-selective | Heartbeat interferes | Reverses | Verdict |
|---|---|---|---|---|
| `SIGSTOP` on `copylogdb` / `applylogdb` | **yes** — the only way to separate them | **no** (measured) | instantly, on `SIGCONT` | the default |
| `tc netem delay` on the node interface | no — slows copying, apply follows | no | on `qdisc del`, but the backlog drains slowly | for realism, not for control |

**The heartbeat does not notice a suspended process.** Both replication
processes were stopped for 30 s each, and after both stalls
`cubrid heartbeat status` still listed the same pids in `state registered`
(`copylogdb:260 applylogdb:263`, unchanged across the whole run) with no
`[Failback]` or `[Failover]` line in the master log. The heartbeat monitors
process *existence*, not progress — the CBRD-26983 session saw it react to a
*dead* process in 10 ms, and a live-but-frozen one is invisible to it. Stage
suspension therefore does not fight the engine, which is what OQ7 was unsure of.

`tc netem delay 200ms` grew the apply lag from 52,771 to 68,201 log pages over
30 s and had not drained 30 s after removal (63,849). It works; it just cannot
say *which* stage it is slowing.

## The finding that changes G9

The foundation's G9 acceptance reads per-node replication state from
`db_ha_apply_info` over SQL. Two things are wrong with that as a sole source.

**1. The view is written by `applylogdb`, so a stalled applier freezes its own
report.** With `applylogdb` suspended for 30 s, *every* column held still —
not just the applied position:

```
21s  (before)          eof 40419  final 12633  lag 27786  ins 340000
36s  (+15s stopped)    eof 40419  final 12633  lag 27786  ins 340000
51s  (+30s stopped)    eof 40419  final 12633  lag 27786  ins 340000
57s  (+5s resumed)     eof 71143  final 16288  lag 54855  ins 440000
```

`eof_lsa` is how far *copying* has reached, and copying never stopped — yet it
did not move for thirty seconds, because the row lives in `_db_ha_apply_info`
and the log applier is what writes it. A monitor polling this view sees a
**constant** lag during a total apply stall, which is indistinguishable from a
healthy steady state, and only learns the truth when the applier resumes (lag
jumped 27,786 → 54,855 in one sample).

**2. During a copy stall the lag moves in the reassuring direction.** With
`copylogdb` suspended, `eof_lsa` froze while `applylogdb` kept draining the
backlog already on disk, so the reported lag **fell**:

```
78s   (copy stall begins)  eof 80459  final 30915  lag 49544
93s   (+15s)               eof 80459  final 41883  lag 38576   <- falling
116s  (+30s)               eof 80459  final 41883  lag 38576
```

Replication was completely stopped and the number got better.

**3. The view that does see it is `applyinfo -r <master>`, and it is text.** Run
during the same copy stall it reported the truth:

```
 *** Copied Active Info. ***      EOF LSA : 32765 | 24        <- the slave
 ***  Active Info. ***            EOF LSA : 81109 | 9696      <- the master, via -r
 *** Delay in Copying Active Log ***
 Delayed log page count         : 48343
 Estimated Delay                : - second(s)
```

48,343 pages behind, while `db_ha_apply_info` said 38,576 and falling. Note the
`Estimated Delay : -`: this is the first-sample behaviour the foundation cited
from the code (`process_rate` is zero until a second iteration,
`util_cs.c:3893-3903`), now observed rather than read.

**Consequence for G9.** A replication monitor needs a **master-side reference**;
the slave's catalog view alone is not a lag measurement. Either the collector
also reads the master's append LSA and computes the difference itself, or it
accepts that it can only see the *apply* half and says so. What it must not do
is present `eof - final` as "replication delay".

## A usability trap in `applyinfo`

`-L` is the **copied log path**, not the database directory. `-L /db` produced,
four times:

```
Can't generate the applied info due to an invalid path of the -L option or no
related information in the db_ha_apply_info catalog table.
```

The correct value here is `/db/hadb_n65-n1` — the per-peer copy directory. The
error conflates two unrelated causes (wrong path, empty catalog) in one string,
which is exactly the kind of thing a provisioner should get right on the user's
behalf.

## Capacity, as an aside

The load was deliberately heavy — 20,000-row `INSERT … SELECT` batches at about
1.4 batches/s — and **a single `applylogdb` could not keep up with it at any
point**. The baseline phase, before any injection, already showed 27,786 pages
of lag, and at the end of the run the master held 3,440,000 rows against the
slave's 1,680,000. C-053 worried that importdb at `--degree=4` could outrun one
applylogdb and WU-51b did not reproduce it at that scale; at this write rate it
reproduces easily.

**This confounds the injected numbers and they should not be read as calibrated
lag measurements.** The mechanism questions OQ7 asked are answered decisively;
"how many pages does 200 ms of latency cost" is not, and would need a load the
replication pipeline can actually keep up with.

## Reproducing

```bash
cd harness && bash oq7-lag.sh     # ~4 min: baseline, apply stall, drain, copy stall, drain, netem, drain
```
