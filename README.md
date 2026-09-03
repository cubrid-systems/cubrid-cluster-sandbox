![cluster-sandbox — multi-node CUBRID topologies for development: one command, your own build, and the states you need one for as verbs: lag, split brain, the trip back](docs/assets/banner.svg)

**cluster-sandbox** stands a multi-node CUBRID topology up in containers, from
one command and your own build. It also reproduces the states you actually need
a cluster for: a slave that has fallen behind, two nodes that both believe they
are master, and the return trip after a failover.

For engine developers, QA, and external contributors. Part of
[CUBRID Systems Research](https://github.com/cubrid-systems).

> **Where it is:** phase 1 has started. The command surface, its JSON envelope
> and its exit codes are built and tested; the verbs behind them are not. See
> [Status](#status).

## Prerequisites

Three things, and the third is the one people forget:

| | Why |
|---|---|
| **Docker** | a node is a container, and a partition is a route operation inside one. Tested against 29.x |
| **Go** | to build `csb`. There are no binary releases yet |
| **A CUBRID install tree** | the engine under test. Your own build (`install.out`), or an unpacked release — it is bind-mounted read-only and never goes into an image |

Linux x86-64 for now. The engine tree has to be loadable by the container's
libc: `csb` reads the highest `GLIBC_` symbol your build requires straight out of
the ELF and refuses with that sentence rather than failing later as a linker
error.

## Install

```bash
git clone https://github.com/cubrid-systems/cubrid-cluster-sandbox
cd cubrid-cluster-sandbox
make dist                      # a static binary at bin/csb
sudo install bin/csb /usr/local/bin/     # optional
```

`make check` runs gofmt, `go vet` and the unit tests — no Docker, no engine.
`make e2e CSB_E2E_BUILD=~/cubrid/install.out` runs the whole surface against a
real build: it creates a cluster, breaks it every way the tool knows, returns
service to the original master and destroys it, asserting on the JSON envelopes
rather than on printed text. Run it against an engine build before trusting the
tool with one. State lives under `$CSB_HOME`
(default `~/.local/share/csb`), one directory per cluster holding its `describe`
artifact and its run record.

## Getting started

Stand up a two-node HA pair against a build you made, and write to it.

```bash
$ csb cluster create --name hadb --build ~/cubrid/install.out
   createdb hadb on hadb-n1
   seeded hadb-n2 with 7 files from hadb-n1
   heartbeat start on 2 node(s), concurrently
   waiting for hadb-n1 to reach registered_and_active
cluster hadb: 2 node(s) on hadb-net, state serving
  hadb-n1          master     registered_and_active
  hadb-n2          slave      registered_and_standby
```

About 50 seconds from nothing, most of it the engine. The first run also builds
the base image, once.

That is the whole assembly — two configuration files per node, the four-step
slave chain, the start ordering, and the traps that go with them — and you did
not have to know any of it. That is the point of the tool; the traps are listed
in [`docs/design/03-assembly.md`](docs/design/03-assembly.md) if you want to see
what it did on your behalf.

![The assembly, animated: absent becomes defined when the configuration is written, seeded once the slave has a copy of the master's volumes, forming when the heartbeat starts on both nodes at once, and serving only when the master reaches registered_and_active. Along the way the tool waits for an explicit completion signal rather than the databases.txt entry, copies hadb* while excluding the lock file, and refuses to call the cluster ready while a write would still be rejected](docs/assets/anim-create.svg)

Every transition is bounded, and every one decides on **observed state** rather
than on the exit code of the command that was supposed to cause it. That rule is
not defensive programming: `databases.txt` gains its entry *before* `createdb`
finishes, and seeding on that signal copies a database with a live transaction in
it — which is the one trap of the seven that leaves a corrupt slave instead of a
failed start.

Then use it. `node exec` runs a command on a node with the engine's environment
already set:

```bash
$ csb node exec master -- "csql -u dba -c 'CREATE TABLE t(i INT PRIMARY KEY);' hadb"
$ csb cluster describe --json | jq .data.engine
{ "kind": "build", "version": "11.5.0", "commit": "dd15f7f", "min_glibc": "2.34" }
$ csb cluster destroy --cluster hadb        # keeps the run record; --purge drops it
```

**What works today, honestly.** All of it. The surface names 36 verbs across
seven nouns and every one is built and has been run against a real cluster.
`cluster up` after a `down` brings the group back — an earlier version of this
paragraph said it did not, on a stall that turned out to be this tool's own bug
and not the engine's ([`docs/design/03-assembly.md`](docs/design/03-assembly.md)
§3).

What is **not** here is a policy for the return trip. `ha failback` performs it
and stops at the decision nobody has written down — who authorises it, and on
what evidence — because a tool that picked that would be inventing a requirement
([`harness/failback.sh`](harness/failback.sh)).

## The surface being built

```
csb cluster create --preset ha --nodes 2 --build ~/cubrid/install.out
csb cluster status --json

csb fault partition master --keep ping-host    # split brain, on request
csb fault lag slave --stage apply              # one stage, not "slow"
csb load start --profile insert --rate 2000/s  # a rate it has to hold, and report
csb repl watch --interval 0.5s --out lag.tsv

csb ha failback --to hadb-n1                   # interactive; the policy is open
csb cluster describe --out cluster.yaml        # stands the same cluster up elsewhere
```

Every command has a `--json` form and a documented exit code, because
`cubrid-testkit` provisions through this surface rather than screen-scraping it
([`docs/design/01-cli.md`](docs/design/01-cli.md)).

A verb that is specified and not yet implemented exits 1 with a
`not_implemented` note rather than pretending — never exit 2, because a consumer
has to tell a gap from a typo. No verb uses that answer any more. The shell
scripts in [`harness/`](harness/) are no longer how a cluster gets provisioned;
they are the measurements and the operator script that the tool was built out
of.

## Why it exists

Answering a single HA question about a serial-cache change took a two-node
cluster built by hand. Every step of it was mechanical: two configuration files
per node, a four-step slave chain, a start ordering that fails quietly if you get
it wrong, and two traps that each cost an afternoon. None of it was written down
anywhere a newcomer would find.

The neighbouring tools each solve an adjacent problem:

| Tool | What it is | Why it is not this |
|---|---|---|
| `cubrid-contrib/sandbox` | a single-container build shell | one node, and it builds the engine rather than running a topology |
| `cubrid-contrib/docker_for_ctp` | a two-container rig that drives CTP | a test rig against a released tarball |
| `cubrid-testkit` | the test harness succeeding CTP | treats HA as a workload it runs, not one it provisions |
| `cubrid-operator` | production Kubernetes deployment | operational lifecycle, not local iteration |

Nobody provisions a development topology. That is the gap.

## Architecture

Six components and two artifacts. [`docs/DESIGN.md`](docs/DESIGN.md) §4 fixes the
boundaries between them, [`docs/design/`](docs/design/) specifies what crosses
each one, and the diagram says which document that is.

![Architecture: consumers over one command surface, six components — topology model, provisioner core, container backend, fault injection, inspector, load driver — acting on a two-node cluster in one network, and emitting two artifacts: describe and record](docs/assets/architecture.svg)

The sixth component is the late one. Phase 0 assumed a scenario brings its own
traffic; the field's own switchover measurement sat unusable for four years
because of exactly that assumption, so the load driver is a component with a rate
contract rather than a loop in each scenario's shell script
([`docs/design/06-load.md`](docs/design/06-load.md)).

What those components actually stand up is one user-defined network, one
container per node, and an engine tree that crosses the host boundary as a
read-only bind mount:

![What one command stands up: a docker network holding two node containers, each running cub_master, cub_server, copylogdb and applylogdb, with the host's build tree mounted read-only at /opt/cubrid-ro, the database directory at /db on both, a ping host on the same network, and the describe artifact and run record kept on the host outside the containers](docs/assets/topology.svg)

**There is a base image, and there is never an engine image.** The base image is
`ubuntu:24.04` plus five packages, built once from a recipe the tool carries. The
engine is bind-mounted from your tree: rebuilding it rebuilds nothing here,
because the container is looking at the same files. That distinction is what
makes the tool usable *while* you are changing the engine, and it is the opposite
of what a production deployment should do
([ADR-002](docs/design/ADR-002-backend-contract.md)).

## Status

**Phase 0 complete.** The manual assembly is written down and runs
(`harness/lib.sh`), and the three questions the design was unsure about were
answered by measurement rather than by reading:

| Question | Answer | Where |
|---|---|---|
| Does split brain need a broken configuration? | **No** — a correctly configured cluster reaches two masters in 9 s when the ping host survives the partition | [`docs/findings/split-brain.md`](docs/findings/split-brain.md) |
| How is replication lag injected, and does the heartbeat allow it? | Suspend a replication stage; the heartbeat watches process *existence*, not progress, and does not interfere | [`docs/findings/replication-lag.md`](docs/findings/replication-lag.md) |
| Is the return to the original master mechanically possible? | Yes — restored in 2 s with no row loss. What is *not* settled is the policy around it | [`docs/findings/failback.md`](docs/findings/failback.md) |

The first two are worth watching rather than reading. Both animations run on
measured values; neither rounds a figure to make a point.

![The same route-level cut, twice, animated. On the left ha_ping_hosts is set and the ping host survives: the master pings successfully, concludes it is not partitioned and stays master, while the slave pings successfully, finds nothing to cancel its failover and promotes — two masters in 9 s, from a correct configuration. On the right the ping host is cut from the master too: it demotes itself, the failover is clean, and forty-five seconds after the heal the roles are still swapped because only one master exists and nothing triggers](docs/assets/anim-splitbrain.svg)

A single ping host is a quorum of one, and it votes for whoever asks it — the
master cancels its failback when the ping *succeeds*, the slave cancels its
failover only when the ping *fails*, so one surviving host satisfies both
cancel-nots at once. That is why `partition` cuts routes rather than interfaces:
an interface-level cut cannot express `--keep`.

![The replication pipeline and its gauge, animated. Suspending the apply stage freezes every column of db_ha_apply_info for thirty seconds, eof_lsa included, because applylogdb is what writes the row — the reported lag holds at 27,786 and the truth, 54,855, arrives in a single sample on release. Suspending the copy stage instead freezes eof_lsa while the applier keeps draining, so the reported lag falls from 49,544 to 38,576 while replication is entirely stopped. Only applyinfo -r, read against the master, sees either](docs/assets/anim-lag.svg)

So there is no field called `delay`. `repl status` reports the copy stage and the
apply stage separately, always against a master-side reference, and reports
`null` with a reason rather than a number its source cannot support
([`docs/design/05-inspect.md`](docs/design/05-inspect.md) §3).

**Decided since:** the provisioner is written in **Go**, and anything an operator
reads, edits, or runs on a real host stays shell
([ADR-001](docs/design/ADR-001-implementation-language.md)).

**Runnable, and one decision short.**
[`harness/failback.sh`](harness/failback.sh) returns a cluster to its original
master and stops where a person has to choose. Most of what it once guessed at
came out of the field's own records — "caught up" is a canary rather than a
number, writes are held off with the broker's `ACCESS_MODE`, and the return trip
is routine and sometimes needs no rebuild. What nobody has written down is **who
authorises it and on what evidence**, so that step asks and does not assume. A
site that does it differently disagreeing with this script is the most useful
thing that could happen to it.

## Layout

```
docs/
  DESIGN.md          the design document — problem, goals, architecture, decisions
  ROADMAP.md         phases, milestones, and where the project actually is
  design/            01 command surface · 02 topology · 03 assembly · 04 faults
                     05 inspection · 06 load · 07 the run record · ADR-001 language
  requirements/      what the field asks for, from CUBRID's internal tracker
  survey/            PostgreSQL, MySQL, MongoDB, TiDB, and the CUBRID gap analysis
  findings/          what running it showed — including where it contradicted the design
  assets/            banner · architecture · topology, and three animated figures:
                     assembly, split brain, replication lag
harness/
  Dockerfile · entrypoint.sh · lib.sh  the Phase-0 spike: one node, and the
                                       four-step assembly that makes two of them
  oq9-splitbrain.sh · oq7-lag.sh       the experiments
  failback.sh · failback-demo.sh       the failback artifact, and a rig that proves it runs
  results/                             captured console output and samples
```

## Running the harness

Needs Docker and a built CUBRID install tree (`ENGINE=<path to install.out>`;
the default points at a local build). Each script builds its own network and
containers and removes them on exit.

```bash
cd harness
bash oq9-splitbrain.sh A|B|C     # ~4 min per arm
bash oq7-lag.sh                  # ~7 min
bash failback-demo.sh            # ~5 min — sets up the failed-over state, then runs failback.sh
```

Nodes run with `--cap-add=NET_ADMIN` and `--init`, both for reasons the harness
README records: the fault mechanisms are route and qdisc operations, and without
a reaping PID 1 `cubrid heartbeat stop` never returns.

## Relationships

- **`cubrid-testkit`** consumes this tool: it owns suites, dispatch, and
  reporting; this project owns the environment they run on.
- **`cubrid-operator`** is not a dependency. It serves production Kubernetes;
  this serves a developer setting up an environment. The connection is later and
  narrower — operational testing.
- **CUBRID Ops** owns engine-internal metrics. This project leaves a documented
  seam and does not build a second collector.

## Licence

Apache License 2.0 — see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).

The slave rebuild follows the steps and ordering of CUBRID's own
`share/scripts/ha/ha_make_slavedb.sh`. No code from it is copied; the sequence is
reused and attributed where it is used.
