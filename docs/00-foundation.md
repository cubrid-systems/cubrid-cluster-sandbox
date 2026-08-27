---
title: Cluster Sandbox — Foundation
category: roadmap-foundation
project: cluster-sandbox
status: graduated (own dir: /data/cub_sys/projects/cluster-sandbox)
sources:
  - projects/10-selected/N64-cubrid-ops/00-foundation.md §1 ("Developer Experience (Docker/CLI/SDK) is out of scope here" — the only place this repo names the idea, and the only one of the four recorded candidates left without an owner)
  - https://github.com/cubrid-systems/cubrid-testkit — docs/ROADMAP.md (HA appears once, as a target workload for isolation-anomaly verification), docs/adr/ADR-001 (Testcontainers named for self-testing the harness, not for provisioning a CUBRID topology)
  - https://github.com/CUBRID/cubrid-contrib — `sandbox/sandbox.sh` (~130 lines of POSIX sh; `img new|rm|ls` and `pod run|rm|ls`; `docker run --rm -it -v $SRC:$SRC -w $SRC -h <name> -u $(whoami) [--cpuset-cpus]`), `sandbox/Dockerfiles/Dockerfile_centos7` (CentOS 7 + devtoolset-8 build dependencies, `ARG WHOAMI` + `adduser $WHOAMI`), `docker_for_ctp/` (two containers on fixed IPs + ssh, drives CTP)
  - https://github.com/CUBRID/cubrid-operator — `CubridDB` CRD (deploy · HA · backup schedule · scale · status), early-stage; production Kubernetes rather than local iteration
  - CBRD-26983 / PR #7720 — the serial cache write-back change whose HA verification produced the measurements in §1
  - src/object/schema_system_catalog.cpp:111 (`db_ha_apply_info` — replication progress is a catalog view, therefore SQL-readable)
  - src/executables/utility.h:1599-1602 (`cubrid statdump` takes only `--output-file` and `--interval`; there is no output-format option)
  - src/connection/heartbeat.h:62-70 (`HB_PTYPE_SERVER` / `HB_PTYPE_COPYLOGDB` / `HB_PTYPE_APPLYLOGDB` — the replication pipeline is two heartbeat-managed processes, not one)
  - src/executables/master_heartbeat.c:866-895 (split brain is a named diagnosis: `num_master > 1` with a priority mismatch logs `[Failback] [Diagnosis] Multiple master nodes (a, b) are detected` and queues `HB_CJOB_FAILBACK`), :1042-1054 + :1110-1135 (the ping check that decides failback vs failover, and its cancel conditions)
  - src/object/schema_system_catalog_install.cpp:1956-1988 (`db_ha_apply_info` is 26 columns — six LSA pairs, three progress timestamps, six counters — not a delay scalar)
  - src/executables/util_cs.c:3893-3924 + src/transaction/log_applier.c:7456-7478 (`cubrid applyinfo` reports two delays as `printf` text, and the first sample always prints `-` because `process_rate` is zero until a second iteration)
summary: A CLI-first provisioner that stands up a CUBRID topology in containers from a declarative configuration — node count and roles, engine version or a local build directory, parameters — so that engine developers, QA, and external contributors can reproduce a multi-node setup without hand-assembling it. Seed motivated by a measured case: verifying one HA question for CBRD-26983 took a hand-built two-node cluster, and every step of that assembly is mechanical. A web front end over the same API and per-container monitoring are part of the intended shape; the monitoring depth is the open question with a real cost boundary, because engine-internal counters are text-only until N20 lands. A second requirement set (2026-08-27) extends the scope past assembly to the states a developer needs to reproduce — inter-node lag, split brain, a semi-automatic failback script, and replication monitoring and tracking — which are conditions rather than events and land as G7-G9. Intended to live as its own repository under `cubrid-systems`, like `cubrid-testkit` and `spatial`.
created: 2026-08-18
updated: 2026-08-27
lang: en
tags: [foundation, graduated, developer-experience, docker, containers, ha, test-environment, provisioning, monitoring, replication, fault-injection, cli, cluster-sandbox]
---

> **Source of truth.** This is the project's own foundation, migrated out of the
> CUBRID Systems roadmap on 2026-08-28 when `cluster-sandbox` graduated to its
> own repository. The roadmap keeps a thin pointer at
> `roadmap/projects/30-graduated/N65-cluster-sandbox/00-foundation.md` carrying
> org identity and cross-project relationships only; the detail — §1 Context,
> §4 Proposed Design, §7 Failure Modes, §8 Rollout — lives here now.
>
> All 11 sections were authored in the roadmap's selected band (2026-08-18) on
> top of the survey series, which sits alongside this file:
> [`01-00-survey_overview.md`](01-00-survey_overview.md) … CUBRID synthesis
> [`01-05-survey_cubrid-gap-and-measurement.md`](01-05-survey_cubrid-gap-and-measurement.md).
> A second requirement set landed 2026-08-27 (§2 G7–G9); two of its three open
> questions were closed by measurement the same day, and the third (§9 OQ8) is
> the one this project cannot close on its own. Measurement write-ups:
> [`findings/`](findings/). Outstanding: SVG comprehension figures for the
> anomaly material.

## 1. Context & Problem

CUBRID has no supported way to stand up a multi-node topology for development.
The pieces that exist each solve a neighbouring problem: `cubrid-contrib/sandbox`
is a single-container **build** shell, `cubrid-contrib/docker_for_ctp` is a
two-container rig for driving **CTP** against a released tarball, `cubrid-testkit`
is the **test harness** succeeding CTP and treats HA as a target workload rather
than as something it provisions, and `cubrid-operator` deploys to **production
Kubernetes**. This repo names the gap exactly once: N64-cubrid-ops §1 records
*Developer Experience (Docker/CLI/SDK)* among the candidates it declined, and it
is the only one of those four left without an owner.

