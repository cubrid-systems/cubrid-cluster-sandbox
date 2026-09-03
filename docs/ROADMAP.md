---
title: cluster-sandbox — Roadmap
category: roadmap
project: cluster-sandbox
summary: Phases, milestones and current position. Phase 0 is complete; phase 1 is the CLI, the container backend and the HA preset. The one thing blocking design decisions rather than implementation is what the technical team requires of the return to the original master — a question that has to be asked in the team's own vocabulary, because "failback" already means something else to them.
created: 2026-08-28
updated: 2026-09-03
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

**The last unmeasured row of the split-brain table is measured.** The
Active-Active window after a healed partition is real and is as long as
`ha_calc_score_interval_in_msecs` — about 12 s at 15000 against about 1 s at the
default, three runs each. What it leaves is not the reported "data syncing both
ways" but a one-directional merge and a permanent divergence that every gauge
calls healthy ([`findings/active-active-window.md`](findings/active-active-window.md)).

**The surface verifies itself.** `make e2e CSB_E2E_BUILD=…` drives the whole
thing against a real engine in about two minutes and found three defects on its
first run — see M3.4. Two of them had been in the tree for weeks, one of them
was costing every promotion 57 seconds, and none of them was visible to a unit
test.

**The surface no longer promises anything it does not do.** Six verbs were
specified and unbuilt through phases 1 and 2 — `node logs`, `node shell`,
`fault ping-unavailable`, `repl watch`, `ha promote`, `ha failback` — and M3.3
built all six. **Thirty-six verbs across seven nouns**, and the helper that answered
`not_implemented` is gone because nothing calls it.

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
| M0.7 | Failback script reviewed by the technical team | **done 2026-09-03** — all four questions answered, and two of the answers contradicted the tracker: writes are not quiesced at all, and the return trip is conditional on the hardware being asymmetric rather than routine ([`requirements/01-failback-field-evidence.md`](requirements/01-failback-field-evidence.md) §9) |

## Phase 1 — the tool exists

G1, G2, G3, G4. A developer with Docker and a CUBRID build gets a two-node HA
cluster from one command, can break it in the three ways the CBRD-26983
verification needed, and can ask it what state it is in.

