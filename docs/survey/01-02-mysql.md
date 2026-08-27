---
title: MySQL — Local Multi-Node Provisioning (Survey 02)
category: survey
project: cluster-sandbox
status: selected
lang: en
sources:
  - https://github.com/datacharmer/dbdeployer — README.md, docs/features.md, wiki/main-operations
  - https://dev.mysql.com/doc/mysql-shell/8.0/en/deploy-sandbox-instances.html — `dba.deploySandboxInstance()`
  - https://dev.mysql.com/doc/mysql-shell/8.0/en/manage-sandbox-instances.html — start / stop / kill / delete
  - 01-00-overview.md §3 (D1–D5); 01-01-postgresql.md (harness-as-provisioner pattern)
summary: The most informative single system, because MySQL has three answers at once. `dbdeployer` is the community tool and the only one in this survey that treats topology as a named choice — `--topology=master-slave|group|all-masters|fan-in` — with binaries unpacked from tarballs into a versioned directory that deployments reference by name. MySQL Shell's AdminAPI is the vendor-official answer and is deliberately smaller: numbered sandbox instances on ports, explicitly for testing, with `killSandboxInstance()` as a first-class abrupt-stop verb next to the graceful `stopSandboxInstance()`. Both stop at process isolation and neither provisions a network fault.
created: 2026-08-18
updated: 2026-08-18
tags: [roadmap, survey, cluster-sandbox, mysql, dbdeployer, mysql-shell, adminapi, sandbox, provisioning]
---

**Contents:**