The cost of that gap was measured on 2026-08-18. Answering a single question
about CBRD-26983 — whether a failover and failback can hand out an
AUTO_INCREMENT value the other node already issued — required a two-node HA
cluster, and building it by hand took the whole of the following: writing
`cubrid_ha.conf` and a `ha_mode=on` `cubrid.conf` for each node; `createdb` on
one node, then `backupdb` → fix the backup file's permissions → copy it across →
`restoreslave` on the other; `service start` then `heartbeat start` on each, in
that order; and inducing failover by hand with `docker network disconnect` and
`pkill cub_master`, because `cubrid heartbeat stop` takes the server down with it
and `cubrid changemode` refuses an active→standby transition that the heartbeat
did not drive. Two further traps cost time rather than steps: after
`heartbeat stop`, `heartbeat start` alone fails with *"CUBRID heartbeat feature
is being deactivated"* and needs a full `service stop` / `service start`; and with
`ha_ping_hosts` unset the cluster logs `[Failback] [Cancelled] No hosts are
registered in ha_ping_hosts`, so a partition is never diagnosed and split brain
persists until an operator intervenes. None of this is research. It is a script
that nobody has written, and every developer who needs an HA repro writes it
again.

Part of the plumbing is already written, in the neighbouring tool. `cubrid-contrib/sandbox`
runs its container as the invoking user (`ARG WHOAMI` + `adduser $WHOAMI` in the Dockerfile,
`-u $(whoami)` at run time) and mounts the shared directory **at the same path inside and
outside** (`-v $SRC:$SRC -w $SRC`). Both decisions are worth taking rather than rediscovering:
the first is exactly the problem the measurement above ran into, where `cubrid backupdb`
produced a root-owned file that the host user could not copy to the second node, and the second
keeps compiler paths, core dumps, and debugger paths valid on both sides. What does not carry
over is the image: sandbox's is a *build* image (devtoolset-8, cmake, ant, bison) on a base that
reached end of life in 2024, and a runtime container needs none of it.

The container substrate itself turned out to be free. The topology ran on the
stock `ubuntu:24.04` image with no Dockerfile: CUBRID's binaries link only
against libc, libm, libgcc_s and libstdc++ plus the libraries the install tree
ships, so a build produced on the host runs unmodified in a container with the
install directory bind-mounted. That is what makes a local-build workflow
practical — the same provisioner can take a released version or a path to a
developer's own `install.out`, and the second case costs a bind mount rather
than an image build.

**Home.** The work is intended as its own repository under the `cubrid-systems`
organization, following the pattern this repo already uses for `cubrid-testkit`
and `spatial` — the roadmap entry carries organizational positioning and the
sibling repo carries the design, at which point this project moves to
`30-graduated/` with a thin foundation per CLAUDE.md §2.5.

**The second cost lands after the cluster is up.** Assembly is only the
precondition. What a developer needs to reproduce are states the assembly does
not hand them: a slave that has fallen behind, two nodes that both believe they
are master, and the operator-driven return to the original master once a
failover has happened. Each is a *condition* that holds until something clears
it rather than an event, and the 2026-08-18 session reached them sideways
rather than on request — split brain arrived as a consequence of `ha_ping_hosts`
being unset and persisted because nothing diagnosed it, and the demotion that
ends a failback was seen only as a log line in the middle of an induced
failover (`01-05` §2 G4). The engine already names all three: the replication
pipeline is two heartbeat-managed processes rather than one
(`HB_PTYPE_COPYLOGDB` and `HB_PTYPE_APPLYLOGDB`, `heartbeat.h:62-70`), split
brain is a diagnosis with its own log string
(`[Failback] [Diagnosis] Multiple master nodes (a, b) are detected`,
`master_heartbeat.c:866-871`), and replication progress is twenty-six columns
of a catalog view (`schema_system_catalog_install.cpp:1956-1988`). What is
missing is a way to *ask* for those states and a way to *watch* them.

That second requirement set was supplied by hgryoo on 2026-08-27 — inter-node
lag, split brain, a semi-automatic failback script, replication monitoring, and
replication tracking — and it is aimed at a party this project has not yet
talked to. The people who perform failback on real clusters are the technical
team, their requirements are unknown here, and §9 OQ8 records the artifact
meant to extract them rather than guessing on their behalf.

## 2. Goals

**G1 — One command stands a topology up.**
*Acceptance*: from an empty directory, a two-node HA cluster serving queries
after one command and one configuration input. Measured against the 2026-08-18
baseline (§1): two configuration files per node, a four-step slave chain, an
ordering constraint, and two undocumented traps all reduce to zero manual
steps, verified by someone who has not built one before.

**G2 — The build under test is an argument.**
*Acceptance*: a locally built install tree runs by path, and a released version
by name, neither requiring an image build. Precedent for treating this as
ordinary rather than exotic: `install_path`, `--binarypath`, `--{comp}.binpath`
(`01-00-survey_overview.md` §5.1 DI2).

**G3 — Failure scenarios are verbs, addressed by role.**
*Acceptance*: clean stop, crash, and network partition are one verb each and
target `master` / `slave`, not a pid or a container id. Re-running the
CBRD-26983 scenario set through those verbs reproduces the measured id sequence
`1, 2, 21, 22, 41, 42, 61`.

**G4 — The cluster is inspectable without parsing human-formatted text.**
*Acceptance*: one command reports per-node liveness, HA state, and replication
delay, with the delay read from the `db_ha_apply_info` catalog view over SQL.

**G5 — A topology is reproducible from an artifact.**
*Acceptance*: a `describe` output recreates the same cluster on another
machine and is small enough to attach to a JIRA issue
(`01-03-survey_mongodb.md` §4 I3).

**G6 — `cubrid-testkit` can drive it as a dependency.**
*Acceptance*: a non-interactive surface — stable exit codes, machine-readable
output — that testkit calls to provision and tear down without screen-scraping.

**G7 — Anomalous states are requested, not waited for.**
*Acceptance*: replication lag and split brain are each one command that puts
the cluster into that state and holds it until healed. Lag is addressable per
replication stage, because CUBRID's pipeline is two processes and stalling
`copylogdb` produces a different state than stalling `applylogdb`
(`heartbeat.h:62-70`) — the same split the engine's own report already makes,
"Delay in Copying Active Log" versus "Delay in Applying Copied Log"
(`util_cs.c:3893-3924`). Split brain is confirmed by the engine, not by
inspection: both nodes report `active` while the state holds, and
`[Failback] [Diagnosis] Multiple master nodes` appears in the heartbeat log
once it is healed (`master_heartbeat.c:866-871`).

**G8 — Failback is a script, and the script is the question.**
*Acceptance*: the operator sequence that returns a cluster to its original
master exists as a runnable, step-annotated script with a human decision point
at every step that is not mechanically safe, and it is put in front of the
technical team as the thing to correct. The measure is not how much of it runs
unattended; it is that the team marks it up and the marks become requirements
(§9 OQ8). Full automation is deliberately declined until they have. **Written
and run end to end 2026-08-27**: `failback.sh` restored the original master in
a two-node pair with no row loss (`rc=0`, promotion in 2 s), which establishes
that the return trip is mechanically possible with commands that already exist
— and therefore that what is missing is judgement, not capability.