| # | Item | Acceptance |
|---|---|---|
| M1.1 | Command surface and machine-readable output (`design/01-cli.md`) | **done 2026-09-02** — every command has a `--json` form, the envelope is one type, and the exit codes are implemented and tested. The verbs behind the surface are M1.2–M1.7; until they land each exits 1 with a `not_implemented` note rather than pretending |
| M1.2 | Topology model — the `ha` preset, node count, per-node overrides (`design/02-topology.md`) | **done 2026-09-02** — presets `ha` and `single`, everything derived from the cluster name, parameters routed by file with `--set-hidden` as the escape hatch, and the describe artifact is the same value the tool builds from |
| M1.3 | Container backend — image, network, run-as-invoking-user, `NET_ADMIN`, `--init` | **done 2026-09-02** — a host-built `install.out` runs by path and is in no image; the base image is built once from a recipe tagged by its own hash. The engine's glibc floor is read from the ELF and checked against the image before anything starts |
| M1.4 | Assembly — config generation, the slave chain, start ordering (`design/03-assembly.md`) | **done 2026-09-02** — one command from an empty directory to `serving`, eight ordering traps encoded, zero manual interventions and no ordering knowledge required of a first-time user |
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
| M3.1 | **The surface `cubrid-testkit` provisions through** (was M2.4) | testkit calls it to set up and tear down without screen-scraping. **Slice 1 done 2026-09-03**: the CTP `ha_repl.conf` round trip, in and out ([`design/02-topology.md`](design/02-topology.md) §7). Four decisions taken with the maintainer rather than guessed — the transport is a docker-exec `Channel` and not ssh, csb keeps taking a host-built tree so `cubrid_download_url` is ignored and said so, CTP parameter keys map onto `--set` with validation kept, and the compatibility layer lives on this side as `describe --format ctp`. Verified against the real `CTP/conf/ha_repl.conf`. What is still open is the acceptance line itself: testkit has to make the first real call |
| M3.2 | **Replication canary** — `repl check`, a write that has to arrive | **done 2026-09-02** — measured on one cluster minutes apart: healthy, the row arrives in **0.63 s**; with the applier suspended the gauge reads `apply_lag=0` and `fail=0` while the row does not arrive in 15 s, exit 4. Every number says fine and nothing is moving. It is what the field actually trusts: they verify a rebuilt slave with `applyinfo -r … -a` and a `repl_test` table they create and insert into, rather than by reading a threshold off a gauge ([`requirements/01-failback-field-evidence.md`](requirements/01-failback-field-evidence.md) §4) |
| M3.3 | **The six verbs the surface named and had not built** | **done 2026-09-03** — measured on one cluster in a single unattended run. `ha promote` moved the role in **1.8 s** and reported that `cubrid heartbeat stop` had not returned yet, which is the phase-0 finding turned into a note: the roles are the evidence, not the exit status. `ha failback` returned service in **2.5 s** and verified it with a write that arrived on the rejoined node in **0.09 s** — roles alone say the group agrees, not that replication carries anything. `repl watch` under an apply stall recorded `copy 0→16, rose at +1.3s` while `apply` sat flat at 0, which is §3's lie with a timestamp on it. `node logs` names the process and finds the file; `fault ping-unavailable --mechanism binary` produced `rc=126` from a running node and `clear` put the binary back at its original mode |
| M3.4 | **The surface verifies itself against a real engine** (`e2e/`, `make e2e`) | **done 2026-09-03** — thirteen checks in one unattended run of about three minutes: create, both provenances, the canary, `repl diff`, a stalled stage seen through `repl watch`, both ping mechanisms, a clean stop and start, a dropped-packet partition that diverges the pair and the rebuild that closes it, promote, failback, the record's ordering and the exit codes. It asserts on the JSON envelope rather than on printed text, because the envelope is the contract and prose is free to change. Not part of `make check` — it needs Docker, an engine tree and several minutes — and not optional either |
| M3.5 | **The third split-brain flavour, measured** (`harness/calc-score-window.sh`) | **done 2026-09-03** — six runs, three per arm. The window is the interval: both nodes accepted writes for 11/12/12 s at `ha_calc_score_interval_in_msecs=15000` against 2/1/0 s at the default, and the roles settled at 13/13/13 s against 4/2/1. The reported both-ways sync did not happen — rows crossed one way only, the wrong way relative to the settled roles, and the standby is left permanently missing a row while `repl status`, `repl check` and `ha resync` all report a healthy cluster. `to-be-master` was not observed in any run and is recorded as unreproduced rather than dismissed ([`findings/active-active-window.md`](findings/active-active-window.md)) |
| M3.6 | **`repl diff`, and a resync decision that compares before it reassures** | **done 2026-09-03** — the divergence M3.5 measured was invisible to every verb this tool had. `repl diff` asks the two databases what they hold, taking its table list from the catalog rather than from the applier's error log, because a split brain fails nothing and that log is empty exactly when the divergence is largest. Verified on one cluster: healthy, `w master=1 standby=1 same`, exit 0; after a healed split brain, `w master=3 standby=2 DIFFERENT`, exit 1, while `repl status` read `apply_lag=0 fail=0` on both sides. `ha resync` used to answer `resume — fail_counter is 0` there; it now compares first and answers `slave`, naming the table |
| M3.7 | **`ha resync --path slave` rebuilds from a backup** | **done 2026-09-03** — the engine's own procedure, taken from `share/scripts/ha/ha_make_slavedb.sh` in the build in use, with the ssh and scp removed because both nodes' directories are on one host: online `backupdb -C` on the master, the old database removed, the backup and then `<db>_bkvinf` copied in that order, `cubrid restoreslave -s master -m <host>`, rejoin. `restoreslave` rather than `restoredb` is the piece that matters — it writes the replication bookkeeping a healed split brain leaves wrong. Three runs on diverged pairs: 19 s, 21 s, 23 s, each ending `registered_and_standby` with every table matching and a canary at 0.08–0.61 s against 25.97 s before. It compares again afterwards and fails `still_diverged` rather than reporting a repair it did not make |
| M3.9 | **`record export --format html`** — the first honest slice of the web front end | **done 2026-09-03** — the same document the JSON export writes, rendered as one self-contained page: both intervals per role change, the tool's events and the engine's harvested lines in one timeline with the actor named, the invalidity reasons at the top, and the `describe` as it stood when the record opened. No CDN, no font, no script — a run record has to open on a machine with no network, and the suite fails the page if it contains `http://`, `https://` or `<script`. It renders what `Build` returns rather than computing its own view, which is the rule a front end over this surface has to keep |
| M3.8 | **A node that will not rejoin, diagnosed and repaired** | **done 2026-09-03** — found by the e2e suite, and it turned out not to be the rebuild's doing at all. A node whose heartbeat was stopped mid-stream (which is what `ha promote` does) comes back with `db_ha_apply_info` recording a position its replication log does not reach; the applier asks for that page, calls it corrupted, and exits, while `cubrid heartbeat start` reports "HA processes start: fail" without naming a process. `node start` reads the applier's own log, recopies the master's active log, starts the node again — **and then checks**. When the recopy is not enough, which is the case for a node demoted by a role change, it exits 1 naming the remedy instead of exiting 0 on a node that never came back. **That remedy is a rebuild, and it is the field's answer, not ours**: the rejoin path in their tracker is `ha_make_slavedb.sh`. `ha resync <node> --path slave` now accepts a node in any state but master, because the one most needing a rebuild is the one with no role at all |

