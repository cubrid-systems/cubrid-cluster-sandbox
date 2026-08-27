# cluster-sandbox

A CLI-first provisioner that stands a CUBRID topology up in containers from a
declarative configuration — node count and roles, engine version *or* a local
build directory, per-node parameters — so that engine developers, QA, and
external contributors can reproduce a multi-node setup without hand-assembling
it, and can then reproduce the *states* that setup does not hand them: a slave
that has fallen behind, two nodes that both believe they are master, and the
operator-driven return to the original master after a failover.

Part of [CUBRID Systems Research](https://github.com/cubrid-systems). [`docs/DESIGN.md`](docs/DESIGN.md) is the design;
[`docs/design/`](docs/design/) is where it is being specified; and
[`docs/ROADMAP.md`](docs/ROADMAP.md) says where the project actually is.

## Why

Answering one HA question for CBRD-26983 required a two-node cluster built by
hand, and every step of that assembly is mechanical: two configuration files per
node, a four-step slave chain, a start ordering, and two undocumented traps. The
neighbouring tools each solve an adjacent problem — `cubrid-contrib/sandbox` is a
single-container *build* shell, `docker_for_ctp` drives CTP against a released
tarball, `cubrid-testkit` is the *test harness* and treats HA as a workload
rather than as something it provisions, and `cubrid-operator` deploys to
production Kubernetes. Nobody provisions a development topology.

## Status

**Phase 0.** The manual assembly is written down and runs (`harness/lib.sh`), and
the questions the design was unsure about have been answered by measurement
rather than by reading:

| Question | Answer | Where |
|---|---|---|
| Does split brain need a broken configuration? | **No** — a correctly configured cluster reaches two masters in 9 s when the ping host survives the partition | [`docs/findings/split-brain.md`](docs/findings/split-brain.md) |
| How is replication lag injected, and does the heartbeat allow it? | Suspend a replication stage; the heartbeat watches process *existence*, not progress, and does not interfere | [`docs/findings/replication-lag.md`](docs/findings/replication-lag.md) |
| Is the operational failback mechanically possible? | Yes — the original master was restored in 2 s with no row loss. What is *not* settled is the policy around it | [`docs/findings/failback.md`](docs/findings/failback.md) |

The one open question this project cannot close on its own is what the technical
team actually requires of failback ([`docs/DESIGN.md`](docs/DESIGN.md) §9 OQ8).
[`harness/failback.sh`](harness/failback.sh) is the instrument for asking them:
it encodes this project's guess at the operator sequence, with a decision point
at every step where the guess is a guess, and it is meant to come back marked up.

## Layout

```
docs/
  DESIGN.md          the design document — problem, goals, architecture, decisions
  ROADMAP.md         phases, milestones, and where the project actually is
  design/            the design below the architecture: command surface, topology
                     model, assembly, fault vocabulary, inspection
  survey/            PostgreSQL, MySQL, MongoDB, TiDB, and the CUBRID gap analysis
  findings/          what running it showed — including where it contradicted the design
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