**G9 — Replication is watched over time, and against the master.**
*Acceptance*: per-node replication state is read from `db_ha_apply_info` over
SQL — the LSA columns (`committed` / `committed_rep` / `append` / `eof` /
`final` / `required`), the three progress timestamps, and the six counters of
which `fail_counter` separates broken from slow
(`schema_system_catalog_install.cpp:1956-1988`) — sampled on an interval and
retained, so a lag episode can be traced afterwards rather than only watched
live. **Amended 2026-08-27 after running it (OQ7): that view alone is not a lag
measurement, and a monitor built on it reports the opposite of the truth.** The
row is written by `applylogdb` itself, so a stalled applier freezes *every*
column including `eof_lsa` and reports a constant, healthy-looking lag; and
during a `copylogdb` stall the applier keeps draining the on-disk backlog, so
the reported lag **falls** while replication is entirely stopped (measured:
49,544 → 38,576 pages with copying suspended). The acceptance therefore gains a
second half — the collection path must carry a **master-side reference** for
the copying stage, and anything that cannot must not call `eof - final`
"replication delay". The negative acceptance stands and was also observed:
nothing parses `cubrid applyinfo` text, whose first sample prints `-` because
`process_rate` is zero until a second iteration (`util_cs.c:3893-3903`,
`log_applier.c:7456-7478`).

## 3. Non-Goals

- **Production deployment and lifecycle management.** That is
  `cubrid-operator`'s `CubridDB` CRD.
- **Running tests.** `cubrid-testkit` owns suites, dispatch, and reporting;
  this project owns the environment they run on.
- **Collecting engine-internal metrics.** Tier 3 belongs to N64 W1 over N20.
  This project leaves a seam (§4) and does not build a second collector.
- **A management console for real clusters.** N64 owns the Web Management
  Console; any UI here is a development-environment control panel.
- **Non-Linux hosts** in the first phase.

## 4. Proposed Design

Architecture only, per CLAUDE.md §2.6. Five layers, each answering one of the
survey's decisions (`01-00` §3).

1. **Topology model (D1).** A named preset plus a count plus per-node
   overrides — `ha` with two nodes is the case that motivated the project. Roles
   come from CUBRID's own vocabulary (`master`, `slave`, `replica`) because they
   are also the addressing keys for layer 4. A declarative document is deferred,
   not designed away: MongoDB expresses a sharded multi-router cluster in flags
   (`01-03` §2), which puts CUBRID's near-term catalogue well inside what
   presets carry.

2. **Provisioner core (D1→D2).** Turns the model into what the engine needs:
   per-node `cubrid.conf` and `cubrid_ha.conf`, the database construction chain
   (`createdb` → `backupdb` → transfer → `restoreslave`), and the start ordering
   `service start` before `heartbeat start`. This layer owns the traps — the
   heartbeat reactivation sequence and the `ha_ping_hosts` default (§1, and
   `01-05` §2 G3/G6) are configuration the provisioner gets right by
   construction rather than documentation a user must remember.
   **Measured 2026-08-27 (OQ9)**: the ownership does *not* have to run the other
   way. Split brain was assumed to need a deliberately wrong configuration; it
   does not. A cluster
   with `ha_ping_hosts` correctly set reaches two masters in nine seconds when
   the ping host survives the partition, because a single ping host is a quorum
   of one that votes for whoever asks it. So the provisioner writes the correct
   configuration always, and the anomaly comes from the *fault*, not from the
   config. The unset-`ha_ping_hosts` variant remains reproducible as a second
   flavour distinguished by its log line, and when a scenario asks for it the
   deviation belongs to the scenario and has to travel in `describe` (G5).

3. **Backend (D3): containers.** One container per node, one user-defined
   network, hostname equal to node name. This is a **requirement, not a
   preference**: none of the four surveyed tools ships a network partition
   because all four chose process isolation, and CUBRID's failover induction
   needs one (`01-00` §5.1 DI1). Two conventions are inherited from
   `cubrid-contrib/sandbox` rather than rediscovered — run as the invoking user,
   and mount the build tree at an identical path inside and outside (§1). A
   third is the provisioner's own and was found by running it: **`ping` must be
   in the image**. `hb_check_ping` does not open a socket, it runs
   `popen("ping -w 1 -c 1 <host> …; echo $?")`, so an image without
   `iputils-ping` returns 127 — read as `HB_PING_FAILURE`, indistinguishable
   from a partitioned ping host, which makes every master demote itself on any
   heartbeat loss. The partition mechanism is a backend requirement too: it has
   to work at **route level** (`ip route add blackhole <peer>`, hence
   `NET_ADMIN`), because cutting the whole interface cannot express "cut the
   peer but keep the ping host" — and that distinction is the entire content of
   OQ9. And the container needs a **reaping PID 1** (`--init`, or an entrypoint
   that waits): without one, `cubrid heartbeat stop` never returns, because
   `us_hb_deactivate` polls "is any `cub_server` still running" on a one-second
   sleep (`util_service.c:3995-4004`) and a zombie `cub_server` answers yes
   forever. Measured 2026-08-27 — the command sat in `hrtimer_nanosleep` for
   five minutes with `cub_server`, `cub_pl` and both `cub_admin` processes
   defunct and reparented to `tail -F /dev/null`, while the deactivation itself
   had already logged *Command execution: deactivate. Success.* and the peer had
   been promoted. This is the sharpest example so far of why the tool owns the
   substrate: nothing about it is discoverable from CUBRID's documentation, and
   it makes the one command a planned failback depends on look like a hang. A
   Kubernetes backend is a seam behind the same topology model, not a phase-1
   deliverable (§8).

4. **Verbs (D4).** Lifecycle (`create`, `start`, `stop`, `destroy`) and fault
   (`kill`, `partition`, `heal`, `promote`), all role-addressed. The
   kill-versus-stop split is not cosmetic in CUBRID: a graceful stop runs the
   serial cache write-back (`serial_flush_cache_pool`) and a crash does not,
   which is exactly the pair the CBRD-26983 verification had to build by hand.
   The set G7 adds behaves differently and that is the design item: `lag` and
   `splitbrain` are **conditions**, entered by one command and held until
   `heal`, where the four above are events. A condition needs an owner — who
   reverses it, and what the engine does while it is in force — and `lag`
   additionally needs a target, since the replication pipeline has two stages
   that stall independently. `failback` is a third shape again: a scripted
   sequence with human decision points (G8), not a verb the tool completes on
   its own.

