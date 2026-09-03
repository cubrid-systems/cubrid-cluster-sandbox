---
title: cluster-sandbox — Inspection
category: design
project: cluster-sandbox
summary: What the inspector reads and what it is forbidden to claim. Three tiers, and one hard rule that came out of measurement — db_ha_apply_info cannot measure replication lag on its own, because it is written by the process it would be reporting on.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# Inspection

Layer 5 of [`../DESIGN.md`](../DESIGN.md) §4. Of the five layers this is the one
that running the design *corrected* rather than confirmed, so most of this
document is about what the inspector may not say.

## 1. Tiers

| Tier | What it shows | Source | Cost |
|---|---|---|---|
| **T1 container** | process liveness, stdout/stderr, CPU/memory/disk per node | the container runtime and the engine's own log files | free |
| **T2 topology** | HA role per node, group membership, replication position | `cubrid changemode`, `cubrid heartbeat status`, `db_ha_apply_info` over SQL, plus a master-side reference (§3) | small |
| **T3 engine internals** | buffer/lock/log/SQL counters, wait events, statement statistics | none that is machine-readable today | blocked |

T3 is a **seam**, not a component: a documented attachment point for a collector
once the engine has an output contract. Nothing in this project parses
`statdump`. The organization's CUBRID Ops work owns that contract; building a
second collector here would be building the wrong half of it
([`../DESIGN.md`](../DESIGN.md) §3, §9 OQ2).

## 2. What T2 reads

```
cubrid changemode <db>            active | standby | to-be-active | maintenance
cubrid heartbeat status           group membership, priorities, process registry
SELECT … FROM db_ha_apply_info    replication position on a standby
```

`db_ha_apply_info` is a catalog view of **twenty-six columns** — six LSA pairs
(`committed`, `committed_rep`, `append`, `eof`, `final`, `required`), three
progress timestamps (`log_record_time`, `log_commit_time`, `last_access_time`),
and six counters of which `fail_counter` is the one that separates *broken* from
*behind* (`schema_system_catalog_install.cpp:1956-1988`). It is SQL, it is
stable, and it needs no output-format contract. That is why the design chose it.

## 3. The rule: never report a lag the source cannot support

**`db_ha_apply_info` alone is not a lag measurement.** Three measured ways it
misleads, each in a different direction:

The tool can say all three, because it is the thing that caused them: when a
lag condition is in force on a node, `repl status` and `cluster status` mark that
node's figures `stale_apply_info` at severity `error` rather than printing them
as a measurement. Demonstrated on demand — a suspended applier reports a constant
`lag=13` for as long as the stall lasts, and clearing it moves the figure to
`lag=2718` in one step.

**It freezes.** The row is written by `applylogdb`. Suspend the applier and
*every* column stops moving — `eof_lsa` included, although copying never
stopped — so the view reports a **constant, healthy-looking** lag for as long as
the stall lasts:

```
before          eof 40419  final 12633  lag 27786
+15 s stopped   eof 40419  final 12633  lag 27786
+30 s stopped   eof 40419  final 12633  lag 27786
+5 s resumed    eof 71143  final 16288  lag 54855      ← the truth, all at once
```

**It falls.** During a *copy* stall the applier keeps draining the backlog
already on disk, so the reported lag **decreases** while replication is entirely
stopped: 49,544 → 38,576 log pages. `applyinfo -r <master>` read the same moment
correctly at 48,343 pages behind.

**It is absent.** A just-demoted node has no row at all until its applier writes
one — so the check an operator most needs during a failback is empty exactly
when they need it ([`../findings/failback.md`](../findings/failback.md)).

### What follows

1. **`repl status` reports two stages, never one number.** Copy progress and
   apply progress are separate figures with separate provenance. There is no
   field called `delay`.
