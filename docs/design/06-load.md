---
title: cluster-sandbox — Load
category: design
project: cluster-sandbox
summary: The workload driver, and why the tool owns it. Two kinds of load that must not be conflated — transactions against the master, and contention on the node — plus the one property that decides whether a measurement means anything: a stated rate the driver holds, and an honest report when it could not.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# Load

A sixth component, and the design did not have it. [`../DESIGN.md`](../DESIGN.md)
§4 fixes five layers; none of them generates traffic, because phase 0 assumed a
scenario brings its own. The requirements pass showed that assumption is what
broke the field's own measurement, so load is specified here as a component with
a contract rather than left to each scenario's shell loop.

Three requirements need it, and they need *different* things from it.

1. **The failover-loop repro is a host-contention recipe.** The field's own
   reproduction is HA on VMs plus *"큐브리드 빌드(Thread 20~40) 등의 로드 심한
   로드 상황"* — a compile. The mechanism in the report is heartbeat responses
   not arriving inside their window; replication volume is a *consequence*, not
   the cause ([`../requirements/01-failback-field-evidence.md`](../requirements/01-failback-field-evidence.md) §1).
2. **The threshold sweep needs the load to be the same every run**, or the sweep
   measures the load's variance instead of the threshold
   ([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §2).
3. **This project's own lag figures are uncalibrated for exactly this reason.**
   The phase-0 driver was an open-loop doubling insert; a single `applylogdb`
   never kept up, the pipeline carried 27,786 pages of backlog *before* any
   injection, and the run ended 3.44 M master rows against 1.68 M on the slave.
   The mechanism questions were settled; "what does 200 ms cost" was not
   ([`../findings/replication-lag.md`](../findings/replication-lag.md)).

## 1. Two kinds, and conflating them is the same error as §1 of the fault vocabulary

| Kind | What it saturates | What it produces | The scenario that needs it |
|---|---|---|---|
| **db** | the master's transaction path | replication backlog, apply cost, fail counts | lag, bulk-load apply delay, `to_be_active` that cannot drain |
| **host** | the node's CPU and I/O | heartbeat responses missing their window | the failover loop, the switchover threshold |

They are not two intensities of one thing. A db load can run at any volume
without delaying a heartbeat, because the heartbeat is a separate process
exchanging small packets; and a compile with no database traffic at all can
trigger a failover. [`04-faults.md`](04-faults.md) §1 separates events from
conditions for the same reason: two things with one name produce scenarios that
pass for the wrong reason.

So the profile, not a scale factor, is the first argument.

## 2. Shape

```
csb load start [--profile insert] [--rate 2000/s] [--concurrency 4] [--batch 200]
               [--for 60s] [--table t] [--seed 42] [--node master] [--require-rate]
csb load stop
csb load status
```

**`--batch` separates volume from rate, and the two are reported separately.**
The rate contract counts *statements*; a statement can carry many rows. Measured
on the first implementation: 281 statements/s single-row is the ceiling, because
each one is a process the driver spawns, and 100 statements/s at `--batch 200`
is **14,141 rows/s** — enough to build a replication backlog, which single-row
inserts at that ceiling are not. A driver that reported one number for both could
not say which of them it had failed to hold.

`load` is the sixth noun ([`01-cli.md`](01-cli.md) §1), because it is a thing a
user holds in their head separately from the cluster, the faults and the
inspector — it has a lifecycle, it can be running or not, and asking "what is
the load right now" is a question with an answer.

It is **not** a fault verb. A load is not an anomaly; it is the condition under
which anomalies become interesting, and half the fault vocabulary is only
meaningful with one running.

## 3. Rate control is the whole point

The driver targets a rate, holds it, and **reports whether it held it.**

```json
{ "profile": "insert", "requested": "2000/s", "achieved": "1180/s",
  "held": false, "notes": [{"code": "load_rate_not_held", ...}] }
```

This is [`README.md`](README.md) principle 4 — never report a number its source
cannot support — applied one layer earlier. A driver that silently falls behind
does not merely under-load the cluster: it makes every figure measured during
that window a figure about *the driver*, and the phase-0 run is the proof. An
open-loop driver cannot even tell you that happened.

Three consequences:

- **`--require-rate` makes a miss an error** (exit 1,
  [`01-cli.md`](01-cli.md) §6) rather than a footnote, for the runs where the
  rate is a premise instead of an observation. Measured: asking for 500/s and
  getting 85.6/s exits 1 and says so in a note.
- **A run whose load did not hold its rate is marked invalid in the record**,
  with the reason, and the record does not leave the reader to infer it
  ([`07-record.md`](07-record.md) §4).
- **Saturation is a legitimate target, and it is requested explicitly**
  (`--rate max`). The failure to distinguish "I asked for saturation" from "I
  asked for 2000/s and got saturation" is what makes phase 0's numbers
  unusable.

## 4. Determinism, and what cannot be made deterministic

`--seed` fixes the key sequence and the value padding, so two runs of the same
profile write the same rows in the same order. Combined with `describe`
([`02-topology.md`](02-topology.md) §4) that is the whole input to a run: same
cluster, same load spec, same result, or the sweep is comparing two different
experiments and calling the difference a threshold.

What cannot be fixed is recorded instead — wall-clock timings, the node's CPU
share, the driver's own cost (§6). The record carries them because the field's
stalled test could not say which of three explanations it was looking at, and
"the machine was busier that afternoon" was one of the three.

## 5. Profiles

| Profile | Kind | What it does | Why it exists |
|---|---|---|---|
| `insert` | db | PK-ordered inserts, one table, padded rows | the shape that produced every lag figure this project has measured; keeps the comparison to phase 0 meaningful |
| `update` | db | in-place updates of existing rows | grows the *apply* cost without growing volume, which separates the two stages the pipeline reports separately |
| `mixed` | db | insert / update / delete in stated proportions | the shape a real application has; also the traffic `fault failcount` needs running against it, since that verb's damage is a *delete* the slave cannot apply ([`04-faults.md`](04-faults.md) §8) |
| `bulkload` | db | `loaddb` against the master | the field has a written reproduction of a bulk load outrunning the applier; it is a named case, not the general driver |
| `host-cpu` | host | N busy threads inside the node container | the field's failover-loop recipe, which is a compile |
| `host-io` | host | sustained writes to the node's database directory | the other half of a build: disk contention, which is also what `ha_check_disk_failure_interval` watches |

**`host-*` profiles are bounded or they mean nothing.** "Saturated" on a
32-core host and on a 4-core CI runner are different experiments. The node
container is given an explicit CPU quota and the profile states its saturation
ratio — *N threads against M cores* — so a reproduction on another machine is a
reproduction rather than a coincidence. That quota is part of the topology, not
of the load, and travels in `describe`.

## 6. Where it runs, and the cost it cannot hide

The driver runs **inside the node containers**, because a host-side driver
competes with the wrong cgroup: it would starve the engine only incidentally,
and a `host-cpu` profile that does not share the engine's CPU quota does not
reproduce the field's condition at all.

**Which node depends on the profile, and that distinction was learned the hard
way.** A `db` profile is a client's workload and belongs on a client node when
there is one (§7). A `host` profile is not a workload at all — it exists to
squeeze the engine's own cgroup — so it runs on the database node whatever else
the cluster has. Moving every profile to the client left `host-cpu` burning a
client's quota and the engine's untouched, which is the exact thing the sentence
above says does not reproduce anything.

The honest consequence is that a db driver consumes the resources it is
measuring — `csql` processes on the master are CPU the master does not have. The
design does not pretend otherwise: `load status` reports the driver's own CPU and
memory alongside the achieved rate, and the record keeps them. A figure that
cannot be separated from the driver is better published with the driver's cost
next to it than published alone.

**Alternative rejected: the user brings their own load.** It is the cheaper
design and it is the one that has already failed. A threshold measurement whose
load is unspecified is precisely the state that left the field's
hidden-parameter test open for four years, unable to say whether an 8–11 second
role change was the engine, the parameter, or the afternoon. `loaddb` survives as
a profile because a specific field case is written against it; "run whatever you
like against it" does not survive as a contract.

## 7. What load adds to the artifacts

`describe` gains a `load:` block — the spec in force, not the achieved rate —
for the same reason it carries `faults`: a cluster reproducing a bug under load
is not the same cluster ([`02-topology.md`](02-topology.md) §4). The achieved
rate belongs to the run, not to the topology, and lives in the record
([`07-record.md`](07-record.md)).

## 7. Latency, and what it is the latency of

`load status` reports `p50`, `p90` and `p99` in milliseconds alongside the rate,
because a workload that holds its rate can still be unusable and the rate alone
cannot say so. Measured during a failover under 40 statements/s: `p50 15.7 ms`
and **`p99 4220 ms`** — four seconds at the tail while the median barely moves,
which is what a client actually experiences when a role changes and is invisible
in every other figure this tool reports.

Three things it is careful about.

**It is per statement, not per row.** With `--batch` one statement carries many
rows, and the two are different questions — the same care the rate contract
already takes.

**It includes the cost of the client.** Each statement is a `csql` invocation, so
starting the client is in the number. That cost is real for this driver and is
reported separately as `driver_cost` rather than subtracted from a figure
somebody might quote.

**It is absent below twenty samples.** A percentile from three measurements is
not a percentile, and publishing one would be the same class of lie as a lag
figure with no source. Every sample is kept rather than reservoir-sampled, up to
a cap, and `latency_complete` says whether the distribution is all of them.

## 8. Several clients, one rate

`--rate` is the **aggregate target across every driver**, not a figure the cluster
reports about itself. With N clients it is divided among them, each driver is
started with its share, and both figures are reported — the total and
each client's — because "the load" and "what this client managed" are different
questions.

**Each client owns a disjoint range of the key space.** An interleave was tried
first and was wrong in a way worth recording: every driver read `MAX(i)` at a
different moment, so their offsets were relative to different origins, and the
first two-client run produced **146 unique-constraint violations out of 1025
statements**. A range needs no coordination between the drivers, survives a
restart, and is bounded — CUBRID's `INT` is 32-bit and each client takes
100,000,000 keys, so twenty-one clients is the limit. That is a number worth
stating rather than discovering.

**The distributions are not merged.** Each client reports its own; a percentile
of percentiles is not a percentile, and the samples that would make a real one
live on separate machines. The total line carries the rate, the count and the
errors, which do add up.

### What the second client immediately showed

With one client the driver held 19.8/s of 20/s. With two asking for 20/s each it
held **11.2/s each**, and the tail went from `p99 230 ms` to `p99 1244 ms` — with
**no errors**, so nothing was failing. The limit is the driver, not the engine:
it spawns a `csql` process per statement, and that cost does not divide.

This is a real ceiling on using this driver for performance work, and it is why
a client node takes a `--tools` directory. A workload that needs a rate this
driver cannot reach belongs in a tool built for it — `sysbench`, a JDBC
application, the site's own harness — running on the same client node, against
the same broker, recorded in the same `/results`. **The tool's job is to provide
the place, not to become the benchmark.**

## 9. `load` is a demo client with a shortcut attached

```
csb load driver [--out mine.py]
```

The built-in driver is unremarkable — it opens a connection, paces statements,
writes a status file — and it is not trying to be a benchmark. It cannot be one:
it spawns a `csql` per statement, which is a ceiling measured at about 20
statements a second per client (§8).

**What is worth seeing is not the driver but how it gets there.** The tool copies
this program into the node's own directory and runs it with `python3`, and a
client node's `/tools` is the same route without the copy. So the verb exists to
stop `load` looking like a feature when it is an example:

```
csb load driver --out mine.py            # here is the program I run
# edit it, or throw it away and write your own
csb cluster create --clients 1 --tools ./my-tools ...
csb node exec client -- python3 /tools/mine.py
```

`/tools` is yours and read-only, `/results` is writable and outlives the cluster,
and the broker answers at `<node>:33000` from inside with no port published on
your machine (`02-topology.md` §8).

That leaves the built-in `load` with two honest jobs and no third one. **Making
the cluster busy** while something else is being measured — where what matters is
that replication is moving, not that a rate was achieved — and **squeezing the
engine's own cgroup** with the `host` profiles, which are contention rather than
workload and run on the database node for that reason (§6). Anything that is
really a performance question belongs to a tool built for it, on the client node,
by the same path this verb shows you.