5. **Inspector (D5).** Tier 1 from the container runtime (liveness, logs,
   resources) and tier 2 from the engine's existing surfaces — `db_ha_apply_info`
   over SQL for replication delay, `cubrid changemode` and
   `cubrid heartbeat status` for role and process state. Tier 2 is where G9 lands,
   and it is deeper than a delay number: `db_ha_apply_info` is twenty-six
   columns whose useful content is a *set* rather than a scalar — the LSA
   columns separate copied progress from applied progress, and `fail_counter`
   is what distinguishes replication that is broken from replication that is
   behind. The inspector therefore samples and retains rather than prints, so
   that "when did the slave start falling behind, and against which stage" is
   answerable after the episode instead of only during it. It also needs a
   **second source**, which OQ7 established the hard way: the view is written by
   `applylogdb`, so it cannot report a stall of the process that writes it, and
   during a copy stall it reports a *falling* lag. Copying progress has to be
   measured against the master's append position — the reference
   `cubrid applyinfo -r <master>` uses — and the inspector reads that position
   itself rather than parsing applyinfo's output. Tier 3 is a
   **seam**: a documented attachment point for a scraper once N20 and N64 W1
   provide a contract, with nothing in this project parsing `statdump`.

**Consumers.** The CLI is the primary surface. `cubrid-testkit` consumes the
same non-interactive surface as a dependency (decided, §9 OQ3). A web front end
sits over that surface later rather than beside it (§9 OQ6).

## 5. Alternatives Considered

**A1 — Do nothing; every developer writes the script again.**
*Reject*: the cost is measured, not hypothetical (§1), and it is paid per
person per repro. *Revisit when*: never — the baseline is the argument.

**A2 — Extend `cubrid-contrib/sandbox` in place.**
*Reject*: sandbox's image is a *build* image on an end-of-life base, its
lifecycle is a single interactive container, and it has no network, no second
node, and no database. The conventions transfer (§4 layer 3); the codebase does
not. Placement is separately decided — own repository under `cubrid-systems`.
*Revisit when*: the build role and the run role converge enough that one image
serves both.

**A3 — Put provisioning inside `cubrid-testkit`.**
*Reject*: decided the other way — testkit consumes this tool. The survey also
shows the failure mode: PostgreSQL's harness has every primitive
`cluster-sandbox` wants (`kill9`, `promote`, backup/restore, `install_path`) and
exposes none of them to a developer who is not writing a TAP test
(`01-01` §4 I1). *Revisit when*: testkit grows an environment layer that
duplicates this one.

**A4 — Build on Kubernetes from the start (kind / k3d + `cubrid-operator`).**
*Reject*: entry cost for the external-contributor half of the audience, and the
operator is production-shaped — its concerns are scheduling, storage classes,
and rollout, not "kill the master and see what the ids do". *Revisit when*:
operational testing becomes a primary use case rather than a later inclusion
(§9 OQ4).

**A5 — Process isolation on one host, as all four surveyed tools chose.**
*Reject*: it forecloses the partition verb (`01-00` §5.1 DI1) and re-imports
the port bookkeeping that `dbdeployer` accumulated over years (`01-02` §4 I3).
*Revisit when*: a non-network way to induce CUBRID failover exists.

### Comparable DBMS Practice

The full matrix is `01-00-survey_overview.md` §5 and is not duplicated here.
Three findings bear on the choices above:

- **DI1** — no comparable tool ships a network partition, because all four
  isolate with processes and ports. This is what makes A5 a reject and the
  container backend a requirement, and it means the partition verb is the one
  part of this design with no model to copy.
- **DI2** — pointing at a locally built tree is documented in three of the four
  (`install_path`, `--binarypath`, `--{comp}.binpath`), so G2 sits inside
  precedent rather than extending it.
- **A hole, not a finding** — the legs were never asked about latency injection
  or about inducing two masters, because the series' fault question stopped at
  promote / demote / partition / kill / rejoin (`01-00` §3 D4). G7 therefore
  rests on no comparable evidence at all, and `01-05` §5 queues the targeted
  re-read that would fix it.
- **DI3** — provisioning's home is split four ways across the comparable set,
  so precedent cannot settle where this belongs; the answer came from the
  testkit and operator relationships instead (§9 OQ3, OQ4).

## 6. Trade-offs

**Containers cost startup latency and image plumbing; they buy the partition,
per-node hostnames, and the disappearance of port bookkeeping.** The measured
setup showed the cost is small — a stock base image, no Dockerfile — and
`01-02` §4 I3 shows the bookkeeping the alternative accumulates.

**Presets are fast to build and cheap to use, and they will need a migration.**
A declarative document arrives when the topology catalogue outgrows flags
(§9 OQ5); designing it first would spend the budget before the catalogue is
known.

**Bind-mounting a host build is free but couples the container base to the host
toolchain.** It worked because both sides were Ubuntu 24.04; a mismatched base
fails at load time. The provisioner should detect this rather than let a
developer debug a linker error (§7).

**Owning the engine's assembly traps makes the tool useful and makes it
brittle.** Encoding the `service start` / `heartbeat start` ordering and the
heartbeat reactivation sequence is most of the value; it also means an engine
change to those sequences breaks the tool silently.
**A tool that owns the configuration has to be able to write a wrong one — but
less often than expected.** OQ9 was run on 2026-08-27 and the tension mostly
dissolved: a *correctly* configured cluster reaches split brain, so the
headline anomaly needs no deviant config at all. What survives is narrow. The
second flavour (`ha_ping_hosts` unset) is still worth reproducing because it is
the default a real deployment starts from, and reproducing it means shipping a
configuration the provisioner otherwise fixes. The residual cost is that a
scenario's cluster is not always an example of how to configure CUBRID, and
`describe` (G5) has to say which.

## 7. Failure Modes

Triggers identified; handling deferred to the sibling repo's design.

- **Base-image / host toolchain mismatch** — a build produced on a different
  distribution fails to load in the container. Trigger: `--build` pointing at a
  tree built elsewhere.
- **Stale state after an interrupted run** — orphan containers, networks, and
  bind-mount directories from a provisioner that died mid-assembly.
