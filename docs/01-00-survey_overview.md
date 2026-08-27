---
title: Local Multi-Node Provisioning Across Comparable Systems — Survey OVERVIEW
category: roadmap-survey
project: cluster-sandbox
status: selected
lang: en
sources:
  - 00-foundation.md §1 (the measured CBRD-26983 HA assembly that motivates the series)
  - Per-system files in this directory (to be authored — see §2)
  - https://github.com/CUBRID/cubrid-contrib — sandbox/, docker_for_ctp/
  - https://github.com/cubrid-systems/cubrid-testkit — docs/ROADMAP.md, docs/adr/ADR-001
summary: Index and framing for the N65 survey series. The design space is narrower than it looks: every system that ships a local multi-node provisioner answers the same five questions — what a topology is, where the binaries come from, what isolates a node, which lifecycle verbs exist, and what the tool shows you. This overview fixes those five axes, proposes the comparable set, and carries an empty comparison matrix that the per-system legs fill. Textbook anchors are thin here and the overview says so rather than manufacturing them.
created: 2026-08-18
updated: 2026-08-18
tags: [roadmap, survey, overview, cluster-sandbox, provisioning, containers, ha, developer-experience]
---

**Contents:**

- [1. Purpose & Scope](#1-purpose--scope)
- [2. Files in this survey series](#2-files-in-this-survey-series)
- [3. The five decisions every provisioner makes](#3-the-five-decisions-every-provisioner-makes)
- [4. Comparable set — proposal and rationale](#4-comparable-set--proposal-and-rationale)
- [5. Comparison matrix (to be filled by the legs)](#5-comparison-matrix-to-be-filled-by-the-legs)
- [6. Questions the legs must answer](#6-questions-the-legs-must-answer)

## 1. Purpose & Scope

`00-foundation.md` §1 records what it cost to answer one HA question by hand:
config files per node, a backup/restore chain to build the second node, an
ordering constraint between `service start` and `heartbeat start`, and failure
induction improvised with `docker network disconnect` and `pkill`. The claim
this series has to test is that none of that is CUBRID-specific — that other
systems faced the same assembly problem and converged on a small number of
answers, and that the shape of a CUBRID provisioner can be read off those
answers rather than invented.

Scope is **local, developer-facing, multi-node provisioning**: a tool a person
runs on one machine to get a working cluster of a chosen version. Production
orchestration (Kubernetes operators, configuration management) is in scope only
where a project deliberately reuses one substrate for both, because that choice
is itself a finding. Test *harnesses* are in scope where they provision — the
interesting cases are the ones where the harness's node-startup layer became a
reusable tool, or failed to.

**What this survey does not do.** It does not select the design. §4 Proposed
Design in the foundation stays unwritten until this series closes
(CLAUDE.md §2.7 step 7).

## 2. Files in this survey series

| File | Role | State |
|---|---|---|
| `01-00-survey_overview.md` | This file — axes, comparable set, matrix, cross-cutting findings | authored |
| `01-01-survey_postgresql.md` | `pg_createcluster` (single-node packaging) and the in-tree TAP cluster harness (multi-node + fault verbs) | authored |
| `01-02-survey_mysql.md` | `dbdeployer`, MySQL Shell AdminAPI sandbox instances, `mysql-test-run.pl` | authored |
| `01-03-survey_mongodb.md` | `mlaunch` / mtools — topology from flags, node control by role tag | authored |
| `01-04-survey_tidb.md` | `tiup playground` / `tiup cluster` — version-selectable local cluster, bundled monitoring | authored |
| `01-05-survey_cubrid-gap-and-measurement.md` | CUBRID synthesis: seven gaps, measurement plan, prerequisite order | authored |

Reading order is the `NN` prefix; cross-references go strictly backwards
(CLAUDE.md §2.7).

## 3. The five decisions every provisioner makes

The usual textbook anchors for this repo's surveys — Petrov, Silberschatz,
Gray76 — do not cover developer provisioning, and pretending otherwise would
manufacture a foundation the topic does not have. The foundational structure
here is derived instead from the assembly recorded in `00-foundation.md` §1:
each manual step maps to a decision a provisioner has to make on the
developer's behalf.

**D1 — What is a topology?** A count of nodes and their roles, or a named
preset, or a full declarative document. The measured assembly needed
`ha_node_list`, per-node `cubrid.conf`, and an implicit role split (which node
gets `createdb`, which gets `restoreslave`). Whether a tool exposes presets
(`--replicas 2`) or a file decides how far it scales before it needs the file
anyway.

**D2 — Where do the binaries come from?** A packaged release the tool
downloads, a version already installed, or a path the developer points at. The
third case is the one that matters for engine work and the one most tools treat
as an afterthought — CUBRID's requirement is explicitly that a local build
directory can be handed in (`00-foundation.md` §1), and that turned out to cost
nothing because a host-built tree runs unmodified in a stock container.

**D3 — What isolates a node?** Separate processes and ports on one host,
containers, or VMs. Process-level is the cheapest and is what most database
sandbox tools chose; containers buy realistic hostnames, network isolation, and
the ability to sever a link — which the measured HA work needed, because
failover had to be induced by cutting the network.

**D4 — Which lifecycle verbs exist?** Every tool has create/start/stop/destroy.
The distinguishing set is the *fault* verbs: promote, demote, partition, kill,
rejoin. The measured assembly had none of them and improvised each. Whether
comparable tools ship fault injection, and if not what people use instead, is
the single most decision-relevant question in this series.

**D5 — What does the tool show you?** Nothing (the developer runs their own
client), process-level status, or replication/cluster state. `00-foundation.md`
§9 OQ2 already splits CUBRID's answer into three cost tiers; the legs should
record which tier each comparable tool stopped at, because that is evidence
about which tier is actually load-bearing for developers.

## 4. Comparable set — proposal and rationale

Four systems, chosen to cover the axis extremes rather than to be
representative databases. **This set is a proposal and is the one thing in this
file that needs confirmation before the legs are authored.**

| System | Why it earns a leg |
|---|---|
| **PostgreSQL** | The case where provisioning grew *inside* the project: `pg_createcluster` from packaging, and a test-framework cluster module that sets up streaming replication programmatically. Tests whether an in-tree harness can double as a developer tool — directly relevant to the `cubrid-testkit` boundary in `00-foundation.md` §9 OQ3 |
| **MySQL** | The case with *three* answers at once — a community sandbox tool (`dbdeployer`), a vendor-official sandbox API (MySQL Shell AdminAPI), and a test harness that starts topologies (`mysql-test-run.pl`). The most informative single system for D1/D2, and the clearest evidence on whether vendor-official and community tools converge |
| **MongoDB** | The community single-command case (`mlaunch`): replica sets and sharded clusters from flags, no config file. The lower bound on D1 — how far presets go before a file is needed |
| **TiDB** | The modern vendor-official case (`tiup playground` / `tiup cluster`): version selection as a first-class argument and the same tool spanning local and real deployment. Directly probes D2 and the OQ4 question of whether one substrate serves both |

Recorded but not given legs, to be cited from §5 where they settle an axis:
**CockroachDB** `cockroach demo` (single binary, instant multi-node — the
extreme of D3), **kind** / **k3d** (the container-cluster provisioner pattern
outside databases), **Testcontainers** (library-level provisioning for tests,
already named in `cubrid-testkit` ADR-001).

## 5. Comparison matrix (to be filled by the legs)

Cells stay empty until the corresponding leg is authored. An empty cell is
honest; a guessed cell is a defect (`$KB_ROOT/knowledge/methodology/comparison-matrix.md`).

| Axis | PostgreSQL | MySQL | MongoDB | TiDB |
|---|---|---|---|---|
| D1 topology declaration | imperative Perl API; no presets | **named topologies** — `--topology=master-slave\|group\|all-masters\|fan-in`; AdminAPI has none (one instance per call) | flags in the system's own vocabulary — `--replicaset --nodes --sharded --config --mongos` | per-component counts — `--db --kv --pd --tiflash` |
| D2 artifact source | **`install_path`** — local build first-class | downloaded tarball `unpack`ed into a versioned dir; `/path/to/version` reachable, build tree **not documented** | **`--binarypath`** — documented for self-compiled source | version string via `tiup`; **`--{comp}.binpath`** per component (mixed clusters) |
| D3 isolation substrate | processes + ports | processes + ports | processes + ports | processes + ports |
| D4 lifecycle / fault verbs | `start` `restart` `reload` `stop(mode)` **`kill9`** **`promote`** `backup` `init_from_backup`; **no partition** | `start` `stop` `restart` **`kill`** `wipe_and_restart` (+ `_all`); AdminAPI `start/stop/`**`kill`**`/delete`; no promote, **no partition** | `start` `stop` `restart` **`kill`** with **role-tag selectors** (`kill shard-a secondary`); **no partition** | `scale-out` / `scale-in --pid`; no kill-vs-stop distinction; **no partition** |
| D5 what the tool shows | none — consumer is a test | sandbox catalog / list (tier 1) | `list --tags`, `list --startup` (tier 1) | **Grafana + Dashboard bundled** (tier 3) |
| Vendor-official or community | project-internal (packaging + in-tree harness) | **both** — community `dbdeployer`, vendor AdminAPI | community only | vendor only |
| Shares a substrate with production | no | no | no | **yes** — `tiup cluster` |
| Test-harness integration | **is** the harness | `mysql-test-run.pl` separate; per-sandbox test scripts | none | none (playground is disposable) |
| Config surface beyond the CLI | Perl API | AdminAPI (shell scripting API) | none | read-only web dashboards |

### 5.1 Derived implications

**DI1 — Nobody ships a network partition, because everybody chose processes.**
All four isolate nodes with ports on one host, and not one offers link
severing. CUBRID cannot follow: the measured HA verification induced failover
by cutting the network, because `cubrid heartbeat stop` takes the server down
with it and `cubrid changemode` refuses an active→standby transition the
heartbeat did not drive (`server_support.c:1558`). **Containers are therefore a
requirement rather than a preference (D3), and the partition verb is the one
part of this design with no model to copy.**

**DI2 — Pointing at a local build is ordinary, not exotic.** Three of four
document it (`install_path`, `--binarypath`, `--{comp}.binpath`), and TiDB's
per-component form goes further than the requirement — a mixed cluster of one
locally built component against released others. The CUBRID requirement is
squarely inside precedent, and cost nothing to satisfy: a host-built tree runs
unmodified in a stock container.

**DI3 — Placement is split four ways, so precedent cannot settle OQ3.**
In-tree harness (PostgreSQL), community tool *and* vendor API (MySQL),
community only (MongoDB), vendor tooling spanning local and production (TiDB).
Every option in `00-foundation.md` §9 OQ3 and OQ4 has a working example behind
it, which means the CUBRID answer has to come from the `cubrid-testkit`
boundary and the `cubrid-operator` relationship, not from this table.

## 6. Questions the legs must answer

Each leg closes with its answers to these, so §5 can be filled mechanically.

1. **Fault verbs (D4).** Does the tool ship promote / demote / partition /
   kill, or do users script them? If scripted, against what interface?
2. **Local build path (D2).** Can a developer point the tool at a tree they
   built themselves, and is that a first-class argument or a workaround?
3. **Where provisioning lives.** Inside the engine repo, in a test harness, or
   in a separate tool — and did that placement change over time? This is the
   evidence `00-foundation.md` §9 OQ3 needs.
4. **Observability tier (D5).** Which of the three tiers did the tool stop at,
   and is there a record of users asking for more?
5. **What it refused to do.** Documented non-goals are the cheapest source of
   §5 Alternatives reject conditions.

Figures (CLAUDE.md §2.9) are authored after the legs, when the cross-cutting
shape is known; the overview carries none yet.