- [1. Three answers in one ecosystem](#1-three-answers-in-one-ecosystem)
- [2. The five decisions](#2-the-five-decisions)
- [3. Answers to the overview's five questions](#3-answers-to-the-overviews-five-questions)
- [4. Implications for CUBRID](#4-implications-for-cubrid)

## 1. Three answers in one ecosystem

**`dbdeployer` (community).** The successor to MySQL-Sandbox, written in Go.
Binaries are acquired and registered in two steps and then referred to by
version for the rest of the tool's life:

```
$ dbdeployer downloads get mysql-8.0.4-rc-linux-glibc2.12-x86_64.tar.gz
$ dbdeployer unpack mysql-8.0.4-rc-linux-glibc2.12-x86_64.tar.gz
$ dbdeployer deploy single 8.0.4
Database installed in $HOME/sandboxes/msb_8_0_4
```

`unpack` extracts into the sandbox-binary directory (`$HOME/opt/mysql` by
default) as a directory named for the version; deployments then reference
`8.0` (resolved to the newest matching), an exact `8.0.22`, or an explicit
`/path/to/version`. Topology is a flag:

```
$ dbdeployer deploy --topology=master-slave replication 5.7.21
$ dbdeployer deploy --topology=group        replication 8.0
$ dbdeployer deploy --topology=all-masters  replication 5.7
$ dbdeployer deploy --topology=fan-in       replication 5.7
```

Default is master-slave with one master and two replicas, and replication
starts by itself. The feature list also carries semi-sync, Percona XtraDB
Cluster, MySQL Cluster, and inter-sandbox replication, plus operational
niceties that reveal what long use demands: a **global catalog of sandboxes**,
port-collision avoidance recorded in a per-sandbox JSON description,
lock/unlock, concurrent deployment, and a per-sandbox test script.

**MySQL Shell AdminAPI (vendor-official).** Deliberately narrower:
`dba.deploySandboxInstance(port)` creates one instance under
`$HOME/mysql-sandboxes/<port>`, and the documentation frames sandboxes as being
for testing rather than production. Composition into an InnoDB Cluster is a
separate AdminAPI step, so the sandbox layer provisions *instances* and the
cluster layer assembles them.

**`mysql-test-run.pl`.** The in-tree harness, the MySQL analogue of
PostgreSQL's `Cluster.pm` — noted here for completeness; the two community and
vendor tools above are the developer-facing surfaces and carry the findings.

## 2. The five decisions

**D1 topology.** `dbdeployer` is the only tool in this survey with **named
topologies as a first-class argument**. AdminAPI has no topology concept at the
sandbox layer at all — instances are created one at a time by port.

**D2 artifact source.** Downloaded release tarballs, unpacked into a versioned
binary repository. The `/path/to/version` form means a directory that *looks
like* an unpacked tarball can be used, so a locally built tree is reachable;
the documentation does not describe registering a build tree as a supported
flow, which is a meaningful difference from PostgreSQL's `install_path` and
MongoDB's `--binarypath`.

**D3 isolation.** Processes and ports on one host, for both tools. Port
management is prominent enough in `dbdeployer` (collision prevention, recorded
ports, free-port discovery) to read as a tax that container isolation would not
levy.

**D4 lifecycle and fault verbs.** Both ship an abrupt stop, and both separate
it from the graceful one:

| | dbdeployer | AdminAPI |
|---|---|---|
| start | `./start` | `dba.startSandboxInstance()` |
| graceful stop | `./stop` | `dba.stopSandboxInstance()` |
| **abrupt stop** | `./kill` | **`dba.killSandboxInstance()`** |
| restart | `./restart` | — |
| reset | `wipe_and_restart`, `wipe_and_restart_all` | — |
| delete | `dbdeployer delete` | `dba.deleteSandboxInstance()` |

Multi-node sandboxes get per-node scripts plus `_all` variants, so "kill node
2" is available even though "partition node 2" is not. Neither tool offers
promotion as a verb — in MySQL's replication model that is a client-side
operation, which is why the verb set stops where it does.

**D5 observability.** Sandbox listing and a global catalog (`dbdeployer`);
nothing beyond instance status in AdminAPI. Tier 1 at most.

## 3. Answers to the overview's five questions

1. **Fault verbs.** `kill` per node in both tools, cleanly distinguished from
   graceful stop. No partition, no promote.
2. **Local build path.** Reachable through the `/path/to/version` form but not
   documented as a supported workflow. Weakest of the four systems on this axis.
3. **Where provisioning lives.** Outside the server repository, in two places
   at once — a community tool that grew the rich topology surface, and a vendor
   API that stayed minimal. The vendor did not absorb the community tool.
4. **Observability tier.** Tier 1 — sandbox inventory, ports, status.
5. **What it refused to do.** AdminAPI refuses production use explicitly.
   `dbdeployer` refuses Windows, and refuses master-master circular replication
   while supporting all-masters and fan-in.

## 4. Implications for CUBRID

**I1 — Named topologies are the right D1 shape for a first release.**
`dbdeployer`'s `--topology=` is the only interface in the comparable set that
lets a developer say what they want in one word, and CUBRID's need is narrower
than MySQL's — master-slave, and later replica and broker variants. A preset
name plus a node count covers the measured case without a topology document,
and `../DESIGN.md` §9 OQ5 can be answered incrementally rather than by
designing a schema up front.

**I2 — The kill/stop distinction is universal and cheap.** Every tool in this
survey that ships one fault verb ships *this* one, and separates it from
graceful stop. For CUBRID it maps directly: graceful stop exercises the
shutdown flush (`serial_flush_cache_pool`), abrupt kill does not — the exact
pair the CBRD-26983 verification had to construct by hand with
`cubrid heartbeat stop` versus `pkill -9`. Two verbs, and the most valuable
scenario split in the measurement comes for free.

**I3 — Port bookkeeping is a cost containers remove.** A large share of
`dbdeployer`'s accumulated machinery — port catalogs, collision prevention,
free-port discovery — exists because sandboxes share one host network. CUBRID's
containers get one namespace per node and the whole category disappears,
which is a concrete argument for the D3 choice beyond the network-fault one
that `01-01` §4 I3 already established.