2. **Copy progress requires a master-side reference — and it is read from
   `applyinfo -r`, which amends what this document used to say.** The intent was
   for the tool to read the master's append position itself rather than parse
   that output. The engine exposes it nowhere else: `db_ha_apply_info` is the
   only HA catalog view and it describes the applier rather than the log, so
   there is no SQL for it. The tool therefore parses **one labelled line**,
   `Append LSA`, and nothing else.

   The original objection stands and is narrower than it read: it was about
   `Estimated Delay`, which prints `-` on a first sample because `process_rate`
   is zero until a second iteration (`log_applier.c:7456-7478`). That field is
   still not read. Reading a labelled integer is not the same as trusting a
   derived estimate, and the alternative to reading it is not reporting the copy
   stage at all — which is what the tool did until this reference existed.

   Measured, and it is what the second source buys. Under load, with the applier
   suspended: `apply_lag` freezes at **3** while `copy_lag` grows **1705 → 3499 →
   5188**. With the copier suspended: `apply_lag` reads **0** — the reassuring
   direction — while `copy_lag` grows **7192 → 8787 → 10482**. Resuming the
   copier moves the backlog across the stages in one step, `copy_lag` 14,911 to
   0 and `apply_lag` 0 to 12,416, which is a transition a single "delay" number
   cannot express.
3. **Every figure carries its sample time and its source.** A row `applylogdb`
   wrote four seconds ago is reported as four seconds old, because during an
   apply stall a stale row looks perfectly healthy.
4. **Absence is `null` with a reason, never zero.** `no_master_reference`,
   `stale_apply_info`, `no_apply_info_row`, `ambiguous_apply_info` are note codes
   ([`01-cli.md`](01-cli.md) §4), and each corresponds to something that was
   observed.

### And the sources have defects of their own

The three above are about what `db_ha_apply_info` *means*. The field's tracker
adds a fourth category, independent of meaning: the readings are sometimes
simply wrong.

| Reported | Source | Defect |
|---|---|---|
| 2024 | `db_ha_apply_info` | **two rows** for one database, after `ha_copy_log_base` changed without the old row being removed |
| 2025 | `cubrid applyinfo` | output malformed once a dba password is set |
| 2025 | `cubrid hb status` | a replica's HA-Process Info state displayed wrongly |
| 2022 | `cubrid hb status` | the manual's description of the applylogdb/copylogdb fields was itself wrong |

The duplicate-row case is the load-bearing one: a reader that assumes one row
per database silently takes whichever it is handed. **T2 counts rows and reports
`ambiguous_apply_info` rather than choosing one**, which is the same rule as §3
applied one level down — do not report a figure whose provenance is ambiguous
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §7).

This is the finding with the widest blast radius, and it is not only this
project's problem: any CUBRID monitoring product that publishes `eof - final` as
"replication delay" shows an operator a falling number during an outage. It has
been handed to the organization's CUBRID Ops work as an input to its metrics
contract, which needs the master's append position in it and not only the
slave's applied position.

## 4. `repl check` — a write that has to arrive

```
csb repl check [<selector>] [--wait 30s] [--table csb_canary]
```

Everything above this line reads a gauge. This writes a marker on the master and
waits for it on the standby, and it is here because **the field verifies a
rebuilt slave exactly this way** — `applyinfo -r … -a` alongside a `repl_test`
table they create and insert into
([`../requirements/01-failback-field-evidence.md`](../requirements/01-failback-field-evidence.md) §4).
They do not read a threshold off a gauge; they ask the pipeline to carry
something and watch it land.

The reason that is better judgement than instrumentation is §3. A suspended
applier freezes `db_ha_apply_info` at a constant, healthy-looking lag for as long
as the stall lasts, and a suspended copier makes the reported lag read zero. **A
row does not freeze.** It arrives or it does not, and the check reports which,
with the time it took.

Measured, on one cluster, minutes apart. Healthy: **arrived in 0.63 s**. With the
applier suspended, the gauge reads `copy=0 pages behind`, `apply_lag=0`,
`fail=0` — every number says the cluster is fine — and the row **does not arrive
in 15 s**. That screen is the argument for this verb.

**What it proves is that the path is open, and nothing more than that.** A
healed split brain leaves the two databases holding different rows — measured, one
direction only, five of six runs
([`../findings/active-active-window.md`](../findings/active-active-window.md)) —
and on that cluster the canary arrives, `apply_lag` is 0 on both sides and
`ha resync` correctly reports that replication is not broken. It is not: it is
carrying new writes fine. It simply never carried one old one. **A canary cannot
prove the two databases are the same**, and a healed split brain needs a
divergence check — row counts, or the comparison `ha resync --path table` makes
— rather than a lag check or this one.

Not arriving is **exit 4**, not 1: the write succeeded and the wait ran out,
which is a different fact from the tool being unable to do its job. The table is
created once and reused, because creating it per check would put a DDL through
replication every time and measure something else.

## 4a. `repl diff` — what the two databases actually hold

```
csb repl diff [--table t]
```

