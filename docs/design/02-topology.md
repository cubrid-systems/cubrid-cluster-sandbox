---
title: cluster-sandbox — Topology model
category: design
project: cluster-sandbox
summary: What a topology is, in this tool. A named preset plus a count plus per-node overrides, with a declarative document deferred until the catalogue outgrows flags. Fixes the name-derived identifiers, the engine-source model, and the schema of the describe artifact.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# Topology model

Layer 1 of [`../DESIGN.md`](../DESIGN.md) §4. It answers the survey's first
decision — *what is a topology?* — and every other layer takes its input from
here.

## 1. Presets, not a schema — for now

A topology is **a named preset, a node count, and per-node overrides**. There is
no configuration document in phase 1, and that is a decision with an expiry
date rather than a principle.

The reason to start with flags: MongoDB's `mlaunch` expresses a sharded,
multi-router, three-config-node cluster in flags (`../survey/01-03-mongodb.md`
§2), which puts CUBRID's near-term catalogue — one HA pair — well inside what
presets carry. Designing a document first would spend the budget before the
catalogue is known.

The reason it will not last: replica nodes, broker/CAS tiers, shard
configurations and CDC consumers each bring their own configuration surface and
their own fault verbs. **The migration trigger is the first topology a user asks
for that the preset vocabulary cannot express** ([`../DESIGN.md`](../DESIGN.md)
§9 OQ5). `describe` (§4 below) is deliberately the shape that document will take,
so the migration is a promotion of an existing output rather than a new design.

### Presets

| Preset | Nodes | What it is |
|---|---|---|
| `ha` | 2 (default) | one master, one standby. The case that motivated the project |
| `single` | 1 | `ha_mode=off`, one server. For the many bugs that are not HA bugs |

`replica`, `broker`, and `shard` are phase 3 **as topology shapes**. A single
broker in front of the `ha` preset is not a shape and arrives earlier, as
`--with-broker`: the field's way of blocking writes before it touches replicated
data is to move the broker's `ACCESS_MODE` to RO/SO
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §4),
and a cluster with no broker has no door to close ([`04-faults.md`](04-faults.md)
§9). The assembly does not start one today
([`03-assembly.md`](03-assembly.md) §6). `single` is in phase 1 because a
tool that can only make clusters is the wrong tool for most of the work, and
because it is the likely shape of the `cubrid-contrib/sandbox` build-shell case
if that turns out to be a one-node topology with a `build` role
([`../DESIGN.md`](../DESIGN.md) §9 OQ1).

## 2. Identity

A cluster has a **name**, and everything else is derived from it. This is not
cosmetic: the assembly has two hard constraints that only a naming rule can
satisfy.

```
cluster name          hadb                 (default: the database name)
node names            hadb-n1, hadb-n2     (also the container hostnames)
network               hadb-net
database directory    /db                  INSIDE every container, identically
host work directory   <workdir>/<node>/
```

**Hostname = node name = container name.** The heartbeat resolves peers by
hostname, and `ha_node_list` is a list of hostnames; a container whose hostname
does not match its name is a debugging session nobody needs.

**Every node mounts its database directory at the *same* container path.**
`${DB}_vinf` stores **absolute** volume paths, so a node whose directory sits
elsewhere mounts the *other* node's files and dies with "is in use by … on host
&lt;peer&gt;". This was paid for in N54's WU-51b harness and is inherited rather
than rediscovered.

The build tree is mounted at **the same path inside and outside** the container,
which keeps compiler paths, core dumps, and debugger paths valid on both sides —
the second convention taken from `cubrid-contrib/sandbox`.

## 3. Where the engine comes from

Two sources, and the tool must not care which beyond resolving it:

```
--build /path/to/install.out     a developer's own tree, bind-mounted read-only
--version 11.5.0                 a released version the tool fetches
```