- **Collision with an existing local cluster** — names, networks, or host ports
  already in use.
- **Engine sequence drift** — the ordering and reactivation rules the
  provisioner encodes change in a later CUBRID release.
- **Silent config divergence** — the provisioner writes a `cubrid_ha.conf` the
  engine accepts but that does not mean what the topology model said.
- **Seeding on the wrong completion signal** — `databases.txt` gains its entry
  *before* `createdb` finishes, so a provisioner that seeds the slave on that
  signal copies a database with a live transaction in it. Measured 2026-08-27:
  the slave's recovery reaches its UNDO phase and dies with `fetching
  deallocated pageid 705 of volume "/db/hadb"` → `LOG FATAL ERROR:
  log_recovery:locator_initialize`. Unlike the assembly traps in §1 this one
  produces a *corrupt slave* rather than a failed start.
- **An injected condition outlives its scenario** — a stalled `applylogdb`, a
  latency rule, or an `ha_ping_hosts` deviation left in place after the run, so
  the next scenario silently measures the previous one.
- **The heartbeat undoes the injection** — *ruled out 2026-08-27 (OQ7)*. Both
  replication processes were suspended for 30 s each and the heartbeat kept
  them listed as `registered` with unchanged pids and logged nothing: it
  monitors process existence, not progress. The inverse is now the risk worth
  naming — the heartbeat will not tell anyone that replication has stopped
  while the process is still alive.
- **The switch command never returns** — `cubrid heartbeat stop` completes its
  work and then blocks forever if the node's HA processes cannot be reaped
  (§4 layer 3). The operator sees a hang and cannot tell whether the failover
  happened; it had. Any tool driving this step has to bound it and decide on the
  observed roles rather than on the command's exit.
- **The replication view is empty across a role change** — a node that has just
  been demoted has no `db_ha_apply_info` row until its applier writes one, so
  the "is the target caught up" check returns nothing at the only moment a
  failback decision is ever made (observed twice, 2026-08-27). Trigger: any
  check that treats a missing row as zero lag.
- **The monitor reports the opposite of the truth** — a lag panel built on
  `db_ha_apply_info` alone shows a constant lag through an apply stall and a
  *falling* lag through a copy stall (measured, §9 OQ7). Trigger: any collector
  that treats `eof_lsa - final_lsa` as replication delay without a master-side
  reference.
- **Split brain the tool cannot end** — the state is entered but healing leaves
  both nodes claiming master, or resolves by demoting the node that holds the
  writes the scenario was about.

## 8. Rollout & Migration

**Phase 0 — baseline, spike, and the failback script.** The baseline exists:
the CBRD-26983 assembly was performed twice on 2026-08-18 and `01-05` §3
records the checks. The spike is that assembly written down as a script, which
is the honest floor for what the tool must beat. G8's failback script sits in
the same phase for a scheduling reason rather than a technical one: it needs
none of the tool, and the technical team's markup is an *input* to the phase-1
verb set. Writing it later would mean fixing the fault vocabulary before
hearing from the people who operate the thing. **Both exist as of 2026-08-27**
— the assembly is `lib.sh` `cs_up` and the script is `failback.sh` in the
harness (§10), the latter written against the measured fact that CUBRID has no
engine path back to the original master (OQ9 arm C). It is waiting to be sent.

**Phase 1 — CLI, container backend, HA preset.** G1, G2, G3, G4. The `ha`
preset with two nodes, `--build` and released-version selection, the four fault
verbs role-addressed, and tier 1 + tier 2 inspection.

**Phase 2 — anomalies, observability, reproducibility, testkit integration.**
G5, G6, G7, G9: `describe` as a shareable artifact, the non-interactive surface
testkit consumes, the two condition verbs, and interval sampling of
`db_ha_apply_info` with retention. G7 lands here rather than in phase 1 because
a condition needs heal semantics that OQ7 has not settled, while phase 1's four
verbs are events that need nothing beyond the container backend.

**Phase 3 (skeletal).** Web front end over the same surface; broader topology
catalogue (replica, broker tiers); a Kubernetes backend and `cubrid-operator`
inclusion for operational testing; the tier-3 monitoring seam once N20 and
N64 W1 land.

**Rollback.** The project is a separate repository with no engine change.
Abandoning it costs the repository and nothing else.

**Graduation triggers.** `cubrid-testkit` provisioning through it rather than
its own path; a CUBRID bug reproduced by a second person from a `describe`
artifact; the roadmap entry shrinking to a thin pointer once the sibling repo
carries the design (CLAUDE.md §2.5).

## 9. Open Questions

**OQ1 — What to take from `cubrid-contrib/sandbox`, and whether to absorb it.**
*Owner*: this project. *Verification*: reading sandbox's `docker run` invocation
against the phase-1 backend. Three conventions transfer and are already folded
into §4 layer 3 — run as the invoking user, mount the tree at an identical path,
and the `<noun> <verb>` command shape. What stays open is whether a build shell
is simply a one-node topology with a build role, in which case this project
eventually subsumes sandbox's use case; §5 A2 carries the reject and its
revisit condition.

**OQ2 — Monitoring depth, and what it costs.** *Owner*: this project for tiers
1–2; N64 W1 for tier 3. *Verification*: whether any collection step parses
human-formatted output — a conformance check N64 G1 already specifies.

| Tier | What it shows | Cost | Depends on |
|---|---|---|---|
| T1 container | process liveness, stdout/stderr, CPU/memory/disk per container | ~free — Docker API and the engine's own log files | — |
| T2 topology | HA state per node, master/slave view, replication delay, applied LSA | small, **but larger than this row assumed** — `db_ha_apply_info` is a catalog view, so the applied position comes over SQL with no parsing, and `cubrid changemode` / `cubrid heartbeat status` are one-line stable text. What the view cannot give is *delay*: it is written by `applylogdb`, so it cannot report a stall of the process writing it, and it reports a falling lag during a copy stall (measured, OQ7). A true delay needs the master's append position as a second source | — |
| T3 engine internals | buffer/lock/log/SQL counters, wait events, statement statistics | expensive **today** — `cubrid statdump` offers only `--output-file` and `--interval`, so any consumer parses text. CMS already does this and narrows 64-bit counters to 32; `cubrid-exporter` died in 2020 querying statements the engine does not have | **N20**, then **N64 W1** |

TiDB ships Grafana with a playground because its components already expose
metrics endpoints (`01-04` §4 I1) — the distance between that and CUBRID is the
contract, not the provisioner. §4 layer 5 therefore builds T1 and T2 and leaves
T3 as a seam.

**OQ3 — Relationship with `cubrid-testkit`. → Decided 2026-08-18.**
**testkit consumes this tool.** This project owns provisioning; testkit owns
suites, dispatch, and reporting. The consequence is G6: the surface testkit
calls has to be non-interactive and machine-readable from phase 2, which is
earlier than a CLI-only tool would need it. `01-01` §4 I1 records the failure
mode this avoids — PostgreSQL's harness owns every primitive and surfaces none
of them.

**OQ4 — Relationship with `cubrid-operator`. → Reframed 2026-08-18.**
Not a dependency and not a shared substrate for now. The operator serves
general users running CUBRID on Kubernetes; this tool serves a developer
setting up an environment. The connection is later and narrower: when a
developer uses this tool for **operational testing**, the operator becomes
something the sandbox can include — a component under test, or a second
backend. *Owner*: deferred. *Verification*: the first operational-testing use
case that actually wants it. Nothing in phase 1 depends on the answer, and §5
A4 carries the reject for building on Kubernetes now.

**OQ5 — Topology catalogue.** *Owner*: this project. *Verification*: the first
topology a user asks for that the preset vocabulary cannot express. HA two-node
is phase 1; replica nodes, broker/CAS tiers, shard configurations, and CDC
consumers each bring their own configuration surface and their own fault verbs.
The migration from presets to a declarative document (§6) is triggered here.

**OQ6 — Web UI phase.** *Owner*: this project. *Verification*: whether the
phase-2 non-interactive surface is stable enough to build a second consumer on.
Deferred to phase 3 so that the UI is a client of the same surface testkit uses
rather than a parallel path. The boundary to N64's Web Management Console is
fixed by §3: that one manages real clusters, this one manages development
environments.
**OQ7 — How is lag injected, and does the heartbeat permit it? → Answered
2026-08-27. Suspend a stage; and yes, it permits it.** One cluster, seven
phases (baseline, apply stall, drain, copy stall, drain, `netem`, drain).

**Stage suspension is the default mechanism.** It is the only one that
separates the two stages the engine reports separately, it is instant and
reverses on resume — and the heartbeat does not interfere. After 30 s with each
process suspended, `cubrid heartbeat status` still listed the same pids in
`state registered` and the master log carried no `[Failback]` or `[Failover]`
line. The heartbeat monitors process *existence*, not progress; the 10 ms
reaction the CBRD-26983 session saw was to a *dead* process. **Network delay
stays as the realism mechanism**: `netem delay 200ms` grew the lag from 52,771
to 68,201 pages in 30 s and had not drained 30 s after removal, but it cannot
say which stage it is slowing. So: one verb, a mechanism argument, suspension
as the default.

The run's real yield was elsewhere and is folded into G9 and §4 layer 5: the
observable this project had chosen **cannot see either stall**. The view is
written by `applylogdb`, so suspending the applier freezes all twenty-six
columns at a constant healthy-looking lag; and suspending the copier makes the
reported lag *fall* while replication is fully stopped. `applyinfo -r <master>`
saw the same moment correctly at 48,343 pages behind, and printed
`Estimated Delay : -` — the zero-`process_rate` first sample, observed rather
than read.

*Caveat on the numbers*: the load was heavy enough that a single `applylogdb`
never kept up (27,786 pages of lag before any injection; 3.44 M master rows
against 1.68 M on the slave at the end), so the injected figures are not
calibrated lag measurements. The mechanism questions are settled; "what does
200 ms cost" is not. *Artifacts*: `findings/oq7-lag.md`.

**OQ8 — What does the technical team actually require of failback?** *Owner*:
this project, blocked on a party outside it. *Verification*: the marked-up
script comes back. Nobody in this repo knows today, and the gap is specific:
the engine's own failback is *automatic* once it is diagnosed — detecting
multiple masters queues `HB_CJOB_FAILBACK` with no operator in the loop
(`master_heartbeat.c:866-895`) — so whatever the technical team does by hand is
the part the engine does not do. Deciding when it is safe, dealing with the
divergence that accumulated on the node that was wrong, and ordering the
restarts are the candidates, and they are guesses. The G8 script is the
instrument: it encodes this project's guess so the team corrects it, and the
corrections are the requirement set. Until then the split between what the tool
does and what the operator decides is unfounded, and G8 declines to pick it.

**Sharpened by running it, 2026-08-27.** The script works — original master back
in 2 s, no rows lost — so the open part is judgement, not mechanism, exactly as
assumed. Two of its five questions changed shape. Question 1 ("what counts as
caught up") now has to survive the evidence being *absent*: a just-demoted node
has no `db_ha_apply_info` row at all, and both runs printed `<none>` at the step
that asks. Question 3 ("is `heartbeat stop` what you use") gained a reason to
doubt the command itself: it hangs indefinitely when the node's HA processes
cannot be reaped, after having already succeeded (§4 layer 3), so an operator
watching it cannot tell a completed switch from a stuck one.
*Artifacts*: `findings/failback.md`.

**OQ9 — Does split brain need a deliberately broken configuration? → Answered
2026-08-27. No.** Run on a two-node containerised pair in three arms, cutting
the link with a per-node blackhole route so that the master's reachability to
the ping host could be varied independently of its reachability to the peer —
a distinction `docker network disconnect` cannot express.

| Arm | `ha_ping_hosts` | What was cut | Outcome |
|---|---|---|---|
| A | a third host, reachable from both | the peer link only | **two masters, 9 s** |
| B | unset (the default) | the peer link only | **two masters, 13 s** |
| C | a third host, cut from the master too | link **and** the master's ping path | clean failover, 9 s |

Arm A is the answer: the *documented* configuration reaches split brain. The
asymmetry is in one function — a master cancels its failback when
`ping_try_count == 0` **or** the ping succeeded, while a slave cancels its
failover only when it tried and failed (`master_heartbeat.c:1042-1054`) — so a
surviving ping host satisfies both cancel-nots at once and a quorum of one
votes for whoever asks. A logged
`[Failback] [Cancelled] Ping check succeeded … determining that it is not a
network partition`; B logged `[Failback] [Cancelled] No hosts are registered in
ha_ping_hosts …`; C is the control and demoted itself with
`[Failback] [Success] Current node has been successfully demoted to slave`.

Three consequences. (i) §4 layer 2's configuration deviation is **optional** —
the anomaly comes from the fault, not the config — and §6's trade-off shrank
accordingly. (ii) The two flavours are indistinguishable by outcome and
distinguishable by cancel reason, so a scenario assertion belongs on the log
line. (iii) **Recovery differs by arm and that is what G8 is about**: arm A
healed itself in under 45 s (`[Failback] [Diagnosis] Multiple master nodes
(n2, n1) are detected` → demotion, original roles restored, because priority
decides who steps down), while **arm C never recovered** — 45 s after the
network healed the roles were still swapped, and they stay swapped. CUBRID has
no engine path back to the original master after a clean failover.
*Artifacts*: `findings/oq9-splitbrain.md` in the harness (§10).

## 10. References

**Methodology.** `$KB_ROOT/knowledge/methodology/design-doc.md`;
`$KB_ROOT/knowledge/methodology/comparison-matrix.md` (the empty-cell rule the
survey matrix follows); roadmap `CLAUDE.md` §2.1 / §2.3 / §2.7.

**This project's survey series.** `01-00-survey_overview.md` (axes D1–D5,
matrix, DI1–DI3) · `01-01-survey_postgresql.md` · `01-02-survey_mysql.md` ·
`01-03-survey_mongodb.md` · `01-04-survey_tidb.md` ·
`01-05-survey_cubrid-gap-and-measurement.md` (gaps G1–G7, measurement plan,
prerequisite order).

**Roadmap neighbours.** `10-selected/N64-cubrid-ops/` (§1 names this project's
gap; W1 owns the tier-3 contract) · `00-pending-review/N20-utility-json-output/`
(hard prerequisite for tier 3) · `30-graduated/cubrid-testkit/` (consumer,
OQ3) · `30-graduated/spatial/` and `cubrid-testkit` as the sibling-repo pattern
this project follows.

**CUBRID code.** `src/connection/server_support.c:1558` (a non-heartbeat caller
cannot drive active→standby), `:1594-1612` (HA state transition table) ·
`src/object/schema_system_catalog.cpp:111` (`db_ha_apply_info` catalog view),
`src/object/schema_system_catalog_install.cpp:1956-1988` (its twenty-six
columns) · `src/connection/heartbeat.h:62-70` (the three heartbeat-managed
process types) · `src/executables/master_heartbeat.c:866-895` (split-brain
diagnosis and automatic failback), `:1042-1054` and `:1110-1135` (the ping
check and its cancel conditions) · `src/executables/util_cs.c:3893-3924` and
`src/transaction/log_applier.c:7456-7478` (`cubrid applyinfo`'s two delays, and
why the first sample prints `-`) · `src/executables/utility.h:1599-1602`
(`cubrid statdump` output options), `:1634-1646` (`cubrid applyinfo` options —
`-L`, `-p`, `-r`, `-a`, `-v`, `-i`; none of them a format).

**This project's harness**, in this repository at [`../harness/`](../harness/)
(it was `/data/workspace/for-plan/cluster-sandbox/` before graduation): `lib.sh` (the four-step
assembly), `oq9-splitbrain.sh` (three arms), `oq7-lag.sh` (six phases),
`failback-demo.sh` (drives a cluster into a failed-over state and runs the
script against it), `failback.sh` (the G8 artifact),
[`findings/oq9-splitbrain.md`](findings/oq9-splitbrain.md),
[`findings/oq7-lag.md`](findings/oq7-lag.md),
[`findings/failback.md`](findings/failback.md), and captured run output under
`harness/results/`. Derived from
N54's WU-51b harness `for-plan/importdb/m5/ha51b-docker`, which established the
container substrate.

**External.** `CUBRID/cubrid-contrib` (sandbox, docker_for_ctp) ·
`CUBRID/cubrid-operator` · CBRD-26983 / PR #7720 (the verification that
produced §1's baseline).

## 11. Review Log

- 2026-08-18 — Claude (seed registration). Context & Problem only, motivated by
  the CBRD-26983 HA verification measured the same day; alternatives, comparable
  practice, and the survey series park until selected entry. Framing inputs
  supplied by hgryoo: users are engine developers + QA + external contributors;
  topology and build source (released version or local build directory) are
  configuration; monitoring wants engine-internal depth subject to the cost
  recorded in §9 OQ2. Slug settled the same day as `cluster-sandbox`, over
  `dev-environment-kit` and `devbox`; HA is deliberately absent from the name
  because the topology catalogue is meant to be wider than HA. Placement settled
  the same day: own repository under `cubrid-systems`, not a `cubrid-contrib`
  addition — so §9 OQ1 keeps only the inheritance and boundary questions.
- 2026-08-18 — Claude (00-pending-review → 10-selected). Promoted on hgryoo's
  decision to begin the investigation. §2.1 framing gate: pain point and
  mine-able references supplied in §1 and `sources:`; audience, output shape,
  and anti-patterns take the §2.2 carryover values. Survey series opened at
  [`01-00-survey_overview.md`](01-00-survey_overview.md); the comparable set
  proposed there awaits confirmation before the per-system legs are authored.
  §2–§8 and §10 stay unwritten until the series closes.
- 2026-08-18 — Claude (survey series complete). Comparable set confirmed by
  hgryoo as PostgreSQL / MySQL / MongoDB / TiDB; legs `01-01`–`01-04` and the
  CUBRID synthesis `01-05` authored, and the `01-00` matrix filled from them.
  Three findings bear on this document directly: containers are a requirement
  rather than a preference, because no comparable tool ships a network
  partition and CUBRID's failover induction needs one (`01-00` §5.1 DI1); the
  local-build-path requirement sits inside precedent (DI2); and §9 OQ3/OQ4
  cannot be settled by precedent, since all four placements have working
  examples (DI3). §4 Proposed Design is now unblocked (CLAUDE.md §2.7 step 7).
  Figures (§2.9) remain outstanding.
- 2026-08-18 — Claude (deepen to full 11 sections). hgryoo settled the two
  relationship questions: **testkit consumes this tool** (OQ3), and the
  **operator is a later inclusion for operational testing**, not a dependency
  or a shared substrate (OQ4). §2–§8 and §10 authored on that basis; the seed's
  "Anticipated direction" paragraph migrated into §4 and §5 per CLAUDE.md §6a.
  §5 carries five alternatives (A1 do-nothing, A2 extend contrib/sandbox,
  A3 provisioning inside testkit, A4 Kubernetes-first, A5 process isolation),
  each with a revisit condition. Remaining: SVG figures (§2.9), and the
  incubating move once the sibling repo exists.
- 2026-08-27 — Claude (second requirement set: reproducing anomalous states).
  hgryoo supplied five development-time repro requirements — inter-node lag,
  split brain, a semi-automatic failback script, replication monitoring, and
  replication tracking. Folded in as §2 G7–G9, with §4 layers 2 / 4 / 5
  extended, one new trade-off (§6), three new failure modes (§7), a Phase 0
  slot for the failback script (§8), and §9 OQ7–OQ9. Grounded against the
  engine the same day rather than asserted: the replication pipeline is two
  heartbeat-managed processes (`heartbeat.h:62-70`), which is why lag needs a
  stage target; split brain is a named diagnosis that queues an *automatic*
  failback (`master_heartbeat.c:866-895`), which is why the interesting half
  of failback is the operator's and not the engine's; `db_ha_apply_info`
  carries twenty-six columns rather than a delay scalar
  (`schema_system_catalog_install.cpp:1956-1988`); and `cubrid applyinfo`
  prints its delay as text whose first sample is always `-`
  (`util_cs.c:3893-3924`, `log_applier.c:7456-7478`), so tracking reads SQL
  and not that. One code read moved a design assumption: the ping check
  cancels a master's failback both when no ping host is registered and when
  the ping succeeds (`master_heartbeat.c:1042-1054`), so a *correctly*
  configured cluster reaches two masters whenever the ping host survives the
  partition — OQ9 carries the claim as read-not-run, and §6's
  correct-versus-inducible trade-off is written down as smaller than it looked.
  The requirement set is hgryoo's, not the technical team's; OQ8 records that
  theirs is still unknown and names the G8 script as the instrument for
  getting it. `01-05` gained gaps G8–G11 with measurement rows. Figures (§2.9)
  remain outstanding.
- 2026-08-27 — Claude (OQ7 and OQ9 run; the G8 artifact written). The two
  questions the requirement set opened the same day were answered by running
  them on a two-node containerised HA pair, in a harness derived from N54's
  WU-51b rig (§10). **OQ9 — no, split brain does not need a broken
  configuration**: three arms, and the correctly configured one reached two
  masters in nine seconds, because a master cancels its failback when the ping
  succeeds while a slave cancels its failover only when its own ping fails, so
  a surviving ping host is a quorum of one that votes for whoever asks. §4
  layer 2's configuration deviation dropped from required to optional and §6's
  trade-off shrank with it. The control arm demoted cleanly and then **stayed**
  demoted, which is the fact G8 rests on: CUBRID's `[Failback]` means "demote
  myself, another master exists", and there is no engine path back to the
  original master after a clean failover. **OQ7 — suspend a stage, and the
  heartbeat permits it**: both replication processes stayed `registered` with
  unchanged pids through 30 s suspensions and the master log said nothing,
  because the heartbeat watches existence rather than progress. The run's
  larger yield was a correction to this document rather than a confirmation of
  it: **G9's observable does not observe what it was chosen for.**
  `db_ha_apply_info` is written by `applylogdb`, so a stalled applier freezes
  all twenty-six columns at a constant healthy-looking lag, and during a copy
  stall the reported lag *falls* while replication is entirely stopped
  (49,544 → 38,576 pages). G9's acceptance and §4 layer 5 now require a
  master-side reference; §7 lost the heartbeat-interference failure mode and
  gained the monitor-lies one. Three provisioner requirements fell out of
  simply running it, and are in §4 layer 3 and §7: the partition has to be a
  **route-level** cut (an interface cut cannot express "keep the ping host"),
  `ping` has to be **in the image** (`hb_check_ping` shells out to it, and its
  absence is read as a failed ping), and seeding must wait for a **`createdb`
  completion signal** rather than for `databases.txt`, which appears first and
  yields a slave that dies in recovery. G8's script exists as `failback.sh` —
  five decision points, each naming what this project does not know — and is
  waiting to be sent to the technical team; OQ8 is still open and only they can
  close it. `01-05` G8–G11 and their measurement rows carry the same results,
  and `cross-cutting.md` C-054 gained the monitoring finding, which is N64's
  problem more than this project's. Figures (§2.9) remain outstanding.
- 2026-08-27 — Claude (G8 script written, run, and validated). `failback.sh`
  exists and works: driven against a pair that had genuinely failed over, it
  restored the original master in **2 s** with `rc=0` and no row loss, so the
  operational return trip is mechanically possible with commands CUBRID already
  ships. That settles the half of G8 that was never in doubt and isolates the
  half that is — five decision points where this project is guessing, and only
  the technical team can close them (§9 OQ8, still open). Running it produced
  two findings the reading had not. **`cubrid heartbeat stop` hangs forever when
  the node's HA processes cannot be reaped**, *after* the deactivation has
  already succeeded and the peer has been promoted: `us_hb_deactivate` polls
  "is any `cub_server` running" on a one-second sleep
  (`util_service.c:3995-4004`) and a zombie answers yes. In a container this
  means `--init`, now a §4 layer 3 requirement alongside `NET_ADMIN`, and it
  means any tool driving that step must be bounded and decide on the observed
  roles rather than the command's exit — §7 carries both. **And the check the
  operator most needs is empty exactly when they need it**: a just-demoted node
  has no `db_ha_apply_info` row until its applier writes one, and both runs
  printed `<none>` at the step asking whether the target is caught up. With the
  OQ7 result this makes three distinct ways that view misleads — frozen during
  an apply stall, falling during a copy stall, absent across a role change — so
  §9 OQ2's tier-2 cost row, §2 G9, §4 layer 5 and `01-05` G11 were all corrected
  rather than merely annotated. §9 OQ8's question 1 is now "what counts as
  caught up, *when the evidence is missing*". `findings/failback.md` carries the
  run.
- 2026-08-28 — Claude (graduation). `cluster-sandbox` left the roadmap for its
  own repository under `cubrid-systems`, on hgryoo's decision that it now
  belongs to the organization's project set. This file is the migrated source
  of truth; the roadmap shrinks to a thin pointer per its CLAUDE.md §2.5, and
  the survey series, the figures, and the harness moved with it. Nothing was
  changed in the move beyond the frontmatter, the head note, and §10's harness
  path.
