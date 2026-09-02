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

`make check` runs gofmt, `go vet` and the tests. State lives under `$CSB_HOME`
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

Then use it. `node exec` runs a command on a node with the engine's environment
already set:

```bash
$ csb node exec master -- "csql -u dba -c 'CREATE TABLE t(i INT PRIMARY KEY);' hadb"
$ csb cluster describe --json | jq .data.engine
{ "kind": "build", "version": "11.5.0", "commit": "dd15f7f", "min_glibc": "2.34" }
$ csb cluster destroy --cluster hadb        # keeps the run record; --purge drops it
```

**What works today, honestly.** `cluster create`, `describe`, `ls`, `destroy`,
`down`, and `record show`/`export`. `cluster up` after a `down` currently stops
with a diagnosis rather than a cluster: a gracefully stopped HA group does not
return to service on its own, and completing the promotion by hand is a forced
transition whose safety `csb` will not assert on your behalf — see
[`docs/design/03-assembly.md`](docs/design/03-assembly.md) §3. Everything under
`fault`, `repl`, `ha` and `load` is specified and not built; each exits 1 with a
`not_implemented` note rather than pretending.

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

**The surface is built; most of the verbs behind it are not.** A verb that is
specified and not yet implemented exits 1 with a `not_implemented` note rather
than pretending — never exit 2, because a consumer has to tell a gap from a typo.
What provisions a cluster today is the shell harness in [`harness/`](harness/).

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

## Status

**Phase 0 complete.** The manual assembly is written down and runs
(`harness/lib.sh`), and the three questions the design was unsure about were
answered by measurement rather than by reading:

| Question | Answer | Where |
|---|---|---|
| Does split brain need a broken configuration? | **No** — a correctly configured cluster reaches two masters in 9 s when the ping host survives the partition | [`docs/findings/split-brain.md`](docs/findings/split-brain.md) |
| How is replication lag injected, and does the heartbeat allow it? | Suspend a replication stage; the heartbeat watches process *existence*, not progress, and does not interfere | [`docs/findings/replication-lag.md`](docs/findings/replication-lag.md) |
| Is the return to the original master mechanically possible? | Yes — restored in 2 s with no row loss. What is *not* settled is the policy around it | [`docs/findings/failback.md`](docs/findings/failback.md) |

**Decided since:** the provisioner is written in **Go**, and anything an operator
reads, edits, or runs on a real host stays shell
([ADR-001](docs/design/ADR-001-implementation-language.md)).

**Open, and not this project's to close:** what the technical team requires of the
return trip to the original master. [`harness/failback.sh`](harness/failback.sh)
encodes this project's guess at that sequence, with a decision point at every step
where the guess is a guess. It goes to them to be marked up, and the marks are the
requirement set.

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
  assets/            the banner and the architecture diagram
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
