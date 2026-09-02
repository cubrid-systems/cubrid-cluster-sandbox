---
title: cluster-sandbox — Assembly
category: design
project: cluster-sandbox
summary: The state machine from an empty directory to a serving HA cluster, and the six ordering traps it owns on the user's behalf. Five of the six were paid for by hand; the sixth is the only one that produces a corrupt slave rather than a failed start.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# Assembly

Layers 2 and 3 of [`../DESIGN.md`](../DESIGN.md) §4 — the provisioner core and
the container backend. This is where most of the tool's value is, and it is
almost entirely *knowing an order*.

The reference implementation of the happy path already exists and runs:
[`../../harness/lib.sh`](../../harness/lib.sh) `cs_up`. This document says what
it has to become.

## 1. States

```
   absent
     │  create
     ▼
   defined         config written, containers exist, nothing started
     │  (master only) createdb
     ▼
   seeded          the slave has a copy of the master's volumes
     │  heartbeat start, on both, concurrently
     ▼
   forming         the group is assembling; nobody serves yet
     │  master reaches registered_and_active
     ▼
   serving         queries work; this is the only state a scenario may start from
```

Every transition is bounded and every one decides on **observed state**, not on
the exit code of the command that was supposed to cause it. That rule is not
defensive programming — see trap 6.

`create` is idempotent and resumable. A run that dies in `forming` leaves
containers, a network, and volumes behind; `csb cluster create` on the same name
picks up from the state it finds, and `csb cluster destroy` removes all three.
"Stale state after an interrupted run" is a named failure mode
([`../DESIGN.md`](../DESIGN.md) §7) and the answer is that state is derived from
the world, not from a lock file.

## 2. The traps this layer owns

Seven, in the order the assembly hits them. Five were paid for by hand during
the CBRD-26983 verification and N54's WU-51b work; the sixth was found by this
project's own runs; the seventh comes from the field's tracker.

**T1 — `ha_copy_sync_mode` needs one entry per node.** `util_common.c:894`
iterates `num_ha_nodes`, so a value that is correct for one node is a hard
`Invalid Parameter` startup failure for two. *The tool leaves it unset*, which
defaults every node to `sync` and cannot go out of step with the count.

**T2 — both nodes need the database directory at the same container path.**
`${DB}_vinf` holds absolute volume paths, so a slave whose directory is
elsewhere mounts the *master's* files and dies with "is in use by … on host
&lt;master&gt;". *The tool mounts every node's directory at `/db`.*

**T3 — the slave is built by copying volumes, and the copy has two edges.**
`createdb` writes volumes as **files** in the database directory
(`hadb`, `hadb_lgat`, `hadb_lginf`, `hadb_vinf`, …), not into a `hadb/`
subdirectory, so copying only `$DB` leaves the log behind. And the master holds
`${DB}_lgat__lock`, which vanishes mid-copy and breaks the `cp` chain. *The tool
copies `${DB}*` and excludes `*__lock`.*

**T4 — `cubrid heartbeat start` blocks until the group forms.** Run it on one
node and it waits for a peer that is not starting. *The tool starts it on both
nodes concurrently and joins.*

**T5 — the master is not writable the moment it is up.** It passes through
`registered_and_to_be_active`, and a write in that window fails with "Attempted
to update the database when updates are disabled" — so the first DDL of any
scenario fails. *The tool waits for `registered_and_active` before it reports
`serving`.*

**T6 — `databases.txt` does not mean `createdb` finished.** The entry appears
first. Seeding the slave on that signal copies a database with a live
transaction still in it; the slave's recovery then reaches its UNDO phase and
dies with `fetching deallocated pageid … of volume "/db/hadb"` →
`LOG FATAL ERROR: log_recovery:locator_initialize` (`log_recovery.c:953`).
Measured 2026-08-27. *The tool waits for an explicit completion signal.*

**T7 — a mixed HA / non-HA configuration makes `cubrid service start` skip HA,
silently.** With one HA database and one `ha_mode=off` database in the same
`cubrid.conf`, the local server starts first and the per-database
`[@dbname] ha_mode=off` section it applied is never restored; the heartbeat
starts **last**, reads `ha_mode=off`, prints `The server was not configured for
HA.` and declines. `cubrid hb start` afterwards works. Filed upstream as
CBRD-20568, **rejected as not a product issue**, and closed for 10.1
compatibility in 2017 — so it is current behaviour. *The tool never
writes `[@dbname]` sections carrying `ha_mode`, and starts the heartbeat
explicitly rather than through `cubrid service start`.* This one costs nothing
today and will cost something the moment a `single` and an `ha` cluster share a
configuration
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §5).

**A master must also be able to start alone.** Until 11.0 it could not, if the
slave's network had no route to it — `copylogdb` exited on the timeout and took
`heartbeat start` with it (fixed as CBRD-23692). The happy path never
exercises this; `node start master` on a partitioned cluster does.

T6 is the one worth dwelling on: T1–T5 all produce a **failed start**, which is
loud. T6 produces a **corrupt slave**, which is quiet, and N54's harness has the
same race and got away with it. A provisioner that only fixes the loud traps is
not the tool this project is for.

## 3. Reactivation

