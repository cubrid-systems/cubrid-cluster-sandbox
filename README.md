![cluster-sandbox — multi-node CUBRID topologies for development: one command, your own build, and the states you need one for as verbs: lag, split brain, the trip back](docs/assets/banner.svg)

**cluster-sandbox** stands a multi-node CUBRID topology up in containers, from
one command and your own build. It reproduces the states you actually need a
cluster for — a slave that has fallen behind, two nodes that both believe they
are master, the return trip after a failover — as verbs addressed by role. And
it runs a written-down sequence of those verbs against a build, so *does my
change still behave* is a file somebody else can run rather than knowledge
somebody already has.

For engine developers, QA, and external contributors. Part of
[CUBRID Systems Research](https://github.com/cubrid-systems).

> **Where it is:** phases 0, 1 and 2 are complete. **Thirty-seven verbs across
> eight nouns**, every one built and driven against a real engine by `make e2e`.
> Phase 3 is in progress. What is *not* settled is a policy for the return trip —
> see [Status](#status).

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
`make e2e CSB_E2E_BUILD=~/cubrid/install.out` drives the whole surface against a
real build in about two minutes: it creates a cluster, breaks it every way the
tool knows, returns service to the original master and destroys it, asserting on
the JSON envelopes rather than on printed text. Run it against an engine build
before trusting the tool with one — its first run found three defects, two of
them weeks old and one costing every promotion 57 seconds, none visible to a unit
test.

State lives under `$CSB_HOME` (default `~/.local/share/csb`), one directory per
cluster holding its `describe` artifact and its run record.

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
the base image, once. This is what you now have:

![What one command stands up: a docker network holding two node containers, each running cub_master, cub_server, copylogdb and applylogdb, with the host's build tree mounted read-only at /opt/cubrid-ro, the database directory at /db on both, a ping host on the same network, and the describe artifact and run record kept on the host outside the containers](docs/assets/topology.svg)

Then use it. `node exec` runs a command on a node with the engine's environment
already set:

```bash
$ csb node exec master -- "csql -u dba -c 'CREATE TABLE t(i INT PRIMARY KEY);' hadb"
$ csb cluster describe --json | jq .data.engine
{ "kind": "build", "version": "11.5.0", "commit": "dd15f7f", "min_glibc": "2.34" }
$ csb cluster destroy --cluster hadb        # keeps the run record; --purge drops it
```

`master` and `slave` are queries, not labels. After a failover `master` names the
other machine, so a script written before the failover runs unchanged after it.

### What that command did on your behalf

Two configuration files per node, the four-step slave chain, a start ordering
that fails quietly if you get it wrong, and the traps that go with them:

![The assembly, animated: absent becomes defined when the configuration is written, seeded once the slave has a copy of the master's volumes, forming when the heartbeat starts on both nodes at once, and serving only when the master reaches registered_and_active. Along the way the tool waits for an explicit completion signal rather than the databases.txt entry, copies hadb* while excluding the lock file, and refuses to call the cluster ready while a write would still be rejected](docs/assets/anim-create.svg)

Every transition is bounded, and every one decides on **observed state** rather
than on the exit code of the command that was supposed to cause it. That rule is
not defensive programming: `databases.txt` gains its entry *before* `createdb`
finishes, and seeding on that signal copies a database with a live transaction in
it. Of the seven traps this layer owns, five produce a **failed start** — loud,
and an hour to find. That one produces a **corrupt slave**, which is quiet, and
is exactly the class of thing a provisioner exists to own
([`docs/design/03-assembly.md`](docs/design/03-assembly.md) §2).

## Break it on purpose

The failure states are verbs. They divide into **events**, which happen and are
over, and **conditions**, which are entered, held, and cleared — and a condition
that outlives its scenario silently poisons the next one, so every condition
records how to reverse itself and `csb fault ls` shows what is in force.

```bash
csb fault partition master --keep ping-host   # split brain, on request
csb fault lag slave --stage apply             # one stage, not "slow"
csb fault failcount slave --rows 200          # move fail_counter deliberately
csb fault clear --all
```

![The same route-level cut, twice, animated. On the left ha_ping_hosts is set and the ping host survives: the master pings successfully, concludes it is not partitioned and stays master, while the slave pings successfully, finds nothing to cancel its failover and promotes — two masters in 9 s, from a correct configuration. On the right the ping host is cut from the master too: it demotes itself, the failover is clean, and forty-five seconds after the heal the roles are still swapped because only one master exists and nothing triggers](docs/assets/anim-splitbrain.svg)

Split brain needs no misconfiguration. A single ping host is a quorum of one and
it votes for whoever asks it — the master cancels its failback when the ping
*succeeds*, the slave cancels its failover only when the ping *fails*, so one
surviving host satisfies both cancel-nots at once. That is why `partition` cuts
routes rather than interfaces: an interface-level cut cannot express `--keep`
([`docs/design/04-faults.md`](docs/design/04-faults.md) §3, §5).

`fault failcount` is the one verb `clear` cannot reverse, because its damage is
data. It exits 3 there and points at `ha resync`, which performs the engine's own
`ha_make_slavedb.sh` rebuild and reports which of the three repair paths it chose
and why.

## Watch what it did

```bash
csb repl status --json                     # both stages, against the master
csb repl check --wait 30s                  # a write that has to arrive
csb repl watch --interval 0.5s --out lag.tsv
csb repl diff --table t                    # what the two databases actually hold
```

![The replication pipeline and its gauge, animated. Suspending the apply stage freezes every column of db_ha_apply_info for thirty seconds, eof_lsa included, because applylogdb is what writes the row — the reported lag holds at 27,786 and the truth, 54,855, arrives in a single sample on release. Suspending the copy stage instead freezes eof_lsa while the applier keeps draining, so the reported lag falls from 49,544 to 38,576 while replication is entirely stopped. Only applyinfo -r, read against the master, sees either](docs/assets/anim-lag.svg)

So there is no field called `delay`. `repl status` reports the copy stage and the
apply stage separately, always against a master-side reference, and reports
`null` with a reason rather than a number its source cannot support. A canary
proves the path is open; it cannot prove the two databases are the same, which is
why `repl diff` compares the catalog and why `ha resync` runs that comparison
*before* it says nothing is wrong
([`docs/design/05-inspect.md`](docs/design/05-inspect.md) §3–4a).

## Write it down as a scenario

A scenario is a sequence of this tool's own verbs and the state they are expected
to reach — a file, run against a build:

```bash
csb scenario run scenarios/ha-split-brain.json --build ~/cubrid/install.out
```

```json
{
  "name": "a partition makes two masters, and healing it makes one again",
  "cluster": { "preset": "ha" },
  "steps": [
    { "note": "the pair is serving", "await": { "masters": 1, "standbys": 1 }, "within": "60s" },
    { "note": "a write arrives", "run": ["repl", "check"] },
    { "note": "cut the peer, keeping the witness",
      "run": ["fault", "partition", "slave", "--mechanism", "drop"],
      "await": { "masters": 2 }, "within": "120s" },
    { "note": "heal it", "run": ["fault", "clear"],
      "await": { "masters": 1, "standbys": 1 }, "within": "180s" }
  ]
}
```

**A step is an argv this tool already accepts**, dispatched through the same path
with the same envelope and the same exit code — so a scenario cannot ask for
anything the command line cannot, and most judgements need no new vocabulary:
`repl check` already exits 4 when the row does not arrive, `repl diff` exits 1
when the sides differ. **The build is not in the file**: a scenario is a
statement about behaviour and the engine under test is the variable, so the same
file runs against the build you just made and the one you are comparing it with.

`matrix` and `repeats` turn one scenario into many runs, because half of what
people write is a sweep rather than a reproduction — vary one thing, hold the
rest, repeat, read a table. `measure` names what to collect from a closed list,
every entry a field this tool already emits, so a table cannot report something
nobody can go and look at
([`docs/design/01-cli.md`](docs/design/01-cli.md)).

## Load, and why it is a component

```bash
csb load start --profile insert --rate 2000/s --batch 200 --require-rate
csb load status --json
```

Phase 0 assumed a scenario brings its own traffic. The field's own switchover
measurement sat unusable for four years because of exactly that assumption, so
load is a component with a **rate contract**: it targets a rate, holds it, and
reports whether it held it. `--require-rate` turns a miss into exit 1, because a
driver that silently falls behind makes every figure measured beside it a figure
about the driver.

Two kinds, and conflating them is an error: a **db** load saturates the master's
transaction path and produces replication backlog; a **host** load saturates the
node's CPU and makes heartbeat responses miss their window. A compile with no
database traffic at all can trigger a failover.

It also reports **p50/p90/p99 per statement**. Measured during a failover at 40
statements/s: p50 15.7 ms, p99 4220 ms. Four seconds at the tail while the median
barely moves — which is what a client actually experiences when a role changes,
and is invisible in every other figure this tool reports. Percentiles are absent
below twenty samples, because a percentile from three measurements is not a
percentile.

`--clients N` puts the driver on a node of its own: part of the cluster — same
network, same labels, destroyed with it — and deliberately not part of the HA
group, so it never appears in `ha_node_list`. The driver used to run inside the
master, competing with the engine for the CPU quota given to the engine; that was
a compromise, not a design. `--tools DIR` mounts a host directory read-only at
`/tools` on those nodes, the same way `--build` takes an engine tree where it
already is ([`docs/design/06-load.md`](docs/design/06-load.md)).

## The surface

Eight nouns, one per thing a user holds in their head, and thirty-seven verbs:

| Noun | Verbs |
|---|---|
| `cluster` | `create` `up` `down` `destroy` `status` `describe` `quiesce` `resume` `ls` |
| `node` | `start` `stop` `kill` `status` `logs` `shell` `exec` |
| `fault` | `partition` `lag` `splitbrain` `failcount` `ping-unavailable` `clear` `ls` |
| `repl` | `status` `check` `watch` `diff` |
| `ha` | `status` `promote` `failback` `resync` |
| `load` | `start` `stop` `status` |
| `scenario` | `run` |
| `record` | `show` `export` |

Every command takes `--json` and has a documented exit code, because
`cubrid-testkit` provisions through this surface rather than screen-scraping it.
Human output is for humans and may change; `--json` is the contract. Three rules
in it come from measurement rather than taste: `data` never carries a derived
figure whose source cannot support it — the field is `null` and `notes` says why,
never zero; `notes` is machine-readable, a list of `{code, severity, message}`;
and timestamps are the **sample's**, not the report's
([`docs/design/01-cli.md`](docs/design/01-cli.md) §4).

Nothing answers `not_implemented` any more. The shell scripts in
[`harness/`](harness/) are no longer how a cluster gets provisioned — they are
the measurements the tool was built out of, and `scenarios/` is where their
successors live.

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

**There is a base image, and there is never an engine image.** The base image is
`ubuntu:24.04` plus five packages, built once from a recipe the tool carries and
tagged with that recipe's hash. The engine is bind-mounted from your tree:
rebuilding it rebuilds nothing here, because the container is looking at the same
files. That is a rule for a tool whose user is *changing* the engine, and the
opposite of what a production deployment should do — `cubrid-operator` ships
images because that is right for its audience.

[ADR-002](docs/design/ADR-002-backend-contract.md) fixes the eleven operations a
backend has to provide, derived from what the docker one already does rather than
invented for backends that do not exist. Evaluated against it, a tailnet changes
four of the eleven and leaves the fault verbs intact — so `--network tailnet` is a
*network* for the docker backend rather than a second backend, and a topology can
span machines without the tool growing one.

## Status

**Phases 0, 1 and 2 are complete**, 0 on 2026-08-27 and 1 and 2 on 2026-09-02.
Phase 3 is in progress. What the apparatus was *for* is the last column here —
questions the field asked and could not measure:

| Question | Answer | Where |
|---|---|---|
| Does split brain need a broken configuration? | **No** — a correctly configured cluster reaches two masters in 9 s when the ping host survives the partition | [`split-brain.md`](docs/findings/split-brain.md) |
| How is replication lag injected, and does the heartbeat allow it? | Suspend a stage; the heartbeat watches process *existence*, not progress, and does not interfere | [`replication-lag.md`](docs/findings/replication-lag.md) |
| Is the return to the original master mechanically possible? | Yes — restored in 2 s with no row loss. The policy around it is not settled | [`failback.md`](docs/findings/failback.md) |
| What actually decides when a cluster switches over? | Not the documented arithmetic. Nineteen runs: raising either heartbeat parameter fourfold leaves the measurement inside its own baseline band; `ha_calc_score_interval_in_msecs` moves it, by about 2× on means | [`switchover-threshold.md`](docs/findings/switchover-threshold.md) |
| Does a healed partition really run Active-Active, syncing both ways? | The window is real and is as long as `ha_calc_score_interval_in_msecs` — ~12 s at 15000 against ~1 s at the default. "Both ways" is not what happens: rows cross in one direction and the divergence that is left is permanent, and every gauge calls it healthy | [`active-active-window.md`](docs/findings/active-active-window.md) |

That fourth row is a 2021 request the field said developers could not carry out,
and a test that tried has been open since 2022 on three candidate explanations of
its own result. The sweep discriminates between them: two parameters move nothing
and the third, travelling the same path and written to the same file by the same
command, moves the measurement — so the path works and the first two are inert on
it.

**One decision short.** `ha failback` performs the return trip and stops where a
person has to choose. Most of what it once guessed at came out of the field's own
records — "caught up" is a canary rather than a number, writes are in fact not
quiesced at all, and the return trip is routine and sometimes needs no rebuild.
What nobody has written down is **who authorises it and on what evidence**, so
that step asks and does not assume. A site that does it differently disagreeing
with [`harness/failback.sh`](harness/failback.sh) is the most useful thing that
could happen to it.

**Decided along the way:** the provisioner is written in **Go**, and anything an
operator reads, edits, or runs on a real host stays shell
([ADR-001](docs/design/ADR-001-implementation-language.md)).

## Layout

```
cmd/csb/             the binary
internal/            topology · assembly · backend · fault · inspect · load ·
                     record · run · selector · store · engine · cli
e2e/                 the whole surface against a real engine, on JSON envelopes
scenarios/           sequences you can run against a build: split brain,
                     failback, the switchover-threshold sweep
docs/
  DESIGN.md          the design document — problem, goals, architecture, decisions
  ROADMAP.md         phases, milestones, and where the project actually is
  design/            01 command surface · 02 topology · 03 assembly · 04 faults
                     05 inspection · 06 load · 07 the run record
                     ADR-001 language · ADR-002 backend contract
  requirements/      what the field asks for, from CUBRID's internal tracker
  survey/            PostgreSQL, MySQL, MongoDB, TiDB, and the CUBRID gap analysis
  findings/          what running it showed — including where it contradicted the design
  assets/            banner · architecture · topology, and three animated figures:
                     assembly, split brain, replication lag
harness/
  lib.sh             the Phase-0 spike: the four-step assembly, done by hand
  oq9-splitbrain.sh · oq7-lag.sh · sweep-switchover.sh · calc-score-window.sh
                     the experiments behind docs/findings/
  failback.sh        the operator script, and the question it still asks
  results/           captured console output and samples
```

## Running the experiments

The `harness/` scripts predate the tool and are kept because the findings cite
them. Each builds its own network and containers and removes them on exit; they
need Docker and a built install tree (`ENGINE=<path to install.out>`).

```bash
cd harness
bash oq9-splitbrain.sh A|B|C     # ~4 min per arm
bash oq7-lag.sh                  # ~7 min
bash failback-demo.sh            # ~5 min — sets up the failed-over state, then runs failback.sh
```

New work belongs in `scenarios/` instead: same sequences, one command, against
any build, with the run record as evidence.

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
