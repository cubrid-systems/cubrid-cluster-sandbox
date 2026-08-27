---
title: cluster-sandbox — Roadmap
category: roadmap
project: cluster-sandbox
summary: Phases, milestones and current position. Phase 0 is complete; phase 1 is the CLI, the container backend and the HA preset. The one thing blocking design decisions rather than implementation is what the technical team requires of failback.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# Roadmap

Goals and their acceptance lines are in [`DESIGN.md`](DESIGN.md) §2. This file
says what order they get built in and where the project is.

## Where it is

**Phase 0 complete (2026-08-27).** Phase 1 not started. One external dependency
is outstanding and it shapes phase 1 rather than blocking it — see *Open* below.

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
| M1.1 | Command surface and machine-readable output (`design/01-cli.md`) | every command has a `--json` form; exit codes are documented and stable |
| M1.2 | Topology model — the `ha` preset, node count, per-node overrides (`design/02-topology.md`) | a two-node HA cluster from one command and one configuration input |
| M1.3 | Container backend — image, network, run-as-invoking-user, `NET_ADMIN`, `--init` | a host-built `install.out` runs by path with no image build |
| M1.4 | Assembly — config generation, the slave chain, start ordering (`design/03-assembly.md`) | zero manual interventions; a first-time user needs no ordering knowledge |
| M1.5 | Event verbs — `stop`, `kill`, `partition`, `heal`, `promote`, role-addressed | the CBRD-26983 scenario set reproduces the id sequence `1,2,21,22,41,42,61` |
| M1.6 | Inspector tier 1 + tier 2 (`design/05-inspect.md`) | liveness, HA role and replication position without parsing human-formatted text |

## Phase 2 — the anomalies, and the second consumer

G5, G6, G7, G9.

| # | Item | Acceptance |
|---|---|---|
| M2.1 | Condition verbs — `lag`, `splitbrain`, held until `heal` (`design/04-faults.md`) | lag is addressable per replication stage; split brain is assertable on the engine's own cancel reason |
| M2.2 | Replication observability with a master-side reference | a lag series that stays true through an apply stall, a copy stall, and a role change |
| M2.3 | `describe` as a shareable artifact | the output recreates the same cluster on another machine and fits in a JIRA issue |
| M2.4 | The surface `cubrid-testkit` provisions through | testkit calls it to set up and tear down without screen-scraping |

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
§9 OQ8.) The engine's own failback is automatic once diagnosed, and after a
*clean* failover there is no engine path back to the original master at all —
so whatever is done by hand on a real cluster is exactly the part the engine
does not do, and nobody here knows what that is.
[`harness/failback.sh`](../harness/failback.sh) encodes this project's guess,
with a decision point wherever the guess is a guess. **The marks it comes back
with are the requirement set**, and they shape phase 1's verb vocabulary — which
is why M0.7 sits in phase 0 and not later.

**Implementation language.** Not decided; see `design/README.md`.