`heartbeat stop` followed by `heartbeat start` fails with *"CUBRID heartbeat
feature is being deactivated"*; a full `service stop` / `service start` is
required in between. The tool encodes this in `node start` after any path that
stopped the heartbeat, and it is the sequence most likely to change under it in
a future release — which is exactly the brittleness
[`../DESIGN.md`](../DESIGN.md) §6 accepts.

**And `cubrid heartbeat stop` is not trustworthy as a synchronisation point.**
It returns success and then can hang forever: `us_hb_deactivate` polls "is any
`cub_server` running" on a one-second sleep (`util_service.c:3995-4004`), and a
zombie answers yes. In a container this means the node needs a reaping PID 1 —
`--init` — and it means any step that waits on this command must be bounded and
must decide on the observed roles
([`../findings/failback.md`](../findings/failback.md)).

## 4. Container requirements

Not preferences; each one is load-bearing.

| Requirement | Why |
|---|---|
| `--cap-add=NET_ADMIN` | both fault mechanisms are route/qdisc operations ([`04-faults.md`](04-faults.md)) |
| `--init` | without a reaping PID 1, `cubrid heartbeat stop` never returns |
| `ping` in the image | `hb_check_ping` runs `popen("ping -w 1 -c 1 <host> …; echo $?")`. No binary → 127 → read as `HB_PING_FAILURE`, indistinguishable from a partitioned ping host, so **every master demotes itself on any heartbeat loss**. Its *absence* is now also a verb ([`04-faults.md`](04-faults.md) §10) |
| `--cpus` set explicitly | a `host-cpu` load profile is only reproducible against a stated core count ([`06-load.md`](06-load.md) §5) |
| run as the invoking user | files written to the mounted work directory stay editable on the host; the CBRD-26983 assembly lost time to a root-owned `backupdb` output |
| one user-defined network | hostname resolution between peers, and a place to cut |
| `--shm-size` raised | CUBRID's shared memory does not fit the 64 MB default |

The base image needs nothing else. `ubuntu:24.04` with `python3`, `iproute2`,
`iputils-ping` and `procps` is the whole of it. `python3` earns its place twice:
the seeding step already needs it, and the load driver runs inside the node
rather than on the host ([`06-load.md`](06-load.md) §6).

**There is a base image, and there is never an engine image.** The distinction is
the one that decides whether this tool is usable while you are changing the
engine, so it is worth stating plainly:

- The **base image** is those four packages. The tool builds it once from a
  recipe it carries, tagged with the hash of that recipe — so an unchanged recipe
  is never rebuilt, and a changed one is a different image rather than a
  silently stale one.
- The **engine** is bind-mounted read-only from the host tree and is in no image
  at all. Rebuilding the engine rebuilds nothing here; the container sees the new
  binaries because they are the same files. Measured: a stock `ubuntu:24.04`,
  running as the invoking user with a host-built tree mounted at
  `/opt/cubrid-ro`, runs that tree's `cubrid_rel` unmodified.

That is what [`../DESIGN.md`](../DESIGN.md) §2 G2 asks for — the build under test
is an argument — and it is why the four packages are a fixed recipe rather than
a per-cluster one.

**What does not carry over from `cubrid-contrib/sandbox` is its image.** That is
a *build* image — devtoolset-8, cmake, ant, bison — on a base that reached end of
life in 2024. A runtime container needs none of it. The conventions transfer;
the image does not ([`../DESIGN.md`](../DESIGN.md) §5 A2).

## 5. What `create` writes

Per node, two files, from the topology model:

- **`cubrid.conf`** — `ha_mode`, `cubrid_port_id`, and any `--set` key that
  belongs to this file.
- **`cubrid_ha.conf`** — `ha_port_id`, `ha_node_list` (the whole group, and the
  *same* on every node — that is how each node learns who its peer is),
  `ha_db_list`, `ha_ping_hosts` unless a scenario asked for it unset, and
  `ha_apply_max_mem_size`.

An unknown `--set` key is refused rather than written, because the engine
accepts a file with a key it ignores and the divergence is then silent
([`02-topology.md`](02-topology.md) §5).

## 6. The broker, when the topology has one

**Nothing in the assembly starts a broker today**, and that is a gap rather than
a decision. `csql` on the node reaches `cub_server` directly, so the four-step
assembly needs no broker and the harness never started one — but the field's way
of blocking writes is to move the broker's `ACCESS_MODE`, and a cluster with no
broker has no door to close
([`04-faults.md`](04-faults.md) §9,
[`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §4).

`--with-broker` ([`02-topology.md`](02-topology.md) §1) therefore adds:

- **A third configuration file**, `cubrid_broker.conf`, with `ACCESS_MODE` as a
  parameter the tool owns rather than a `--set` key — `quiesce` writes it, so a
  user setting it by hand would be fighting a verb.
- **One more start step, after `serving`.** The broker starts once a master is
  `registered_and_active`, not before: a broker in front of a server that is
  still `registered_and_to_be_active` accepts a connection and fails the first
  write, which is trap T5 wearing a different hat.
- **Nothing on the host.** No published ports — access stays `node exec` and
  `node shell`, which is what keeps port bookkeeping absent
  ([`../DESIGN.md`](../DESIGN.md) §6). The open question is whether
  `cubrid-testkit` can live with that (§9 OQ3 consumers, M2.4); if it cannot,
  the bookkeeping comes back and it comes back here.

The broker is **off by default**, because every phase-1 scenario runs without one
and a component that is started but unused is a component that fails in ways
nobody is looking at.