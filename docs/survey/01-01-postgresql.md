---
title: PostgreSQL — Local Multi-Node Provisioning (Survey 01)
category: survey
project: cluster-sandbox
status: selected
lang: en
sources:
  - src/test/perl/PostgreSQL/Test/Cluster.pm (postgres/postgres, master) — in-tree TAP cluster module
  - https://salsa.debian.org/postgresql/postgresql-common/-/raw/master/pg_createcluster — POD synopsis and options
  - 01-00-overview.md §3 (the five decisions D1–D5)
summary: PostgreSQL splits the problem in two and neither half is a developer cluster tool. The packaging layer (`pg_createcluster`) provisions exactly one cluster and has no notion of a replica. The multi-node capability lives in the in-tree test harness `PostgreSQL::Test::Cluster`, which builds a streaming standby from a base backup in three calls and ships the fault verbs — `promote`, `kill9`, `stop('immediate')` — that the packaging tool lacks. Its binaries come from an `install_path` argument, which makes a locally built tree first-class. What neither provides is network-level disruption, and that is the one primitive CUBRID's HA verification actually needed.
created: 2026-08-18
updated: 2026-08-18
tags: [roadmap, survey, cluster-sandbox, postgresql, tap-harness, streaming-replication, provisioning]
---

**Contents:**

- [1. Two tools, two halves of the problem](#1-two-tools-two-halves-of-the-problem)
- [2. The five decisions](#2-the-five-decisions)
- [3. Answers to the overview's five questions](#3-answers-to-the-overviews-five-questions)
- [4. Implications for CUBRID](#4-implications-for-cubrid)

## 1. Two tools, two halves of the problem

**`pg_createcluster` — packaging, single node.** Its synopsis is
`pg_createcluster [options] <version> <name> [-- <initdb options>]`. Version is
a positional argument, so running several major versions side by side is the
normal case, and `--datadir`, `--port`, `--locale`, `--start-conf` cover the
per-cluster surface. It creates the configuration tree under
`/etc/postgresql/<version>/<name>/` and registers the cluster with the
`postgresql-common` infrastructure that `pg_ctlcluster` and `pg_lsclusters`
then operate on. It has **no functionality for standbys, replicas, or
multi-node setups** — the script is single-cluster provisioning by
construction. It is also Debian packaging rather than upstream, which bounds
how far it can be cited as "how PostgreSQL does it".

**`PostgreSQL::Test::Cluster` — in-tree harness, multi-node.** This is where
PostgreSQL's replication topologies are actually stood up. The module is part
of the source tree (`src/test/perl/`) and exists to let TAP tests build
clusters programmatically. A streaming standby is three calls:

```perl
$primary->backup('bkp');                       # pg_basebackup
my $standby = PostgreSQL::Test::Cluster->new('standby');
$standby->init_from_backup($primary, 'bkp');
$standby->enable_streaming($primary);          # primary_conninfo + standby.signal
$standby->start;
```

The shape is worth noting: **backup on the primary, restore into the standby,
then flip it into standby mode** — the same three-step chain CUBRID's manual
assembly used (`backupdb` → copy → `restoreslave`), which suggests the chain is
inherent to physical replication rather than a CUBRID awkwardness.

## 2. The five decisions

**D1 topology.** Imperative, not declarative. There is no topology document —
the test author writes Perl that constructs nodes and wires them. Presets do
not exist; the primitives (`new`, `init`, `backup`, `init_from_backup`,
`enable_streaming`, `enable_restoring`, `set_standby_mode`,
`set_recovery_mode`) are composed per test.

**D2 artifact source.** `PostgreSQL::Test::Cluster->new()` takes an
`install_path`; when given, the module prefixes command names with
`$install_path/bin/` and adjusts `PATH` and `(DY)LD_LIBRARY_PATH` accordingly,
otherwise it falls through to `PATH`. **A locally built tree is a first-class
argument**, and cross-version testing (one node from one install path, another
from a different one) follows from it.

**D3 isolation.** Processes on one host, distinguished by port and data
directory. No containers, no network namespaces.

**D4 lifecycle and fault verbs.** The richest set in this survey's comparable
group, and all of it inside a test harness:

| Verb | Method |
|---|---|
| start / restart / reload | `start`, `restart`, `reload` |
| graceful and abrupt stop | `stop` with mode (`'fast'`, `'immediate'`) |
| **crash** | `kill9` — SIGKILL to the postmaster |
| **promote a standby** | `promote` — `pg_ctl promote` |
| backup / restore | `backup`, `backup_fs_cold`, `init_from_backup` |
| teardown | `teardown_node` |

What is **absent** is any network-level disruption: no partition, no link
severing, no packet manipulation. `connstr` and `raw_connect` expose the
connection string and a raw socket, leaving disruption to the test author.

**D5 observability.** None as a product feature. The module is consumed by
tests, so state is asserted with SQL and log inspection rather than displayed.

## 3. Answers to the overview's five questions

1. **Fault verbs.** Ships `kill9`, `stop('immediate')`, and `promote`. Does not
   ship partition. The absence is notable precisely because everything else is
   there — it suggests network-level fault injection sits outside what a
   process-level harness can offer, and needs a different substrate (D3).
2. **Local build path.** First-class, via `install_path`.
3. **Where provisioning lives.** Split. Packaging owns single-node
   convenience; the **source tree owns multi-node**, as test infrastructure
   rather than as a developer-facing tool. There is no upstream CLI that gives
   a developer a two-node cluster.
4. **Observability tier.** Tier 0 — none. The consumer is a test, not a person.
5. **What it refused to do.** `pg_createcluster` declines multi-node outright.
   `Cluster.pm` declines to be a user interface: it is a Perl API, discoverable
   only by reading the module.

## 4. Implications for CUBRID

**I1 — The harness-as-provisioner pattern is real, and it stops short of being
a tool.** PostgreSQL has every primitive `cluster-sandbox` wants and exposes
none of them to a developer who is not writing a TAP test. This is direct
evidence for `../DESIGN.md` §9 OQ3: a test harness *can* own provisioning,
but if it does, the developer-facing surface still has to be built on top. The
CUBRID variant of the question — whether `cubrid-testkit` provides the
environment — inherits this shape: testkit could own the primitives and
`cluster-sandbox` the surface, which is a cleaner split than either owning both.

**I2 — `install_path` validates the local-build requirement as ordinary.**
CUBRID's requirement to point at a local build directory is not exotic; the
reference implementation of a database test harness has taken exactly that
argument for years.

**I3 — Process isolation buys everything except the partition.** PostgreSQL's
harness reaches the limit of process-level isolation precisely at network
faults. CUBRID's measured HA work needed to *cut a link* to induce failover, so
this is the axis where `cluster-sandbox` cannot follow PostgreSQL's substrate
choice — and the strongest argument in the survey so far for containers (D3).
