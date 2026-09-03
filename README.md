![cluster-sandbox — multi-node CUBRID topologies for development: one command, your own build, and the states you need one for as verbs: lag, split brain, the trip back](docs/assets/banner.svg)

**cluster-sandbox** stands a multi-node CUBRID topology up in containers, from
one command and your own build. It reproduces the states you need a cluster for —
a slave that has fallen behind, two nodes that both believe they are master, the
return trip after a failover — as verbs addressed by role, and runs a written-down
sequence of those verbs against a build.

For engine developers, QA, and external contributors. Part of
[CUBRID Systems Research](https://github.com/cubrid-systems).

> **Where it is:** phases 0, 1 and 2 are complete; phase 3 is in progress.
> Thirty-seven verbs across eight nouns, all built and driven against a real
> engine by `make e2e`. See [Status](#status).

## Prerequisites

| | Why |
|---|---|
| **Docker** | a node is a container, and a partition is a route operation inside one. Tested against 29.x |
| **Go** | to build `csb`. There are no binary releases yet |
| **A CUBRID install tree** | the engine under test. Your own build (`install.out`), or an unpacked release — bind-mounted read-only, never put in an image |

Linux x86-64 for now. `csb` reads the highest `GLIBC_` symbol your build requires
out of the ELF and refuses with that sentence if the container's libc cannot load
it.

## Install

```bash
git clone https://github.com/cubrid-systems/cubrid-cluster-sandbox
cd cubrid-cluster-sandbox
make dist                      # a static binary at bin/csb
sudo install bin/csb /usr/local/bin/     # optional
```

- `make check` — gofmt, `go vet`, unit tests. No Docker, no engine.
- `make e2e CSB_E2E_BUILD=~/cubrid/install.out` — the whole surface against a
  real build, about two minutes. Run it against an engine build before trusting
  the tool with one.

State lives under `$CSB_HOME` (default `~/.local/share/csb`), one directory per
cluster holding its `describe` artifact and its run record.

## Getting started

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

The engine is bind-mounted from your tree, so rebuilding it rebuilds nothing
here. Then use it — `node exec` runs a command with the engine's environment
already set:

```bash
$ csb node exec master -- "csql -u dba -c 'CREATE TABLE t(i INT PRIMARY KEY);' hadb"
$ csb cluster describe --json | jq .data.engine
{ "kind": "build", "version": "11.5.0", "commit": "dd15f7f", "min_glibc": "2.34" }
$ csb cluster destroy --cluster hadb        # keeps the run record; --purge drops it
```

`master` and `slave` are queries, not labels: after a failover `master` names the
other machine, so a script written before the failover runs unchanged after it.
`slave[0]`, `n1` and `all` also select.

### What the command did on your behalf

![The assembly, animated: a CSB lane showing what the tool does and an ENGINE lane showing what the engine is doing meanwhile, with a callout naming each point where a script doing the obvious thing would break — seeding on the databases.txt entry before createdb has returned, copying the database directory instead of hadb star without the lock file, starting the heartbeat one node at a time, and reporting the cluster ready before the master is writable](docs/assets/anim-create.svg)

Seven ordering traps, and every transition decides on observed state rather than
on the exit code of the command that was supposed to cause it. What each one is
and why: [`docs/design/03-assembly.md`](docs/design/03-assembly.md) §2.

## Break it on purpose

Faults are **events**, which happen and are over, and **conditions**, which are
entered, held and cleared. Every condition records how to reverse itself,
`csb fault ls` shows what is in force, and `csb cluster describe` carries it.

```bash
csb fault partition master --keep ping-host   # split brain, on request
csb fault lag slave --stage apply             # one stage, not "slow"
csb fault splitbrain --flavour ping-survives
csb fault failcount slave --rows 200          # move fail_counter deliberately
csb fault ping-unavailable slave
csb fault clear --all
```

![The same route-level cut, twice, animated. On the left ha_ping_hosts is set and the ping host survives: the master pings successfully, concludes it is not partitioned and stays master, while the slave pings successfully, finds nothing to cancel its failover and promotes — two masters in 9 s, from a correct configuration. On the right the ping host is cut from the master too: it demotes itself, the failover is clean, and forty-five seconds after the heal the roles are still swapped because only one master exists and nothing triggers](docs/assets/anim-splitbrain.svg)

`partition` cuts routes rather than interfaces, which is what makes `--keep`
possible: split brain needs a surviving ping host, not a broken configuration.
`fault failcount` is the one verb `clear` cannot reverse — its damage is data, so
it exits 3 and points at `ha resync`, which performs the engine's own
`ha_make_slavedb.sh` rebuild and reports which repair path it chose.
Mechanisms and flavours: [`docs/design/04-faults.md`](docs/design/04-faults.md).

## Watch what it did

```bash
csb repl status --json                     # both stages, against the master
csb repl check --wait 30s                  # a write that has to arrive
csb repl watch --interval 0.5s --out lag.tsv
csb repl diff --table t                    # what the two databases actually hold
csb ha status
csb record show --since 5m
csb record export --out run.html --format html
```

![The replication pipeline and its gauge, animated. Suspending the apply stage freezes every column of db_ha_apply_info for thirty seconds, eof_lsa included, because applylogdb is what writes the row — the reported lag holds at 27,786 and the truth, 54,855, arrives in a single sample on release. Suspending the copy stage instead freezes eof_lsa while the applier keeps draining, so the reported lag falls from 49,544 to 38,576 while replication is entirely stopped. Only applyinfo -r, read against the master, sees either](docs/assets/anim-lag.svg)

There is no field called `delay`. `repl status` reports the copy stage and the
apply stage separately against a master-side reference, and reports `null` with a
reason rather than a number its source cannot support. `repl check` proves the
path is open; `repl diff` compares the catalog, which is the only thing that
proves the two databases agree
([`docs/design/05-inspect.md`](docs/design/05-inspect.md)).

## Write it down as a scenario

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

A step is an argv this tool already accepts, dispatched through the same path
with the same exit code. Judgements come from the verbs themselves — `repl check`
exits 4 when the row does not arrive, `repl diff` exits 1 when the sides differ —
plus `contains`/`absent` on what a step printed, `role_change_within` against the
record's measured interval, and `await` for a state to arrive.

The build is an argument to the run rather than a field in the file, so one
scenario runs against the build you just made and the one you are comparing it
with. `matrix` and `repeats` turn one scenario into many runs, `${name}`
substitutes into the cluster parameters and every step, and `measure` names what
to collect from a closed list of fields the tool already emits
([`docs/design/01-cli.md`](docs/design/01-cli.md)).

## Load

```bash
csb load start --profile insert --rate 2000/s --batch 200 --require-rate
csb load status --json
csb load stop
```

The driver targets a rate, holds it, and reports whether it held it;
`--require-rate` turns a miss into exit 1. `--batch` separates volume from rate —
`rows/s` is `rate × batch` — and both are reported separately. Latency comes back
as p50/p90/p99 per statement, absent below twenty samples.

Profiles are two different things and are not interchangeable: `insert`,
`update`, `mixed` and `bulkload` saturate the master's transaction path;
`host-cpu` and `host-io` saturate the node itself, which is what makes heartbeat
responses miss their window.

`--clients N` puts the driver on a node of its own — part of the cluster, not
part of the HA group — instead of inside the master, where it competes with the
engine for the engine's CPU quota. `--tools DIR` mounts a host directory
read-only at `/tools` on those nodes
([`docs/design/06-load.md`](docs/design/06-load.md)).

## The interface

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

Every command takes `--json` and has a documented exit code. Human output may
change; `--json` is the contract:

```json
{ "schema": "csb/v1", "command": "cluster status", "cluster": "hadb",
  "at": "2026-08-28T07:14:22Z", "ok": true, "data": {}, "notes": [] }
```

`data` never carries a figure its source cannot support — the field is `null` and
`notes` says why, never zero. `notes` is a list of `{code, severity, message}`,
not prose. Timestamps are the sample's, not the report's
([`docs/design/01-cli.md`](docs/design/01-cli.md) §4).

## Architecture

![Architecture: consumers over one command surface, six components — topology model, provisioner core, container backend, fault injection, inspector, load driver — acting on a two-node cluster in one network, and emitting two artifacts: describe and record](docs/assets/architecture.svg)

Six components and two artifacts — `describe` stands the same cluster up
elsewhere, and the run record says what happened to this one.
[`docs/DESIGN.md`](docs/DESIGN.md) §4 fixes the boundaries;
[`docs/design/`](docs/design/) specifies what crosses each one.

`--network tailnet` puts the nodes on a tailnet instead of a docker bridge, so a
topology can span machines ([ADR-002](docs/design/ADR-002-backend-contract.md)).

## Status

**Phases 0, 1 and 2 are complete**, 0 on 2026-08-27 and 1 and 2 on 2026-09-02.
Phase 3 is in progress ([`docs/ROADMAP.md`](docs/ROADMAP.md)).

What has been measured with it — questions the field asked and could not answer:

| Question | Answer | Where |
|---|---|---|
| Does split brain need a broken configuration? | **No** — a correctly configured cluster reaches two masters in 9 s when the ping host survives the partition | [`split-brain.md`](docs/findings/split-brain.md) |
| How is replication lag injected, and does the heartbeat allow it? | Suspend a stage; the heartbeat watches process *existence*, not progress, and does not interfere | [`replication-lag.md`](docs/findings/replication-lag.md) |
| Is the return to the original master mechanically possible? | Yes — restored in 2 s with no row loss. The policy around it is not settled | [`failback.md`](docs/findings/failback.md) |
| What actually decides when a cluster switches over? | Not the documented arithmetic. Nineteen runs: raising either heartbeat parameter fourfold leaves the measurement inside its own baseline band; `ha_calc_score_interval_in_msecs` moves it, by about 2× on means | [`switchover-threshold.md`](docs/findings/switchover-threshold.md) |
| Does a healed partition run Active-Active, syncing both ways? | The window is real and is as long as `ha_calc_score_interval_in_msecs` — ~12 s at 15000 against ~1 s at the default. Rows cross in one direction only, and the divergence that is left is permanent and reported healthy by every gauge | [`active-active-window.md`](docs/findings/active-active-window.md) |

**One decision short.** `ha failback` performs the return trip and stops where a
person has to choose: who authorises it, and on what evidence. Nobody has written
that down, so the step asks rather than assumes
([`harness/failback.sh`](harness/failback.sh)).

## Why not one of the existing tools

| Tool | What it is | Why it is not this |
|---|---|---|
| `cubrid-contrib/sandbox` | a single-container build shell | one node, and it builds the engine rather than running a topology |
| `cubrid-contrib/docker_for_ctp` | a two-container rig that drives CTP | a test rig against a released tarball |
| `cubrid-testkit` | the test harness succeeding CTP | treats HA as a workload it runs, not one it provisions |
| `cubrid-operator` | production Kubernetes deployment | operational lifecycle, not local iteration |

Nobody provisions a development topology. That is the gap this fills.

## Layout

```
cmd/csb/             the binary
internal/            topology · assembly · backend · fault · inspect · load ·
                     record · run · selector · store · engine · cli
e2e/                 the whole surface against a real engine, on JSON envelopes
scenarios/           sequences you can run against a build
docs/
  DESIGN.md          problem, goals, architecture, decisions
  ROADMAP.md         phases, milestones, and where the project is
  design/            01 command surface · 02 topology · 03 assembly · 04 faults
                     05 inspection · 06 load · 07 the run record
                     ADR-001 language · ADR-002 backend contract
  requirements/      what the field asks for, from CUBRID's internal tracker
  survey/            PostgreSQL, MySQL, MongoDB, TiDB, and the CUBRID gap analysis
  findings/          what running it showed
  assets/            the figures used above
harness/             the phase-0 experiments the findings cite, and failback.sh
```

The `harness/` scripts predate the tool and are kept because the findings cite
them; each needs Docker and `ENGINE=<path to install.out>`. New work belongs in
`scenarios/`.

## Relationships

- **`cubrid-testkit`** consumes this tool: it owns suites, dispatch and
  reporting; this project owns the environment they run on.
- **`cubrid-operator`** is not a dependency. It serves production Kubernetes;
  this serves a developer setting up an environment.
- **CUBRID Ops** owns engine-internal metrics. This project leaves a documented
  seam and does not build a second collector.

## Licence

Apache License 2.0 — see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).

The slave rebuild follows the steps and ordering of CUBRID's own
`share/scripts/ha/ha_make_slavedb.sh`. No code from it is copied; the sequence is
reused and attributed where it is used.
