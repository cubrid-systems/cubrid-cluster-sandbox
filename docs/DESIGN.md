---
title: cluster-sandbox — Design
category: design
project: cluster-sandbox
status: phase 0 complete
sources:
  - CUBRID Ops (cubrid-systems roadmap) §1 — "Developer Experience (Docker/CLI/SDK) is out of scope here": the one place the organization had named this gap, and the only one of its four recorded candidates left without an owner
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
summary: A CLI-first provisioner that stands up a CUBRID topology in containers from a declarative configuration — node count and roles, engine version or a local build directory, parameters — so that engine developers, QA, and external contributors can reproduce a multi-node setup without hand-assembling it. Seed motivated by a measured case: verifying one HA question for CBRD-26983 took a hand-built two-node cluster, and every step of that assembly is mechanical. A web front end over the same API and per-container monitoring are part of the intended shape; the monitoring depth is the open question with a real cost boundary, because engine-internal counters are text-only until N20 lands. A second requirement set (2026-08-27) extends the scope past assembly to the states a developer needs to reproduce — inter-node lag, split brain, a semi-automatic failback script, and replication monitoring and tracking — which are conditions rather than events and land as G7-G9. Phase 0 closed 2026-08-27: the manual assembly runs as a script and the two questions this design could not settle by reading were answered by measurement (see `findings/`).
created: 2026-08-18
updated: 2026-08-28
lang: en
tags: [design, developer-experience, docker, containers, ha, test-environment, provisioning, monitoring, replication, fault-injection, cli, cluster-sandbox]
---

> **What this is.** The design document for `cluster-sandbox` — the problem, the
> goals, the architecture, and the decisions behind them. It is the starting
> point for the repository; the comparable-engine evidence it rests on is in
> [`survey/`](survey/), and what running the design actually showed is in
> [`findings/`](findings/).
>
> **Phase 0 is complete and the architecture below is still a sketch.** §4 names
> five layers and their boundaries; it does not yet specify the command surface,
> the topology model, the verb semantics, or the inspector contract. That work is
> [`design/`](design/), and it is where the project goes next.

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

**Home.** This repository, under the `cubrid-systems` organization, following
the pattern of `cubrid-testkit` and `cubrid-spatial`. The organization's roadmap
keeps a one-page pointer for cross-project purposes; the design lives here.

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
(`survey/01-00-overview.md` §5.1 DI2).

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
(`survey/01-03-mongodb.md` §4 I3).

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

Five layers, each answering one of the decisions the survey found every
provisioner has to make (`survey/01-00-overview.md` §3), **plus a sixth
component the survey did not predict** (item 6). This section fixes the
**boundaries**; the interfaces across them are specified in
[`design/`](design/).

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

6. **Load driver — not one of the survey's five decisions, and required
   anyway.** No comparable tool ships one, which is why the survey never
   surfaced it; and the two requirements that matter most in phase 2 cannot be
   met without it. The field's reproduction of its own failover loop is *host
   contention* — a compile with 20–40 threads — not database traffic, and the
   threshold sweep needs a load identical on every run or it measures the load's
   variance instead of the threshold. Two kinds, therefore, deliberately not one
   scale: transactions against the master, and contention on the node. The
   contract that makes it a component rather than a script is that the driver
   **states a rate, holds it, and reports when it could not** — this project's
   own lag figures are uncalibrated precisely because its phase-0 driver was
   open-loop, so every injected number is a delta on an already-saturated
   pipeline ([`design/06-load.md`](design/06-load.md)).

   Its artifact pair is the **run record** — `describe` says what cluster this
   was, the record says what happened to it, including for every role change
   both the measured interval and the one the settings in force predict. It
   exists because a threshold-caused switchover may leave nothing in the engine
   log, which the field has asked to have fixed and which is not fixed yet, so
   until then the *inputs* are the evidence
   ([`design/07-record.md`](design/07-record.md)).

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

The full matrix is `survey/01-00-overview.md` §5 and is not duplicated here.
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

