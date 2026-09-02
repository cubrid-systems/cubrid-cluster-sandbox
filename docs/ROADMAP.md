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

**Phase 0 complete (2026-08-27). Phase 1 started 2026-09-02**; M1.1 through M1.6
are done. One command builds a two-node HA pair and reaches `serving`, the three
failure verbs the CBRD-26983 verification needed work and are addressed by role,
and the cluster answers what state it is in without anything parsing
human-formatted text. **Phase 1 is done.** The scenario that started the project replays through the
tool unattended and reproduces its measured id sequence. Phase 2 starts with the
load driver, because everything else in it measures something under load. One external
dependency is outstanding and it shapes phase 1 rather than blocking it — see
*Open* below.

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

## Phase 2 — the anomalies, and the second consumer

G5, G6, G7, G9.

| # | Item | Acceptance |
|---|---|---|
| M2.1 | Condition verbs — `lag`, `splitbrain`, held until `heal` (`design/04-faults.md`) | **done 2026-09-02**. `fault lag --stage copy\|apply --mechanism suspend\|delay` suspends the named stage and `clear` resumes it; `fault splitbrain` induces two masters and reports the engine's own cancel reason, refusing a flavour the cluster's configuration cannot produce; `fault failcount` moves `fail_counter` by the field's own recipe and says that `clear` cannot reverse it. Measured: a suspended applier freezes `db_ha_apply_info` at a healthy-looking `lag=13`, and clearing it moves to `lag=2718` in one step — the lie and the truth, on demand |
| M2.2 | Replication observability with a master-side reference | **done 2026-09-02**. `repl status` reports two stages with two provenances: apply from `db_ha_apply_info`, copy against the master's `Append LSA`. All three cases hold — an apply stall freezes `apply_lag` at 3 while `copy_lag` climbs to 5188, a copy stall reads `apply_lag=0` while `copy_lag` climbs to 10,482, and a node with no row is a note rather than a zero. The reference is one parsed line from `applyinfo -r`, which amends `design/05-inspect.md` §3 and says why |
| M2.3 | `describe` as a shareable artifact | **done 2026-09-02** — `describe --out` writes **976 bytes** for a two-node cluster with a non-default parameter, a hidden one, a CPU quota and a fault in force, and `create --from` rebuilds it through the same path an ordinary create takes. Verified by round trip: rebuilt under a different name, the topology and the engine build compare identical; an artifact from a build this machine lacks refuses with that build's identity; a different build is a warning rather than a silent substitution |
| M2.4 | The surface `cubrid-testkit` provisions through | testkit calls it to set up and tear down without screen-scraping |
| M2.6 | **Load driver**, both kinds (`design/06-load.md`) | **done 2026-09-02** — `insert`, `update`, `mixed`, `host-cpu`, `host-io`, running inside the node in stdlib Python. Measured: 20/s held exactly; 500/s refused and reported at 85.6/s with `--require-rate` exiting 1; `--batch 200` at 100 statements/s produces 14,141 rows/s. `bulkload` stays unimplemented on purpose — it is a named field case, not the general driver |
| M2.7 | `cluster quiesce` / `ha resync` (`design/04-faults.md` §8–§9) | writes blocked by the broker's `ACCESS_MODE`, which requires `--with-broker`; and fail-count repair with its three paths, reporting how it chose and never zeroing the counter to tidy its output |
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

## Phase 3 — skeletal

Web front end over the same surface. A wider topology catalogue: replica nodes,
broker/CAS tiers, shard configurations, CDC consumers — each brings its own
configuration surface and its own fault verbs, and the migration from presets to
a declarative document is triggered here. A Kubernetes backend behind the same
topology model, and `cubrid-operator` as a component under test once operational
testing is a real use case. The tier-3 monitoring seam, once the engine has a
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