**Two defects M3.3 found in already-built code.** `partition --mechanism drop` and
`ping-unavailable --mechanism icmp` are both packet-level, and **iptables was not
in the base image** — the drop mechanism had never run since it was written.
`record show` read the file in append order while `record export` sorted, so the
two views of one run disagreed about when things happened; the sort moved into
`record.Read`, where no caller can forget it.

**Three defects on its first run, and one of them had been costing 57 seconds.**

- **`ha_ping_hosts` was never written.** `--ping-mode icmp` is the default, was
  recorded in `describe`, and never reached `cubrid_ha.conf` —
  [`design/02-topology.md`](design/02-topology.md) has said "set by default"
  since it was written. What it cost is in the engine's own words: a node left
  alone in the group logged `[Failback] [Cancelled] No hosts are registered in
  ha_ping_hosts … making it impossible to determine` on a loop while it sat in
  `to_be_active`. With the parameter written, the same `ha promote` subtest went
  from **59.1 s to 2.3 s**. The host is the docker network's gateway, which is
  the one address that survives a cut between the two nodes.
- **An unknown noun or verb produced no envelope at all** — it printed to stderr
  and exited 2, so a consumer had to parse stderr to tell a typo from a
  precondition. Every early failure now answers in the envelope
  ([`design/01-cli.md`](design/01-cli.md) §6).
- `record show` read the file in append order while `record export` sorted it,
  so two views of one run disagreed about when things happened.

**And it is the standing check on the withdrawn one.** `down` then `up` is a
subtest now, asserting the original master comes back without a forced
promotion. The `to_be_active` stall was never explained
([`design/03-assembly.md`](design/03-assembly.md) §3); an unexplained
observation deserves a check that runs every time, not a paragraph.

**Why M3.1 moved.** Its acceptance is a sentence about a consumer, and the
consumer is not ready to make the call yet. `cubrid-testkit` now has a Go module,
a `cmd/testkit`, and an `internal/` with a `topology` package in it — it is no
longer docs and an empty directory — but its own README still says *design, not
code*: phases 0 to 2 are done and phase 3, the `shell` task in Go, has not
landed. Nothing here is blocked; the surface it would call is built and
machine-readable. What is missing is a first real call from the other side.

**Its ADR-014 has already decided the half that is theirs**, and it decides it in
our favour: *"The system under test has a topology; the runner does not have a
fleet."* `ha_repl` needs a master and a slave and that looks like a fleet — it is
not, and the runner will not manage one. Whatever stands that pair up is
therefore somebody else, which is the opening this milestone is named after. The
same ADR notes that *"a runner scoped to one machine is a runner you can give a
container to"*, which bears directly on the first question below.

Two things still have to be decided *with* testkit rather than guessed at ahead
of it:

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

### Also phase 3 — skeletal

**A web front end over the same surface**, and the reason to expect it is that
the two artifacts this tool produces are already documents somebody reads: a
`describe` small enough to paste into an issue, and a run record with a timeline
and two intervals per role change. Rendering those is most of a UI, and it is a
client of the same JSON the CLI emits rather than a parallel path (§9 OQ6). What
it must not become is a second surface with its own way of asking. A wider topology catalogue: replica nodes,
broker/CAS tiers, shard configurations, CDC consumers — each brings its own
configuration surface and its own fault verbs, and the migration from presets to
a declarative document is triggered here. **A tailnet so a topology can span machines** ([`DESIGN.md`](DESIGN.md) §9 OQ11).
`ha_node_list` works today because both containers sit on one docker network, and
that is the single structural limit here — every other skeletal item runs into it
first. Tailscale would give each node a stable address without publishing a port,
which keeps §6's refusal of port bookkeeping rather than reversing it. What it
costs is that the fault verbs are defined against the docker network's cut and
would have to be re-measured rather than ported, and that a control plane lands
in the path of `cluster create` for a tool built to run offline. Provisionally a
backend option, not a default, decided together with the fork below.