**What "it worked" will look like.** `cubrid-testkit` provisioning through this
tool rather than through its own path, and a CUBRID bug reproduced by a second
person from nothing but a `describe` artifact. Neither has happened yet; both
are phase-2 outcomes rather than phase-2 deliverables, which is why they are
here and not in §2.

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
200 ms cost" is not. *Artifacts*: `findings/replication-lag.md`.

**OQ8 — What does the technical team actually require of failback?** *Owner*:
this project, blocked on a party outside it. *Verification*: the marked-up
script comes back.

**Narrowed 2026-08-28 by searching the internal tracker**
([`requirements/01-failback-field-evidence.md`](requirements/01-failback-field-evidence.md)).
The engine's own failback is automatic once diagnosed, and after a clean
failover there is no engine path back to the original master
(`master_heartbeat.c:866-895`, and measured) — so whatever the team does by hand
is the part the engine does not do. The tracker turns out to answer the
mechanical half of that and none of the judgement half.

*Answered by the tracker*: the rejoin path is the online rebuild script
`ha_make_slavedb.sh`, and it has a long trouble history worth reproducing; the
operational alarm is `fail_counter`, whose diagnostics the team has separately
asked to be improved; and the failback that actually costs them is not a
deliberate return at all but a **loop** — four sites reporting ten or more
failover/split-brain/failback cycles a day under load, with no network fault
(the failover-loop report, 2016).

*Still unknown, and the reason to send the script*: the threshold for "caught up
enough"; whether and how write traffic is quiesced first; who authorises a
failback and on what evidence; and whether the original master is preferred at
all, or whether sites simply run on whichever node holds the service.

`harness/failback.sh` encodes this project's guess and goes to the team with the
four edits listed in §7 of that document — the tracker already answers what they
would otherwise be asked, and this gets one round of their attention.

**A second requirement arrived unasked** and is not about failback: a 2021
ticket wants the settings that can trigger a switchover documented and
**validated in a user's environment**, and says explicitly that developers
cannot do it. That is a commission for this tool;
[`ROADMAP.md`](ROADMAP.md) M2.5 carries it.

**Reframed 2026-08-28 — the question has been asked in the wrong word.** A second
pass over the tracker
([`requirements/02-ha-role-transition-field-evidence.md`](requirements/02-ha-role-transition-field-evidence.md))
searched its vocabulary rather than this project's. **`failback` means
demotion** — "Fail Back은 마스터 노드가 슬레이브 노드가 되는 것", from the
team's own HA study notes, which is also what every `[Failback]` line this
project measured says. The tracker has
**no term** for returning service to the original master, which is consistent
with there being no engine path for it: nobody files tickets about an operation
that has neither a mechanism nor a name. So OQ8 stands, but it must be asked as
*return-to-original-master*, and that is a fifth edit to `failback.sh` ahead of
the four already listed.

The same pass found the switchover-threshold work is **not missing but stalled**:
a hidden-parameter test, open since 2022 and blocking the settings ticket above,
measured a role change at 8–11 s against an arithmetic 2.5 s, found
`ha_max_heartbeat_gap` apparently inert, and reported an **Active-Active
window** after the network heals — all three unresolved because a VM pair could
not separate engine behaviour from test artefact. And it found a failover that
**stops half-finished**: a node held `to_be_active` from 01:00 to 09:00,
refusing writes, because the applier was looking for a deleted archive log.
Both are constructible here, and they change §4 layer 4 and layer 5 rather than
adding to them.

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
*Artifacts*: `findings/split-brain.md` in the harness (§10).

**OQ10 — is the reported Active-Active window real?** *Owner*: this project.
*Verification*: reproduce the hidden-parameter test's configuration and watch
the direction of
replication after the heal. The two split-brain flavours this project measured
give two masters with replication running **one** way; the test reports
bidirectional sync for the length of `ha_calc_score_interval_in_msecs`. If that
holds, it is a third anomaly rather than a third flavour of one, and
[`design/04-faults.md`](design/04-faults.md) §5 carries it as an unverified claim
until then.

## 10. References