Everything above this line, the canary included, asks **replication** how it is
doing. This asks the **two databases** what they contain, and the two questions
have different answers.

A healed split brain left a standby permanently missing a row while `repl status`
read `apply_lag=0` and `fail=0` on both sides, `repl check` arrived, and
`ha resync` reported that replication was not broken
([`../findings/active-active-window.md`](../findings/active-active-window.md)).
Every one of those was true. Replication was carrying new writes fine; it simply
never carried one old one, and the engine keeps no view that remembers that.
Measured on one cluster, thirty seconds after the heal:

```
repl status  n2  apply_lag=0  fail=0
repl diff    w   master=3  standby=2  DIFFERENT
```

**The table list comes from the catalog, not from the applier's error log.** That
distinction is the whole verb. `ha resync` takes its list from failures, which is
the right source when something failed to apply — and a split brain fails
nothing, so the list is empty exactly when the divergence is largest.

**Row counts are a weak instrument and the verb says so.** Equal counts are not
equal data; they are what two databases can be asked cheaply without a
schema-aware comparison, and they are the field's own instrument for this. A
table missing on the standby counts as zero and differs, because that is the
largest difference there is rather than a table that could not be read.

A difference is **exit 1**, not 0: the command did its job and the answer is bad,
and a harness has to tell "compared and equal" from "compared and not" without
reading prose.

**Nothing will catch this up on its own.** The standby's recorded position has
already moved past the write it is missing — a canary written afterwards arrives
— so nothing will ever re-fetch it. Only a rebuild resets that bookkeeping, which
is why the field's closure is `ha_make_slavedb.sh` and why `ha resync` now
answers `slave` here instead of `resume`.

## 5. `repl watch` — retention, and why

```
csb repl watch [--interval 0.5s] [--for 60s] [--out FILE]
```

A point sample cannot answer "when did it start falling behind, and on which
stage", and that is the question a developer actually has after a run. `watch`
samples and retains; the series is scenario-scoped — bounded, local, discarded
with the cluster — which is what keeps it from being a second operational
collector.

The retained series carries both stages and both provenances, or it inherits
every problem in §3 with a timestamp attached.

**Measured 2026-09-03.** With `fault lag --stage apply --mechanism suspend` in
force, a ten-second series at one sample a second read
`copy 0→16 (max 16) rose at +1.3s` against `apply 0→0 (max 0)`. The applier's own
view sat flat and healthy for the whole window because the process that writes it
was suspended; the copy stage, which has a master-side reference, showed the
truth. The series does inherit §3's lie — and the tool says so at the top of the
window rather than letting a flat line speak for itself, because it knows which
process it suspended.

`rose_after_seconds` is the retained answer to the question this section names:
not the maximum, which a point sample reaches eventually too, but **when it
started**. A stage that never moves reports no rise rather than inventing one.

## 6. `cluster status`

One command, the whole topology, T1 and T2 together:

```
NODE       LIVE  HA ROLE   SERVER                    COPY      APPLY     FAULTS
hadb-n1    yes   active    registered_and_active     —         —         —
hadb-n2    yes   standby   registered_and_standby    0 pages   1,063 p   lag(apply)
```

`—` for the master's replication columns is deliberate: a master has no
replication position to report, and printing `0` there would be the same class of
lie as §3.

**`to_be_active` is a fourth role, not a transition.** This design has treated it
as a window a few seconds wide — [`03-assembly.md`](03-assembly.md) T5 waits it
out. The field has seen a node hold it from **01:00 to 09:00**, live, holding the
service, and refusing every write, because a wrong `db_ha_apply_info` row sent
the applier looking for an archive log that had been deleted. The
promotion completes only when `applylogdb` reaches the `dead` record `copylogdb`
writes on detecting the peer's death, so what an operator needs alongside the
role is **the applier's position and whether the log it wants still exists** —
and the engine will not shortcut it, deliberately: forcing `active` would apply
replication log over data written after the switch.

So `HA ROLE` carries `to_be_active` as a value of its own, and a node in it is
reported as neither healthy nor failed over. **`FAULTS` carries `quiesce(broker)`
the same way**, because "writes are blocked, by this mechanism" changes what
every other column means and a reader who cannot see it will read an idle cluster
as a healthy one ([`04-faults.md`](04-faults.md) §9). Reproducing the state is a scenario
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §3);
reporting it honestly is this layer's job.