**A Kubernetes backend behind the same topology model — and it is `cubrid-operator`
that is on the other side of it.** The operator already has a `CubridDB` CRD that
deploys, configures HA, schedules backups, scales and reports status, which is
the same topology this project models against a different backend. Two ways they
could meet, and the backend contract now says which: **a component under test,
not a second backend.** Three of its eleven operations collide with Kubernetes —
the host-side file access that seeding and the slave rebuild use does not exist in
a pod, "there is never an engine image" collides with how a pod gets its engine,
and `NetworkPolicy` cannot express "keep the route, drop the packets", which is
the difference between two engine code paths that the split-brain finding rests
on. The deepest objection is not in the contract at all: **an operator's job is to
repair what this tool deliberately breaks**, so `node kill` is a scenario here and
a fault to be corrected there. That is the operator behaving correctly, and it is
what makes it a subject worth measuring rather than a substrate to build on —
how fast it notices, what it does with a split brain, and whether its `CubridDB`
status reports a divergence that `repl diff` can see and its gauges cannot. The tier-3 monitoring seam, once the engine has a
machine-readable metrics contract.

## Open

**What does the technical team require of failback? — answered 2026-09-03**
([`DESIGN.md`](DESIGN.md) §9 OQ8, closed;
[`requirements/01-failback-field-evidence.md`](requirements/01-failback-field-evidence.md) §9).
Writes are **not** quiesced — the exposure is accepted, so the tool names it
instead of implying a barrier. The evidence is the **canary**, not a threshold,
which is also the only instrument that works across a role change. A **DBA or
team lead** confirms first, which makes `--dry-run` the document that goes to
them. And the return trip is done **when the nodes are not equivalent**, not as a
matter of course — so `ha failback` is an exception path and a topology has to be
able to say that its nodes are not interchangeable.

What the record said before it was asked:
[`requirements/01-failback-field-evidence.md`](requirements/01-failback-field-evidence.md)
collected what the internal tracker already says, and it answers the mechanical
half: the rejoin path is `ha_make_slavedb.sh`, the alarm is `fail_counter`, and
the failback that actually hurts is the one that should never have started
(four sites, ten or more failover/split-brain/failback cycles a day
under load). What no ticket answers is what an operator *decides*: the threshold
for "caught up enough", whether write traffic is quiesced, who authorises it,
and whether the original master is preferred at all.
[`harness/failback.sh`](../harness/failback.sh) is runnable and in the
repository. **The marks it comes back with are the requirement set**, and they
shape the verb vocabulary — which is why M0.7 sits in phase 0 and not later.

**It is in the repository, which is the channel.** The script was written to be
sent somewhere and marked up; three of its four questions were then answered by
reading the tracker, so what is left is a runnable operator script with one open
decision in it. It does not need dispatching — it needs someone to run it and
disagree.

**The word, and why the script names the operation instead.** A second pass over
the tracker
([`requirements/02-ha-role-transition-field-evidence.md`](requirements/02-ha-role-transition-field-evidence.md))
found that **"failback" means demotion** to the engine and to the team's own study
notes — a master stepping down. Operations use **`failback 작업`** for the return
trip, in ninety-five tickets, so the term does exist; the two readings simply sit
side by side in the same tracker. A script that asks what the team requires "of
failback" can therefore be answered about either one, which is why it names the
operation it means rather than the word.

**Implementation language — decided 2026-09-02.**
[`design/ADR-001`](design/ADR-001-implementation-language.md) accepts **Go** for
the provisioner and shell for the operator-facing scripts. The earlier draft
proposed Python and rejected Go on the argument that nobody in this project's
ecosystem writes it; `cubrid-testkit` accepting Go under the same maintainer
inverted that argument rather than weakening it. M1.1 doubles as the validation
slice: if the schema types, the subprocess orchestration and the build-and-ship
story go badly there, the ADR is amended rather than defended.