**This project's survey series.** `survey/01-00-overview.md` (axes D1–D5,
matrix, DI1–DI3) · `survey/01-01-postgresql.md` · `survey/01-02-mysql.md` ·
`survey/01-03-mongodb.md` · `survey/01-04-tidb.md` ·
`survey/01-05-cubrid-gap.md` (gaps G1–G7, measurement plan,
prerequisite order).

**Related work in the organization.** *CUBRID Ops* — named this project's gap,
and owns the operational metrics contract this design leaves a seam for (§9 OQ2);
the replication-observability finding in `findings/replication-lag.md` is an input
to it. *Utility JSON output* — a hard prerequisite for tier-3 monitoring.
[`cubrid-testkit`](https://github.com/cubrid-systems/cubrid-testkit) — the
consumer (§9 OQ3). All three are tracked in the organization's roadmap.

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
[`findings/split-brain.md`](findings/split-brain.md),
[`findings/replication-lag.md`](findings/replication-lag.md),
[`findings/failback.md`](findings/failback.md), and captured run output under
`harness/results/`. Derived from
N54's WU-51b harness `for-plan/importdb/m5/ha51b-docker`, which established the
container substrate.

**External.** `CUBRID/cubrid-contrib` (sandbox, docker_for_ctp) ·
`CUBRID/cubrid-operator` · CBRD-26983 / PR #7720 (the verification that
produced §1's baseline).

## 11. Decision log

What was decided, and what changed a decision. Ordered oldest first.

**2026-08-18 — the shape of the thing.** Users are engine developers, QA, and
external contributors. Topology and build source are *configuration*, not
variants of the tool. The name deliberately does not say HA, because the
topology catalogue is meant to grow wider than HA (`dev-environment-kit` and
`devbox` were the alternatives). Monitoring wants engine-internal depth, subject
to the cost recorded in §9 OQ2.

**2026-08-18 — the comparable set, and what it settled.** PostgreSQL, MySQL,
MongoDB, TiDB, plus the CUBRID gap analysis (`survey/`). Three findings bear on
§4 and §5 directly. Containers are a **requirement, not a preference**: no
comparable tool ships a network partition, because all four chose process
isolation, and CUBRID's failover induction needs one. Pointing at a locally
built tree sits *inside* precedent — three of the four document it — so G2 is
ordinary rather than exotic. And provisioning's home is split four ways across
the set, so precedent could not settle where this belongs; that answer came from
the testkit and operator relationships instead.

**2026-08-18 — the two relationship questions.** **`cubrid-testkit` consumes
this tool** (§9 OQ3): this project owns provisioning, testkit owns suites,
dispatch, and reporting. The consequence is G6 — the surface testkit calls has
to be non-interactive and machine-readable from phase 2, earlier than a
CLI-only tool would need it. **`cubrid-operator` is a later inclusion, not a
dependency** (§9 OQ4): it serves production Kubernetes, this serves a developer
setting up an environment; the connection arrives when someone wants operational
testing.

**2026-08-27 — the second requirement set.** Reproducing anomalous states, not
only assembling a cluster: inter-node lag, split brain, a semi-automatic
failback script, replication monitoring, replication tracking. Folded in as
G7–G9, and grounded against the engine rather than asserted — the replication
pipeline is *two* heartbeat-managed processes, split brain is a named diagnosis
that queues an **automatic** failback, and `db_ha_apply_info` is twenty-six
columns rather than a delay scalar.

**2026-08-27 — split brain needs no misconfiguration** (`findings/split-brain.md`).
Three arms. A correctly configured cluster reached two masters in **9 seconds**,
because a master cancels its failback when its ping *succeeds* while a slave
cancels its failover only when its own ping *fails* — a surviving ping host is a
quorum of one that votes for whoever asks. §4 layer 2 had assumed the anomaly
needed a deliberately wrong configuration; it does not, and the deviation
dropped from required to optional. The control arm demoted cleanly and then
**stayed** demoted, which is the fact G8 rests on: there is no engine path back
to the original master after a clean failover.

**2026-08-27 — lag is injected by suspending a stage, and the heartbeat permits
it** (`findings/replication-lag.md`). Both replication processes stayed
`registered` with unchanged pids through 30-second suspensions and the master
log said nothing: the heartbeat watches process *existence*, not progress. The
same run **corrected this document**. `db_ha_apply_info` is written by
`applylogdb`, so it cannot report a stall of the process that writes it — every
column freezes at a constant, healthy-looking lag — and during a *copy* stall
the reported lag **falls** while replication is entirely stopped. G9's
acceptance and §4 layer 5 now require a master-side reference, and §7 lost its
heartbeat-interference mode and gained a monitor-lies mode.

**2026-08-27 — the failback script works, and the policy around it is the open
question** (`findings/failback.md`). Driven against a pair that had genuinely
failed over, it restored the original master in **2 seconds** with no row loss.
Two more findings: `cubrid heartbeat stop` hangs when the node's HA processes
cannot be reaped — hence `--init` alongside `NET_ADMIN` in §4 layer 3 — and a
just-demoted node has no `db_ha_apply_info` row at all, which is the third
distinct way that view misleads.

**2026-09-02 — Go, and the argument that reversed.** `design/ADR-001` is
accepted: Go for the provisioner, shell for anything an operator reads, edits or
runs on a real host. The draft five days earlier proposed Python and rejected Go
on one argument — Go wins on distribution and loses on who can fix it, because
nobody in this project's ecosystem writes it. `cubrid-operator` was already Go;
`cubrid-testkit`, the consumer G6 exists for, accepted Go the same week, on
drivers that read almost identically to this project's. The projects share a
maintainer, so the "only Go author in their week" turned out to be the same
developer, weekly. One constraint comes with it: **the boundary with testkit
stays the CLI and its JSON, not a shared package** — sharing a language must not
convert a process boundary into a build-time dependency. M1.1 is the validation
slice.

**2026-08-28 — load and the record are components, not scenario furniture.**
Designing the six gaps the requirements pass left produced one architectural
change and four vocabulary ones. §4 gains a **sixth component**, the load
driver, with a rate contract; and the **run record** becomes an artifact
alongside `describe`, on the principle that a measurement which cannot state its
inputs is not a measurement — the field's four-year-old stalled test is the
evidence for that, not a hypothetical. In the vocabulary: `quiesce` is an
operational condition rather than a fault, and its mechanism is the broker's
`ACCESS_MODE`, which means the `ha` preset gains an optional broker because a
cluster without one has no door to close; `failcount` is the first fault whose
reversal is a **repair** (`ha resync`, three paths) rather than a toggle;
`ping-unavailable` separates "cannot reach the ping host" from "cannot ask at
all"; and `--set-hidden` resolves a contradiction the previous round created —
the three parameters that decide when a failover happens are absent from
`paramdump`, so a rule that refuses every key it cannot look up refused the
entire subject of M2.5.

**2026-08-28 — the word was wrong.** `failback` means *demotion* to the engine
and to the technical team: their own HA study notes say so, and so does every
`[Failback]` log line this project captured. The operation this project has been
calling failback — returning service to the original master — has no name in the
tracker and no engine path, and the two facts are the same fact. §9 OQ8 keeps
its substance and changes its vocabulary; `design/04-faults.md` §7 is retitled.
Found by searching the tracker's language instead of this project's, which also
turned up the switchover-threshold measurement — stalled on reproducibility
rather than knowledge, now OQ10 and M2.5's acceptance target — and a field
report of `to_be_active` held for eight hours, which makes it a role the
inspector reports rather than a transition it waits out.

**2026-08-27 — three requirements that only appeared by running it.** The
partition must be a **route-level** cut, because cutting an interface cannot
express "cut the peer but keep the ping host". **`ping` must be in the image**:
`hb_check_ping` shells out to it, and its absence returns 127, which the caller
reads as a failed ping — so an image without it makes every master demote itself
on any heartbeat loss. And **seeding must wait for a `createdb` completion
signal**, not for the `databases.txt` entry, which appears first and yields a
slave that dies in recovery.
