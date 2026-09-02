---
title: cluster-sandbox — Roadmap
category: roadmap
project: cluster-sandbox
summary: Phases, milestones and current position. Phase 0 is complete; phase 1 is the CLI, the container backend and the HA preset. The one thing blocking design decisions rather than implementation is what the technical team requires of the return to the original master — a question that has to be asked in the team's own vocabulary, because "failback" already means something else to them.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# Roadmap

Goals and their acceptance lines are in [`DESIGN.md`](DESIGN.md) §2. This file
says what order they get built in and where the project is.

## Where it is

**Phases 0, 1 and 2 are complete** — 0 on 2026-08-27, and 1 and 2 on 2026-09-02.

One command builds a two-node HA pair and reaches `serving`. The failure
scenarios are verbs addressed by role, and the scenario that started the project
replays through them unattended and reproduces its measured id sequence. The
cluster answers what state it is in without anything parsing human-formatted
text, and it says so when a figure's source cannot support it. A workload states
a rate and admits when it misses one. A run writes down what happened to it,
including both intervals for every role change, without being asked.

What that apparatus was for: the switchover threshold the field asked to have
validated in 2021 and could not measure has been measured
([`findings/switchover-threshold.md`](findings/switchover-threshold.md)).

Two things are outstanding and neither is blocked work. **M3.1** needs a consumer
that does not exist yet. And the failback script still needs one round of the
technical team's attention — see *Open* below.

## Phase 0 — baseline, spike, and the question for the technical team ✅

The honest floor: the manual assembly written down, so the tool has something to
beat, and the design's own uncertainties resolved by running them rather than by
reading more code.

| # | Item | State |
|---|---|---|
| M0.1 | The CBRD-26983 assembly measured by hand — steps, ordering constraints, traps | done 2026-08-18 |
| M0.2 | Comparable-engine survey and CUBRID gap inventory (`survey/`) | done 2026-08-18 |
| M0.3 | The assembly as a runnable script (`harness/lib.sh` `cs_up`) | done |
| M0.4 | Split-brain reproduction, three arms (`findings/split-brain.md`) | done 2026-08-27 |
| M0.5 | Lag injection, both mechanisms, seven phases (`findings/replication-lag.md`) | done 2026-08-27 |
| M0.6 | Failback script written and validated end to end (`findings/failback.md`) | done 2026-08-27 |
| M0.7 | Failback script reviewed by the technical team | **open** — see below |

## Phase 1 — the tool exists

G1, G2, G3, G4. A developer with Docker and a CUBRID build gets a two-node HA
cluster from one command, can break it in the three ways the CBRD-26983
verification needed, and can ask it what state it is in.

| # | Item | Acceptance |
|---|---|---|
| M1.1 | Command surface and machine-readable output (`design/01-cli.md`) | **done 2026-09-02** — every command has a `--json` form, the envelope is one type, and the exit codes are implemented and tested. The verbs behind the surface are M1.2–M1.7; until they land each exits 1 with a `not_implemented` note rather than pretending |
| M1.2 | Topology model — the `ha` preset, node count, per-node overrides (`design/02-topology.md`) | **done 2026-09-02** — presets `ha` and `single`, everything derived from the cluster name, parameters routed by file with `--set-hidden` as the escape hatch, and the describe artifact is the same value the tool builds from |
| M1.3 | Container backend — image, network, run-as-invoking-user, `NET_ADMIN`, `--init` | **done 2026-09-02** — a host-built `install.out` runs by path and is in no image; the base image is built once from a recipe tagged by its own hash. The engine's glibc floor is read from the ELF and checked against the image before anything starts |
| M1.4 | Assembly — config generation, the slave chain, start ordering (`design/03-assembly.md`) | zero manual interventions; a first-time user needs no ordering knowledge |
| M1.5 | Event verbs — `stop`, `kill`, `partition`, `heal`, `promote`, role-addressed | **done 2026-09-02** for `node stop` / `kill` / `start` and `fault partition` / `clear` / `ls`. Measured through the tool: a killed master fails over in 5 s, `master` then resolves to the other machine with no script change, a route-level partition produces two masters in 6 s, and clearing it lets the engine resolve the split brain — with the cancel reason in the log that tells the flavours apart. `promote` is M2. **The original acceptance is met**: the CBRD-26983 scenario set, replayed through these verbs, reproduces the id sequence `1, 2, 21, 22, 41, 42, 61` unattended ([`findings/scenario-cbrd26983.md`](findings/scenario-cbrd26983.md)) |
| M1.6 | Inspector tier 1 + tier 2 (`design/05-inspect.md`) | **done 2026-09-02** — `cluster status`, `node status`, `ha status`, `repl status`. Liveness from the runtime, role and process state from `changemode` and `heartbeat status`, replication position from `db_ha_apply_info` over SQL. Copy progress is *not* reported: it needs a master-side reference, which is M2.2, and until then the note says so rather than the number lying |
| M1.7 | The run record (`design/07-record.md`) | **done 2026-09-02** — every state-changing command appends without being asked, the engine's own HA lines are harvested into the same timeline under a separate actor, `export` carries the `describe` as it stood when the record opened, and every role change is reported with both intervals. First run: a promotion **5.9 s** after `node kill`, against the **2.5 s** the settings predict, with those settings in the document |

