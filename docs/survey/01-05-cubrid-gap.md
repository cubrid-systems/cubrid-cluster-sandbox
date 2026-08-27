---
title: CUBRID — Gap Inventory and Measurement Plan (Survey 05)
category: survey
project: cluster-sandbox
status: selected
lang: en
sources:
  - ../DESIGN.md §1 (the measured CBRD-26983 two-node assembly, 2026-08-18)
  - 01-01-postgresql.md · 01-02-mysql.md · 01-03-mongodb.md · 01-04-tidb.md
  - src/connection/server_support.c:1558 (`css_change_ha_server_state` — a non-heartbeat caller cannot drive active→standby), :1594-1612 (state transition table)
  - src/object/schema_system_catalog.cpp:111 (`db_ha_apply_info` catalog view)
  - src/executables/utility.h:1599-1602 (`cubrid statdump` has no output-format option), :1634-1646 (`cubrid applyinfo` options)
  - src/connection/heartbeat.h:62-70 (`HB_PTYPE_SERVER` / `HB_PTYPE_COPYLOGDB` / `HB_PTYPE_APPLYLOGDB`)
  - src/executables/master_heartbeat.c:866-895 (split-brain diagnosis → `HB_CJOB_FAILBACK`), :1042-1054 (ping-check cancel conditions), :1110-1135 (their three messages)
  - src/object/schema_system_catalog_install.cpp:1956-1988 (`db_ha_apply_info`, twenty-six columns)
  - src/executables/util_cs.c:3893-3924 + src/transaction/log_applier.c:7456-7478 (`applyinfo`'s two delay figures, and the zero-rate first sample)
  - CUBRID/cubrid-contrib — sandbox/, docker_for_ctp/
summary: Where CUBRID stands on the five axes, what is missing, and how to measure each gap before building. Eleven gaps, of which only one (G7, engine-internal metrics) is blocked on another project — G1-G7 are assembly work whose shape the comparable set already settled, and G8-G11 (added 2026-08-27) are the anomaly and observability set, where the comparable answer is mostly absent and the CUBRID evidence came from reading the heartbeat and the log applier. The measurement plan is deliberately cheap: the baseline is the assembly already performed for CBRD-26983, and each gap's check is a re-run of that assembly with one piece automated.
created: 2026-08-18
updated: 2026-08-27
tags: [roadmap, survey, cluster-sandbox, cubrid, gap-analysis, measurement, ha, provisioning, replication, fault-injection]
---

**Contents:**

- [1. CUBRID today, on the five axes](#1-cubrid-today-on-the-five-axes)
- [2. Gap inventory](#2-gap-inventory)
- [3. Measurement plan](#3-measurement-plan)
- [4. Prerequisite order](#4-prerequisite-order)
- [5. What the comparable set settles, and what it leaves open](#5-what-the-comparable-set-settles-and-what-it-leaves-open)

## 1. CUBRID today, on the five axes

| Axis | CUBRID | Evidence |
|---|---|---|
| D1 topology | Nothing. Hand-written `cubrid_ha.conf` (`ha_node_list`, `ha_db_list`) plus a `ha_mode=on` `cubrid.conf` per node | `../DESIGN.md` §1 |
| D2 artifact source | An install tree. A locally built tree works unmodified in a stock `ubuntu:24.04` container — CUBRID binaries need only libc / libm / libgcc_s / libstdc++ beyond what the tree ships | measured 2026-08-18 |
| D3 isolation | Nothing provided. The measurement used Docker containers on a user-defined bridge | measured |
| D4 fault verbs | None. `cubrid heartbeat stop` takes the server down with it, and `cubrid changemode` cannot drive active→standby because `css_change_ha_server_state` ignores that request from a non-heartbeat caller (`server_support.c:1558`) | code + measured |
| D5 observability | Tier 2 material exists but unassembled: `db_ha_apply_info` is a **catalog view** (SQL-readable), plus `cubrid changemode`, `cubrid heartbeat status`, `cubrid applyinfo`. Tier 3 is text-only — `statdump` has no output-format option | code |

## 2. Gap inventory

**G1 — No topology declaration.** Two config files per node, written by hand,
with the node list duplicated across them. *Comparable answer*: named presets
plus a count (`01-02` §4 I1, `01-03` §4 I2) — CUBRID's near-term catalogue is
smaller than MongoDB's, so no schema is needed yet.

**G2 — Slave construction is a manual chain.** `backupdb` → fix file
permissions → copy to the other node → `restoreslave -s master -m <host>`.
*Comparable answer*: PostgreSQL's harness compresses the identical chain into
`backup` / `init_from_backup` / `enable_streaming` (`01-01` §1), which confirms
the chain is inherent to physical replication and only the *interface* is
missing.

**G3 — Start ordering and a reactivation trap.** `service start` must precede
`heartbeat start`; and after `heartbeat stop`, `heartbeat start` alone fails
with *"CUBRID heartbeat feature is being deactivated"* and needs a full
`service stop` / `service start`. Both were discovered by hitting them.

**G4 — No fault verbs, and demotion is not what it looks like.** Failover had
to be induced with `docker network disconnect` and `pkill cub_master`.
Separately, the measurement established that a demotion **replaces the server
process**: the heartbeat logs `[Failback] [Success] Current node has been
successfully demoted to slave` and 10 ms later `Process failure detected
(pid:102, args:cub_server aitest)`. Any verb set must therefore distinguish
*graceful stop*, *crash*, and *partition*, because in CUBRID they produce
genuinely different engine behaviour — the first runs the shutdown flush, the
others do not.

**G5 — Node addressing has no stable handle.** The master had to be found by
reading `cubrid changemode` output before it could be acted on, and after
failover the same command pointed at the other node. *Comparable answer*:
MongoDB's role tags (`01-03` §4 I1); the survey's verdict against pid
addressing is unanimous (`01-04` §4 I3).

**G6 — Split-brain avoidance is off by default in a fresh setup.** With
`ha_ping_hosts` unset the cluster logs `[Failback] [Cancelled] No hosts are
registered in ha_ping_hosts` and a partition is never diagnosed. A provisioner
that writes the config can set this correctly by construction.

**G7 — Engine-internal metrics have no machine-readable path.** The only gap
blocked on other work: `statdump` is text, CMS already parses it and narrows
64-bit counters to 32, and `cubrid-exporter` died in 2020 against statements
the engine does not have. *Comparable answer*: TiDB ships Grafana with a
playground because its components already expose metrics endpoints (`01-04`
§4 I1) — the difference is the contract, not the provisioner.
The four below were added on 2026-08-27, from the requirement set in
`../DESIGN.md` §1. They differ in kind from G1–G7: those are assembly work
whose shape the comparable set settled, while these are about *states* the
assembly does not produce, and the comparable set mostly has no answer.

**G8 — Lag can be caused but not requested.** CUBRID's replication pipeline is
two heartbeat-managed processes, `HB_PTYPE_COPYLOGDB` and `HB_PTYPE_APPLYLOGDB`
(`heartbeat.h:62-70`), and the engine already reports their delays separately —
`applyinfo` prints "Delay in Copying Active Log" and "Delay in Applying Copied
Log" from two different LSA pairs (`util_cs.c:3893-3924`). Nothing induces
either. The two candidate mechanisms are not equivalent: delaying the container
network is realistic and stage-agnostic, suspending one of the two processes is
precise and is the only way to separate the stages. **Both were run 2026-08-27**
(`../DESIGN.md` §9 OQ7): suspension is the default because the heartbeat
does not interfere with it — it monitors process existence, not progress, and
left both suspended processes listed as `registered` with no log line — while
`netem delay 200ms` grew the lag by roughly 15,000 pages in 30 s without saying
which stage it slowed. *Comparable answer*: none —
the same hole as the partition verb, and for the same reason (`01-00` §5.1 DI1).
Process-isolated tools cannot delay a link they do not have.

**G9 — Split brain is the default outcome, not an inducible one.** The ping
check decides failback for a master and failover for a slave in one function,
and its cancel conditions are asymmetric: a master cancels failback when
`ping_try_count == 0` *or* the ping succeeded, a slave cancels failover only
when it tried and failed (`master_heartbeat.c:1042-1054`, messages at
`:1110-1135`). Two routes therefore end with two masters. With `ha_ping_hosts`
unset — the default, and what the 2026-08-18 session hit — the master cancels
for want of a ping host and the slave, with nothing to fail, promotes. With
`ha_ping_hosts` pointed at a third host that survives the partition, the master
concludes "not a network partition" and stays, and the slave pings successfully
and promotes. **Both routes were run on 2026-08-27** and both produce
two masters — the correctly configured one in **9 seconds**, the default in 13
(`../DESIGN.md` §9 OQ9; harness `../findings/split-brain.md`). A control arm
that cut the master from its ping host as well demoted the master cleanly, so
the mechanism is exactly what the code says. The gap is therefore neither
induction nor a configuration deviation: it is inducing a *chosen* flavour on
request, asserting on the **cancel reason** rather than on the outcome (the two
flavours are indistinguishable by outcome), and ending it. Ending is the
engine's job after a split brain — detecting `num_master > 1` queues an
automatic failback (`:866-895`), measured at under 45 s — but **not** after a
clean failover, where nothing returns the cluster to its original master. That
half is G10's.

**G10 — Failback's manual half is undocumented and unowned.** The engine's
failback needs no operator: the diagnosis queues `HB_CJOB_FAILBACK` directly
(`:866-895`). So whatever the technical team does by hand for a real cluster is
by definition the part the engine does not do — judging when it is safe,
handling the divergence on the node that was wrong, ordering the restarts — and
none of it is written down anywhere this project can read. *Comparable answer*:
thin. One of the four legs records a promote primitive at all (PostgreSQL's
harness, `01-01` §3), MySQL's tool has neither promote nor partition
(`01-02` §4), and not one of the four documents a return-to-original-master
procedure. The gap is closed by asking, not by surveying — and the 2026-08-27
run sharpened what to ask. The control arm of OQ9 failed over cleanly and then
**stayed** failed over: 45 s after the network healed the roles were still
swapped, and they stay that way, because with a single master nothing triggers.
So the engine's `[Failback]` means "demote myself, another master exists", and
the operational return trip has no engine path at all. `failback.sh` in the
harness encodes this project's guess at that trip — five decision points, each
one stating what we do not know — and is the artifact to send. **It was run end
to end the same day** and restored the original master in a two-node pair with
no row loss, so the mechanism is not the gap; the judgement is. Running it also
found that `cubrid heartbeat stop`, the command the switch depends on, hangs
indefinitely when the node's HA processes cannot be reaped — *after* the
deactivation has already succeeded — so the step has to be bounded and judged on
the observed roles rather than on the command's exit
(`findings/failback.md`).

**G11 — Replication state is readable but not retained.** `db_ha_apply_info` is
twenty-six columns over SQL — six LSA pairs, three progress timestamps, six
counters including `fail_counter` (`schema_system_catalog_install.cpp:1956-1988`)
— which was assumed to be a good point sample. **Running it on 2026-08-27 showed
it is not one.** The row is written by `applylogdb`, so a suspended applier
freezes every column — `eof_lsa` included, though copying never stopped — and
the view reports a constant, healthy-looking lag for as long as the stall lasts.
Worse, during a `copylogdb` stall the applier drains the on-disk backlog and the
reported lag **falls** (49,544 → 38,576 pages) while replication is entirely
stopped. `cubrid applyinfo -r <master>` saw the same moment correctly, at 48,343
pages behind. And there is a third state, found while running the failback script: across a
**role change** the view is not wrong but *absent* — a just-demoted node has no
row until its applier writes one, and both failback runs printed `<none>` at the
step that asks whether the target is caught up, which is the only moment that
question is ever asked. So the gap has three halves: the view needs a
**master-side reference** before it is a lag measurement at all; a missing row
must not read as zero lag; and then nothing keeps a series, so "when did the
slave start falling behind, and on which stage" is unanswerable unless someone
was already watching. `cubrid applyinfo -i` does
hold a loop, but it prints (`log_applier.c:7456-7478`) and its first sample
always reports `-` because `process_rate` is zero until a second iteration
(`util_cs.c:3893-3903`) — so it is a watch, not a record. *Comparable answer*:
TiDB's Grafana (`01-04` §4 I1), which is retention over an existing metrics
contract; the CUBRID equivalent needs no contract, because the source is a
catalog view.

## 3. Measurement plan

The baseline exists: the CBRD-26983 assembly was performed twice on
2026-08-18, so "how long, how many steps, how many traps" is answerable
without new work. Each gap's check is that same assembly with one piece
automated.

| Gap | Measurement | Pass condition |
|---|---|---|
| G1 | Steps from empty directory to two nodes running, before vs after | one command, one config input |
| G2 | Slave construction step count and manual interventions (the permission fix is one) | zero manual interventions |
| G3 | Fresh-machine run by someone who has not done it before | no ordering knowledge required |
| G4 | Reproduce the three scenarios (clean stop / crash / partition) that the CBRD-26983 verification ran by hand | one verb each; the id sequences reproduce (`1,2,21,22,41,42,61`) |
| G5 | Same scripts run before and after a failover without edits | role selectors resolve to the current holder |
| G6 | Fresh cluster's heartbeat log after a partition | partition diagnosed, no `[Failback] [Cancelled]` |
| G7 | Deferred — nothing to measure until a contract exists | — |
| G8 | Induce lag by each mechanism (network delay; stage suspension) and read both delay figures | the two stages are separately reachable and the delay reverses on heal without a restart — **MEASURED 2026-08-27, pass on the mechanism**: suspension is stage-selective and the heartbeat does not interfere. The "shows in `db_ha_apply_info`" clause **failed** and moved to G11 |
| G9 | Partition once with `ha_ping_hosts` unset, once with a ping host reachable from both | two masters in both cases; healing logs `[Failback] [Diagnosis] Multiple master nodes` — **MEASURED 2026-08-27, pass**: 9 s and 13 s, heal demoted inside 45 s, plus a control arm that demoted cleanly |
| G10 | Hand the script to the technical team | it comes back marked up — count the steps they changed, added, or refused — **script written *and validated* 2026-08-27 (`failback.sh`, `rc=0`, original master back in 2 s, no row loss); not yet sent** |
| G11 | After a lag episode has ended, ask when it began and which stage it was | answerable from what was retained, by someone who was not watching — **amended 2026-08-27**: the pass condition now also requires the retained series to be *true*, i.e. to carry a master-side reference; a series built on `eof - final` alone records a *falling* lag through a total copy stall |

Two properties are worth measuring for their own sake because they decide
adoption: **time from command to a query-serving cluster**, and **whether a
second person can reproduce a topology from an artifact** (`01-03` §4 I3 —
`mlaunch list --startup` as the model).

## 4. Prerequisite order

```
container substrate (verified: stock image + bind-mounted build tree)
        │
        ├──> G1 topology + G2 slave chain + G3 ordering ──> G4 fault verbs ──> G5 role addressing
        │                                                        │
        │                                                        ├──> G8 lag, stage-targeted
        │                                                        │        (needs heal semantics)
        │                                                        └──> G9 split-brain induction + heal
        │                                                                 ▲
        ├──> G6 config correctness by construction ───────────────────────┘
        │        (the deviant config is one of G9's two routes)
        │
        └──> D5 tier 1 + tier 2 monitoring ──> G11 retention
                 (db_ha_apply_info over SQL)      (so a lag episode is traceable afterwards)

G10 failback script ──> technical team markup ──> requirement set
        (depends on nothing above; its output is an input to G4's and G8's verb shape)

N20 utility JSON output ──> N64 W1 operational contract ──> G7 tier 3 metrics
```

The left branch has no external dependency. G7's branch is entirely outside
this project and should be built as a *seam*, not a collector (`01-04` §4 I1).
G10 is the one item that can start immediately and out of order, and should:
it needs none of the tool, and it is the only way to learn what the people who
perform failback actually require before the verb set is fixed
(`../DESIGN.md` §9 OQ8).

## 5. What the comparable set settles, and what it leaves open

**Settled.** Topology as named presets rather than a schema (G1). Node
addressing by role (G5). A kill-versus-stop distinction as the minimum fault
pair (`01-02` §4 I2). Local build path as an ordinary first-class argument —
three of four systems document it (`install_path`, `--binarypath`,
`--{comp}.binpath`). Bundled tier-3 monitoring is a consequence of the engine's
metric contract, not of the provisioner (G7).

**Left open, and now sharper.**

- **Network faults have no precedent here.** Not one of the four ships a
  partition verb, because all four chose process isolation. CUBRID needs the
  verb — failover was induced by cutting a link — so the container substrate is
  a *requirement*, not a preference, and `cluster-sandbox` will be designing
  this verb without a model to copy.
- **Where the primitives live (`../DESIGN.md` §9 OQ3).** PostgreSQL puts
  them in the test harness and never surfaces them; MySQL and MongoDB put them
  outside the server entirely; TiDB puts them in vendor tooling. The precedent
  is genuinely split, so the CUBRID answer has to come from the
  `cubrid-testkit` boundary rather than from the survey.
- **The legs were never asked about lag or split brain.** The series' fault
  question (`01-00` §3 D4) covered promote / demote / partition / kill / rejoin
  and stopped there, so the four legs carry no evidence on latency injection or
  on inducing two masters. G8 and G9 are therefore recorded here without a
  comparable answer, which is a hole in this survey rather than a finding about
  the comparable set. A targeted re-read — MongoDB's bridging proxy and TiDB's
  chaos tooling are the obvious first two — is queued and would change §5.1's
  matrix rather than any decision already taken.
- **Sharing a substrate with production (`§9 OQ4`).** TiDB's one-family model
  is attractive, but CUBRID's production half already exists as
  `cubrid-operator`. The question narrows to whether the topology model is
  shared with the CRD — deferrable without blocking a Docker-only release.