`--build` is the case that matters for engine work and the one most comparable
tools treat as an afterthought — though three of the four surveyed document it
(`install_path`, `--binarypath`, `--{comp}.binpath`), so it sits inside
precedent (`../survey/01-00-overview.md` §5.1 DI2).

It also costs almost nothing. CUBRID's binaries link only against libc, libm,
libgcc_s and libstdc++ plus what the install tree ships, so a host build runs
unmodified in a stock `ubuntu:24.04` container with the tree bind-mounted. There
is no image build in the `--build` path — a writable `$CUBRID` is assembled over
the read-only tree out of symlinks.

**The failure mode this creates is real and must be caught, not debugged.** A
tree built on a different distribution fails to load in the container. The tool
reads the highest `GLIBC_` symbol version the build requires — out of the ELF
itself, so nothing needs to run first — compares it with the image's own
`ldd --version`, and refuses with that sentence rather than with a linker error
([`../DESIGN.md`](../DESIGN.md) §7). It is a precondition failure, exit 3.

## 4. The `describe` artifact

The output of `csb cluster describe`, and the input to `csb cluster create
--from`. Its job is that a second person reproduces the *same* cluster — which
is one of the two things worth measuring for adoption
(`../survey/01-05-cubrid-gap.md` §3).

```yaml
schema: csb/v1
cluster: hadb
preset: ha
nodes:
  - name: hadb-n1
    role: master          # the role at create time, not now
    overrides:
      ha_copy_sync_mode: sync
  - name: hadb-n2
    role: slave
engine:
  kind: build
  path: /data/workspace/cubrid/install.out
  identity:               # a build tree is not portable; its identity is
    commit: 4dbff6ba0
    built_on: ubuntu-24.04
    built_at: 2026-08-26T16:05:00Z
parameters:               # every non-default, both files, per scope
  common:
    ha_mode: on
    ha_ping_hosts: ping-host
resources:                # what "saturated" means on this machine
  cpus: 4
  shm_size: 512m
faults:                   # WHAT IS CURRENTLY IN FORCE
  - kind: lag
    target: slave
    stage: apply
    mechanism: suspend
    since: 2026-08-28T07:12:00Z
load:                     # the spec in force, not the achieved rate
  profile: insert
  rate: 2000/s
  concurrency: 4
  seed: 42
quiesce:                  # absent when writes are not blocked
  mechanism: broker
  mode: ro
  since: 2026-08-28T07:15:30Z
```

Five things about this schema are load-bearing.

**`engine.identity` rather than `engine.path`.** A build tree does not travel,
so recording its path alone reproduces nothing. Recording the commit and the
build environment lets the second person produce an equivalent tree, and lets
the tool *tell them* when theirs is not.

**`role` is the role at create time.** After a failover the roles have swapped,
and an artifact that recorded the current roles would recreate the cluster
post-failover — which is a different cluster. Roles at create time plus the
fault list is what reproduces the situation.

**`faults` is not optional.** A `describe` taken during a partition that omits
the partition hands the next person a healthy cluster and a bug that does not
reproduce. This is the field a naive implementation drops.

**`load` and `quiesce` are in that same category.** A cluster reproducing a bug
under 2000 inserts a second is not the same cluster as an idle one, and a
cluster whose broker is read-only is not the same cluster as one taking writes.
Both are states a person can be in the middle of when they hit something worth
sharing ([`06-load.md`](06-load.md) §7, [`04-faults.md`](04-faults.md) §9).

**`resources` is here because `host-cpu` load is meaningless without it.**
"Saturated" on a 32-core workstation and on a 4-core CI runner are different
experiments; the node's CPU quota is what makes *N threads against M cores* a
reproducible statement rather than a coincidence
([`06-load.md`](06-load.md) §5).

### Rebuilding from it

Measured: **976 bytes** for a two-node cluster carrying a non-default parameter,
a hidden one, a CPU quota and a fault in force — which is the size test, since
the artifact is meant to be pasted into an issue.