## Phase 2 — the anomalies ✅

**Complete 2026-09-02**, except the second consumer, which moved to phase 3 for a
reason rather than for lack of time: see M3.1.

G5, G6, G7, G9.

| # | Item | Acceptance |
|---|---|---|
| M2.1 | Condition verbs — `lag`, `splitbrain`, held until `heal` (`design/04-faults.md`) | **done 2026-09-02**. `fault lag --stage copy\|apply --mechanism suspend\|delay` suspends the named stage and `clear` resumes it; `fault splitbrain` induces two masters and reports the engine's own cancel reason, refusing a flavour the cluster's configuration cannot produce; `fault failcount` moves `fail_counter` by the field's own recipe and says that `clear` cannot reverse it. Measured: a suspended applier freezes `db_ha_apply_info` at a healthy-looking `lag=13`, and clearing it moves to `lag=2718` in one step — the lie and the truth, on demand |
| M2.2 | Replication observability with a master-side reference | **done 2026-09-02**. `repl status` reports two stages with two provenances: apply from `db_ha_apply_info`, copy against the master's `Append LSA`. All three cases hold — an apply stall freezes `apply_lag` at 3 while `copy_lag` climbs to 5188, a copy stall reads `apply_lag=0` while `copy_lag` climbs to 10,482, and a node with no row is a note rather than a zero. The reference is one parsed line from `applyinfo -r`, which amends `design/05-inspect.md` §3 and says why |
| M2.3 | `describe` as a shareable artifact | **done 2026-09-02** — `describe --out` writes **976 bytes** for a two-node cluster with a non-default parameter, a hidden one, a CPU quota and a fault in force, and `create --from` rebuilds it through the same path an ordinary create takes. Verified by round trip: rebuilt under a different name, the topology and the engine build compare identical; an artifact from a build this machine lacks refuses with that build's identity; a different build is a warning rather than a silent substitution |
| M2.6 | **Load driver**, both kinds (`design/06-load.md`) | **done 2026-09-02** — `insert`, `update`, `mixed`, `host-cpu`, `host-io`, running inside the node in stdlib Python. Measured: 20/s held exactly; 500/s refused and reported at 85.6/s with `--require-rate` exiting 1; `--batch 200` at 100 statements/s produces 14,141 rows/s. `bulkload` stays unimplemented on purpose — it is a named field case, not the general driver |
| M2.7 | `cluster quiesce` / `ha resync` (`design/04-faults.md` §8–§9) | **done 2026-09-02**. `--with-broker` starts a broker the assembly never had; `quiesce` moves its `ACCESS_MODE` with `broker_changer` and is verified through the door — a write returns `FAIL(-581)` while a read returns `OK`, and `resume` reopens it. `ha resync` chooses among resume, table and slave from `fail_counter` and the applier's log, and reports the reason. The `table` path compares row counts rather than repairing: seven induced failures came back `master=0 standby=0`, a scar rather than a divergence, which is the field's common case. A real divergence is refused by name rather than pretended |
| M2.5 | **Switchover-threshold validation** — vary one HA setting under load and observe whether the cluster switches over | **done 2026-09-02** — seven runs, one variable each, under load ([`findings/switchover-threshold.md`](findings/switchover-threshold.md)). The documented arithmetic does not describe the transition: `ha_max_heartbeat_gap` fourfold and `ha_heartbeat_interval_in_msecs` fourfold move nothing, while `ha_calc_score_interval_in_msecs` takes it from 6.9 s to 18.9 s. That discriminates the three explanations the field's stalled test could not separate |

**M2.5 has a specific stalled test to unblock, and a known cost.** The settings
ticket itself is resolved; **the hidden-parameter test that blocks it has been
open since February 2022** — an attempt at this same measurement that could not
separate engine behaviour from test artefact. It left three things unsettled: the default role change measured
at **8–11 s** where 5 × 500 ms predicts 2.5 s, `ha_max_heartbeat_gap` apparently
**inert**, and a reported **Active-Active window** after the network heals when
`ha_calc_score_interval_in_msecs` is raised. Those are the acceptance targets.
The cost model comes from the dynamic-settings review: `cubrid heartbeat reload`
reaches only
`cub_master`, and only four settings change without a restart
(`ha_node_list`, `ha_replica_list`, `ha_ping_hosts`, `ha_tcp_ping_hosts`), so
every other threshold value is **one cluster** —
[`requirements/02-ha-role-transition-field-evidence.md`](requirements/02-ha-role-transition-field-evidence.md)
§2 and §6.

## Phase 3

| # | Item | Acceptance |
|---|---|---|
| M3.1 | **The surface `cubrid-testkit` provisions through** (was M2.4) | testkit calls it to set up and tear down without screen-scraping |

