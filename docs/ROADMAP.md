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

**Phase 0 complete (2026-08-27). Phase 1 started 2026-09-02**; M1.1, M1.2 and
M1.3 are done, and `cluster create` reaches the `defined` state — containers on
a network, nothing started. The assembly that carries it to `serving` is M1.4. One external
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
| M1.5 | Event verbs — `stop`, `kill`, `partition`, `heal`, `promote`, role-addressed | the CBRD-26983 scenario set reproduces the id sequence `1,2,21,22,41,42,61` |
| M1.6 | Inspector tier 1 + tier 2 (`design/05-inspect.md`) | liveness, HA role and replication position without parsing human-formatted text |
| M1.7 | The run record (`design/07-record.md`) | every state-changing command appends without being asked; `record export` carries the `describe` that opened it. In phase 1 because a record a user has to switch on is missing from the run that mattered |

## Phase 2 — the anomalies, and the second consumer

G5, G6, G7, G9.

| # | Item | Acceptance |
|---|---|---|
| M2.1 | Condition verbs — `lag`, `splitbrain`, held until `heal` (`design/04-faults.md`) | lag is addressable per replication stage; split brain is assertable on the engine's own cancel reason |
| M2.2 | Replication observability with a master-side reference | a lag series that stays true through an apply stall, a copy stall, and a role change |
| M2.3 | `describe` as a shareable artifact | the output recreates the same cluster on another machine and fits in a JIRA issue |
| M2.4 | The surface `cubrid-testkit` provisions through | testkit calls it to set up and tear down without screen-scraping |
| M2.6 | **Load driver**, both kinds (`design/06-load.md`) | db and host profiles; a stated rate the driver holds, `--require-rate`, and achieved reported next to requested. **Build order puts this before M2.1, M2.2 and M2.5** — the numbers are identifiers, not a sequence — because each of those measures something under load |
| M2.7 | `cluster quiesce` / `ha resync` (`design/04-faults.md` §8–§9) | writes blocked by the broker's `ACCESS_MODE`, which requires `--with-broker`; and fail-count repair with its three paths, reporting how it chose and never zeroing the counter to tidy its output |
| M2.5 | **Switchover-threshold validation** — vary one HA setting under load and observe whether the cluster switches over | The field asked for exactly this in 2021 and said developers cannot do it: validation belongs in a user's environment, which is what this tool is. A reproduced switchover records its *inputs* (settings, load, timings) **and both intervals — the one measured and the one the parameters predict**, because a threshold-caused switchover may leave nothing in the engine log |

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