`csb cluster create --from <artifact>` rebuilds it, through the same code path an
ordinary `create` takes. A second implementation would drift, and then the
artifact would stop reproducing what it says. Three things that path does that a
plain reload would not:

- **The engine is resolved against this machine.** `--build` wins; otherwise the
  recorded path is used if it happens to exist here. When neither is available
  the command refuses, naming the build the artifact was taken against; when the
  tree it finds is a *different* build it says so and continues. That is what
  `engine.identity` is for, and it is the difference between reproducing a
  topology and reproducing a result.
- **A rename rebuilds the derived fields** rather than substituting a string,
  because the node names, the network and the database all descend from the
  cluster name.
- **What was in force is reported, not re-applied.** The cluster comes up healthy
  and idle and the commands that would restore the situation are printed,
  translated back into role selectors — the recorded names belonged to the
  machine the artifact came from. Injecting a fault is a deliberate act, and
  silently partitioning a cluster somebody has just asked to be built is a
  surprise.

## 5. Parameters

Overrides land in one of two files and the user should not have to know which:

```
csb cluster create --set ha_ping_hosts=ping-host --set max_clients=200
```

The tool routes each key to `cubrid.conf` or `cubrid_ha.conf` by looking it up,
and refuses an unknown key rather than writing a file the engine will silently
ignore — "silent config divergence" is a named failure mode
([`../DESIGN.md`](../DESIGN.md) §7).

### Hidden parameters, and the hole that lookup rule leaves

**The three parameters that decide when a failover happens are not in the lookup
table, because they are not in `paramdump`.**
`ha_heartbeat_interval_in_msecs`, `ha_max_heartbeat_gap` and
`ha_calc_score_interval_in_msecs` are hidden; the lab confirmed as much when a
customer asked how failover is triggered, and the field's stalled threshold test
is a test *of these three*
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §2).
A rule that refuses every key it cannot look up refuses the entire subject of
M2.5.

Two tiers, then, and the second one is opt-in rather than lenient:

```
--set     key=value      known key; validated against the lookup table
--set-hidden key=value   a parameter the engine does not advertise
```

`--set` keeps its refusal, so a typo stays an error. `--set-hidden` writes
without validation, and everything written that way is **flagged in `describe`**:

```yaml
parameters:
  common:
    ha_mode: on
  hidden:                 # written unvalidated, on request
    ha_calc_score_interval_in_msecs: 300000
```

The flag is not bookkeeping. A cluster carrying a hidden parameter may be in a
state the engine's own documentation does not describe — the field reports an
Active-Active window under a raised `ha_calc_score_interval_in_msecs`
([`04-faults.md`](04-faults.md) §5) — and the next person to read the artifact
needs to know that before they trust anything measured on it. The lookup table
also cannot say whether a hidden key exists at all, so a misspelled
`--set-hidden` produces a file the engine ignores: `describe` showing a hidden
key that `paramdump` cannot confirm is the only warning available, and the tool
prints it as one.

### The ping mechanism is a topology choice, not a parameter detail

```
--ping-mode icmp|tcp|none        default: icmp
```

Three states, because the field has met all three and they fail differently
([`../requirements/01-failback-field-evidence.md`](../requirements/01-failback-field-evidence.md) §2):
`icmp` sets `ha_ping_hosts`, `tcp` sets `ha_tcp_ping_hosts` — which exists in the
engine because ICMP is denied to the DB account at some sites, where setting
`ha_ping_hosts` makes the engine fail to start — and `none` leaves both unset,
which is the default a real deployment starts from and one of the split-brain
flavours.

`--ping-mode` is a topology input rather than a `--set` key because it decides
*which* parameter is written, and because the container needs a different image
guarantee for each: `icmp` requires `ping` in the image, and its absence returns
127, which the caller reads as a failed ping
([`03-assembly.md`](03-assembly.md) §4). "The ping mechanism is unavailable" is
then a reproducible condition rather than an accident
([`04-faults.md`](04-faults.md) §10).