**Why it moved.** Its acceptance is a sentence about a consumer, and that
consumer does not exist yet — `cubrid-testkit` is docs and an empty `impl/`. The
surface it would call is built and machine-readable, so nothing here is blocked;
what is missing is somebody on the other side to be wrong about it. Two things
have to be decided *with* testkit rather than guessed at ahead of it:

- **Whether a cluster with no host-facing port is usable.** Access is
  `node exec` and `node shell`, which is what keeps port bookkeeping absent
  ([`DESIGN.md`](DESIGN.md) §6). If testkit needs a socket, the bookkeeping comes
  back and [`design/03-assembly.md`](design/03-assembly.md) §6 is where it lands.
- **Whether the JSON is enough.** It is the contract, and
  [`design/ADR-001`](design/ADR-001-implementation-language.md) forbids handing
  testkit a Go package instead — sharing a language must not turn a process
  boundary into a build-time dependency. Whether the contract as it stands
  carries what a harness needs is a question its first real call answers.

Building against an imagined consumer is how a surface ends up fitting nobody.

| M3.2 | **Replication canary** — `repl check`, a write that has to arrive — **done 2026-09-02**. Measured on one cluster minutes apart: healthy, the row arrives in **0.63 s**; with the applier suspended the gauge reads `apply_lag=0` and `fail=0` while the row does not arrive in 15 s, exit 4. Every number says fine and nothing is moving | the field verifies a rebuilt slave with `applyinfo -r … -a` and a `repl_test` table it creates and inserts into, rather than by reading a threshold off a gauge ([`requirements/01-failback-field-evidence.md`](requirements/01-failback-field-evidence.md) §4). It is cheap, it is what an operator actually trusts, and it tests the path end to end rather than the view §3 of `design/05-inspect.md` says cannot be trusted alone |

### Also phase 3 — skeletal

**A web front end over the same surface**, and the reason to expect it is that
the two artifacts this tool produces are already documents somebody reads: a
`describe` small enough to paste into an issue, and a run record with a timeline
and two intervals per role change. Rendering those is most of a UI, and it is a
client of the same JSON the CLI emits rather than a parallel path (§9 OQ6). What
it must not become is a second surface with its own way of asking. A wider topology catalogue: replica nodes,
broker/CAS tiers, shard configurations, CDC consumers — each brings its own
configuration surface and its own fault verbs, and the migration from presets to
a declarative document is triggered here. **A Kubernetes backend behind the same topology model — and it is `cubrid-operator`
that is on the other side of it.** The operator already has a `CubridDB` CRD that
deploys, configures HA, schedules backups, scales and reports status, which is
the same topology this project models against a different backend. Two ways they
could meet, and they are not the same thing: the operator becomes **a second
backend** behind `02-topology.md`'s model, or it becomes **a component under
test** that this tool stands up and breaks. §9 OQ4 deferred the choice and it is
still deferred — but the language decision changed the cost of it, since both
projects are now Go and a shared topology model is the ADR's own named condition
for reopening how far that sharing goes. The tier-3 monitoring seam, once the engine has a
machine-readable metrics contract.

## Open

**What does the technical team require of failback?** ([`DESIGN.md`](DESIGN.md)
§9 OQ8.) Narrower than it was.
[`requirements/01-failback-field-evidence.md`](requirements/01-failback-field-evidence.md)
collected what the internal tracker already says, and it answers the mechanical
half: the rejoin path is `ha_make_slavedb.sh`, the alarm is `fail_counter`, and
the failback that actually hurts is the one that should never have started
(four sites, ten or more failover/split-brain/failback cycles a day
under load). What no ticket answers is what an operator *decides*: the threshold
for "caught up enough", whether write traffic is quiesced, who authorises it,
and whether the original master is preferred at all.
[`harness/failback.sh`](../harness/failback.sh) goes to the team with four edits
first (§7 of that document). **The marks it comes back with are the requirement
set**, and they shape phase 1's verb vocabulary — which is why M0.7 sits in
phase 0 and not later.

**A fifth edit, and it is the one that decides whether the answers are usable.**
A second pass over the tracker
([`requirements/02-ha-role-transition-field-evidence.md`](requirements/02-ha-role-transition-field-evidence.md))
found that **"failback" means demotion** to the engine and to the team — a master
stepping down — and that the tracker has no term at all for returning service to
the original master. A script that asks what the team requires "of failback" will
be answered about the wrong operation. It has to name the operation it means
before it goes out.

**Implementation language — decided 2026-09-02.**
[`design/ADR-001`](design/ADR-001-implementation-language.md) accepts **Go** for
the provisioner and shell for the operator-facing scripts. The earlier draft
proposed Python and rejected Go on the argument that nobody in this project's
ecosystem writes it; `cubrid-testkit` accepting Go under the same maintainer
inverted that argument rather than weakening it. M1.1 doubles as the validation
slice: if the schema types, the subprocess orchestration and the build-and-ship
story go badly there, the ADR is amended rather than defended.
