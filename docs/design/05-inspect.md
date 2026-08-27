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
cubrid changemode <db>            active | standby | maintenance, per node
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
2. **Copy progress requires a master-side reference.** The tool reads the
   master's append position itself — the reference `cubrid applyinfo -r` uses —
   and computes the difference. It does not parse `applyinfo`'s output: that is
   `printf` text, and its first sample always prints `-` because `process_rate`
   is zero until a second iteration (`log_applier.c:7456-7478`).
3. **Every figure carries its sample time and its source.** A row `applylogdb`
   wrote four seconds ago is reported as four seconds old, because during an
   apply stall a stale row looks perfectly healthy.
4. **Absence is `null` with a reason, never zero.** `no_master_reference`,
   `stale_apply_info`, `no_apply_info_row` are note codes
   ([`01-cli.md`](01-cli.md) §4), and each corresponds to something that was
   observed.

This is the finding with the widest blast radius, and it is not only this
project's problem: any CUBRID monitoring product that publishes `eof - final` as
"replication delay" shows an operator a falling number during an outage. It has
been handed to the organization's CUBRID Ops work as an input to its metrics
contract, which needs the master's append position in it and not only the
slave's applied position.

## 4. `repl watch` — retention, and why

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

## 5. `cluster status`

One command, the whole topology, T1 and T2 together:

```
NODE       LIVE  HA ROLE   SERVER                    COPY      APPLY     FAULTS
hadb-n1    yes   active    registered_and_active     —         —         —
hadb-n2    yes   standby   registered_and_standby    0 pages   1,063 p   lag(apply)
```

`—` for the master's replication columns is deliberate: a master has no
replication position to report, and printing `0` there would be the same class of
lie as §3.
