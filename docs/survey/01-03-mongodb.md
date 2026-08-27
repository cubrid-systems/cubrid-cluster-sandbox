---
title: MongoDB — Local Multi-Node Provisioning (Survey 03)
category: survey
project: cluster-sandbox
status: selected
lang: en
sources:
  - https://rueckstiess.github.io/mtools/mlaunch.html — mlaunch (mtools) reference
  - 01-00-overview.md §3 (D1–D5); 01-01-postgresql.md; 01-02-mysql.md
summary: The single-command case, and the one design in the comparable set that solves the problem `cluster-sandbox` will hit first — how a developer names the node they want to break. `mlaunch init --replicaset --nodes 3 --sharded 2` builds a topology from flags with no configuration file, and the control verbs take *topology-aware tag selectors* rather than pids or ports: `mlaunch kill shard-a secondary` kills the secondaries of one shard. Binaries come from `--binarypath`, documented in as many words for people who compiled their own source. It stops at process isolation and offers no network fault.
created: 2026-08-18
updated: 2026-08-18
tags: [roadmap, survey, cluster-sandbox, mongodb, mlaunch, mtools, replica-set, sharding, provisioning]
---

**Contents:**

- [1. What mlaunch is](#1-what-mlaunch-is)
- [2. The five decisions](#2-the-five-decisions)
- [3. Answers to the overview's five questions](#3-answers-to-the-overviews-five-questions)
- [4. Implications for CUBRID](#4-implications-for-cubrid)

## 1. What mlaunch is

A community tool (part of `mtools`) that starts MongoDB topologies locally.
`init` requires exactly one of `--single` or `--replicaset`, and sharding
layers on top:

```
mlaunch init --replicaset --nodes 3 --arbiter --name rs0
mlaunch init --replicaset --nodes 3 --sharded 2 --config 3 --mongos 2 --csrs
```

`--nodes` is the data-bearing node count (default 3), `--sharded S` takes a
shard count or explicit shard names, `--config` takes 1 or 3, `--mongos`
defaults to 1. That is the entire topology surface — flags, no file.

## 2. The five decisions

**D1 topology.** Flags with sensible defaults, and no configuration document
anywhere. The interesting part is how far that goes: a three-shard cluster with
a three-member config replica set and two routers is one line. The lower bound
this survey wanted to establish — how much topology fits in presets before a
file becomes necessary — turns out to be *quite a lot*, provided the shapes are
named by the system's own vocabulary (replica set, shard, config server,
router) rather than by a generic node list.

**D2 artifact source.** `--binarypath PATH` points at a directory containing
`mongod` and `mongos`. The documentation states the intent directly: *"This is
useful for example if you compile your own source code and want mlaunch to use
the compiled version."* Along with PostgreSQL's `install_path`, this is the
second of four systems where pointing at a local build is an explicitly
supported, documented flow.

**D3 isolation.** Processes and ports on one host.

**D4 lifecycle and fault verbs — the distinctive part.** `start`, `stop`,
`restart` and `kill` all take **tag filters** that name nodes by their role in
the topology:

```
mlaunch kill mongos                 # all routers
mlaunch kill shard-a secondary      # the secondaries of one shard
mlaunch restart                     # everything
```

This is a different design from every other tool in the comparable set. Where
`dbdeployer` gives per-node scripts and `tiup playground` scales in by pid,
`mlaunch` lets the operator address nodes by *what they currently are*. In a
system where roles move — a secondary is elected primary, a node rejoins — that
distinction is the difference between a script that keeps working and one that
has to be rewritten after every failover.

**D5 observability.** `mlaunch list` shows the node overview, `--tags` prints
the addressable tags per node, and `--startup` prints the exact command each
instance was launched with. Tier 1, with the tag listing doubling as
discoverability for the verb selectors.

## 3. Answers to the overview's five questions

1. **Fault verbs.** `kill` with topology-aware selectors — the richest
   *addressing* model in the survey even though the verb set itself is small.
   No network partition, no explicit promotion (in MongoDB, election handles it,
   which is exactly why killing "the primary" by role is the way to trigger one).
2. **Local build path.** First-class and documented, via `--binarypath`.
3. **Where provisioning lives.** Entirely outside the server project, in a
   community tool, with no vendor equivalent for local topologies.
4. **Observability tier.** Tier 1, plus `--startup` as a reproducibility aid.
5. **What it refused to do.** No configuration file, no production framing, no
   fault injection beyond process control.

## 4. Implications for CUBRID

**I1 — Address nodes by role, not by identity.** This is the most transferable
idea in the survey. CUBRID's HA verification needed exactly this and had none of
it: the master had to be found by reading `cubrid changemode` output before it
could be killed, and after failover the same script pointed at the wrong node. A
verb set of `cluster kill master` / `cluster kill slave` / `cluster partition
master` would have replaced the whole improvised sequence, and roles are already
first-class in CUBRID's HA model (`master`, `slave`, `replica` in
`ha_node_list`; the state visible from `cubrid changemode`). The addressing
model is available for free — it just has to be chosen deliberately.

**I2 — Presets go further than expected, so a topology file can wait.**
Reinforces `01-02` §4 I1 from the opposite direction: MongoDB expresses a
sharded, multi-router, three-config-node cluster in flags. CUBRID's near-term
catalogue (HA pair, plus replica and broker variants) is smaller than that, so
`../DESIGN.md` §9 OQ5 does not need a schema before the first release.

**I3 — Print the startup command.** `mlaunch list --startup` is a small feature
with an outsized effect on the kind of work that motivated this project:
reproducing a bug means reproducing the exact invocation. A `cluster describe`
that emits the per-node command line and configuration would let a developer
attach a topology to a JIRA issue and have someone else recreate it.