Two parameters are special and are handled by construction rather than by the
user:

- **`ha_copy_sync_mode` is left unset.** It takes one colon-separated entry per
  node in `ha_node_list`, so a value correct for one node is a hard
  `Invalid Parameter` startup failure for two. Unset, every node defaults to
  `sync` and the value cannot go out of step with the node count.
- **`ha_ping_hosts` is set by default**, because a cluster without it cannot
  diagnose a partition at all. A scenario may ask for it to be unset — that is
  one of the two split-brain flavours — and when it does, the deviation is named
  and travels in `describe` (principle 6 in [`README.md`](README.md)).

  **The host is the docker network's gateway**, and that is a requirement rather
  than a convenience: a ping host has to sit *outside* the pair, or a partition
  between the two nodes takes the ping host with it and neither side can tell
  "the peer is gone" from "I am gone". The gateway survives a route cut between
  the nodes, which is exactly what makes `ping-survives` and `no-ping-hosts`
  different scenarios ([`04-faults.md`](04-faults.md) §5). It is resolved on
  every `create` rather than read back from the artifact, because an address is
  local to the machine that issued it. `describe` carries it as `ping_host`.

  **For a while nothing wrote it.** `--ping-mode icmp` was the default, was
  recorded in `describe`, and never reached `cubrid_ha.conf`. What that cost is
  in the engine's own words, from a node that had just been left alone in the
  group: `[Failback] [Cancelled] No hosts are registered in ha_ping_hosts, or
  all registered hosts are invalid, making it impossible to determine`, on a
  loop, while the node sat in `to_be_active` instead of finishing its promotion.
  The end-to-end suite found it on its first run
  ([`../ROADMAP.md`](../ROADMAP.md) M3.4).

## 7. CTP compatibility, both directions

`cubrid-testkit` inherits CTP's `ha_repl` task, and CTP's external surface is
frozen while the old system keeps running. The conf file is therefore not ours to
change:

```
bin/ctp.sh ha_repl -c conf/ha_repl.conf
  env.<instance>.{master,slave}.ssh.host / .user
  env.<instance>.{cubrid,ha,broker1,broker2}.<key>
  cubrid_download_url= / scenario= / ha_sync_detect_timeout_in_secs=
```

**In:** `cluster create --from-ctp conf/ha_repl.conf` takes the engine parameters
and nothing else. The addresses in that file describe machines CTP would have
reached over ssh; a csb cluster's nodes are containers the command is about to
create, so their addresses are an *output* of the create rather than an input to
it. Validation is kept: an unknown key is refused and named, exactly as `--set`
refuses one, because the engine accepts a file with a key it ignores and the
divergence is then silent. A parameter the engine has and does not advertise —
`ha_max_heartbeat_gap` and the other two this project measured — is not a typo,
so it routes to `--set-hidden` and says so rather than being refused as unknown.
`cubrid_download_url` is answered out loud: csb bind-mounts a build from the host
and never puts an engine in an image, so `--build` decides what runs.

**Out:** `cluster describe --format ctp` writes the fragment back. It is a second
*rendering* of the describe artifact, never a second source — written from the
same JSON, so a cluster cannot describe itself one way to a reader and another to
a harness.

**The key names survive; the transport does not.** csb runs no sshd and publishes
no port, so `ssh.host` is filled with a container name and the fragment says so
in a comment instead of pretending to be a host. That is not a workaround around
testkit: their own ADR-014 already demotes `exec.SSH` to one implementation of a
`Channel`, and states the position this milestone depends on — *"the system under
test has a topology; the runner does not have a fleet."* Whatever stands the pair
up is somebody else, and the fragment is what it hands over.

Verified end to end on the real `CTP/conf/ha_repl.conf`: a cluster created from
it carried `max_clients=200` validated and `ha_max_heartbeat_gap=10` unvalidated
with both notes, and `describe --format ctp` wrote the pair back with the
database name and both parameters.
