---
title: cluster-sandbox — Fault vocabulary
category: design
project: cluster-sandbox
summary: The verb set, split into events and conditions. Fixes the mechanisms — a route-level cut for partition, stage suspension for lag — the clear semantics, and why split brain needs no misconfiguration. Every mechanism here was measured rather than chosen.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# Fault vocabulary

Layer 4 of [`../DESIGN.md`](../DESIGN.md) §4. This is the part of the design
with no model to copy: **not one of the four surveyed tools ships a network
partition**, because all four chose process isolation
(`../survey/01-00-overview.md` §5.1 DI1). It is also the part where running it
changed the design most.

## 1. Two shapes

| | Events | Conditions |
|---|---|---|
| Examples | `stop`, `kill`, `promote` | `partition`, `lag`, `splitbrain` |
| Duration | happen, and are over | entered, held, and cleared |
| Reversal | none needed | required, and owned by the tool |
| In `describe` | no | **yes** — as active faults |

Conflating them is the design error this section exists to prevent. A condition
that outlives its scenario silently poisons the next one, so every condition
records how to reverse itself, `csb fault ls` shows what is in force, and
`csb cluster describe` carries it ([`02-topology.md`](02-topology.md) §4).

## 2. Events

```
csb node stop <selector>     graceful
csb node kill <selector>     crash
csb ha promote <selector>
```

**The stop/kill split is not cosmetic in CUBRID.** A graceful stop runs the
shutdown flush — `serial_flush_cache_pool` among it — and a crash does not.
That is exactly the pair the CBRD-26983 verification had to build by hand, and
reproducing its id sequence `1,2,21,22,41,42,61` is the acceptance line for the
verb set ([`../DESIGN.md`](../DESIGN.md) §2 G3).

**`promote` is not the inverse of anything.** A demotion cannot be driven from
outside: `changemode` refuses an active→standby transition the heartbeat did not
drive (`server_support.c:1558`). What looks like a demotion in the logs is the
heartbeat replacing the server process — the CBRD-26983 session watched
`[Failback] [Success] … demoted to slave` followed 10 ms later by
`Process failure detected (pid:102, args:cub_server aitest)`. Any verb that
appears to demote is really "make the heartbeat decide to", and the tool says so.

## 3. `partition` — and why it must cut routes, not interfaces

```
csb fault partition <selector> [--from <selector>] [--keep <selector>]
```

Default: cut the selected node from every other node in the cluster. `--from`
narrows it to a pair. `--keep` preserves reachability to something — in practice
the ping host.

**The mechanism is a per-node blackhole route** (`ip route add blackhole <peer>`
on each side), not `docker network disconnect`. This is a requirement, not an
implementation detail: disconnecting an interface cuts *everything*, and the
entire content of the split-brain finding is the difference between a partition
where the ping host survives and one where it does not
([`../findings/split-brain.md`](../findings/split-brain.md)). An
interface-level cut cannot express `--keep`.

It is also why the container needs `NET_ADMIN`.

## 4. `lag` — stage-targeted, because the pipeline has two stages

```
csb fault lag <selector> [--stage copy|apply] [--mechanism suspend|delay] [--delay 200ms]
```

CUBRID's replication pipeline is **two** heartbeat-managed processes,
`HB_PTYPE_COPYLOGDB` and `HB_PTYPE_APPLYLOGDB` (`heartbeat.h:62-70`), and the
engine reports their delays separately — "Delay in Copying Active Log" versus
"Delay in Applying Copied Log". A `lag` verb that cannot say which stage it is
slowing is not much of a verb.

| Mechanism | Stage-selective | Reverses | Heartbeat interferes | Use for |
|---|---|---|---|---|
| `suspend` (default) | **yes** | instantly | **no** (measured) | control |
| `delay` (`netem`) | no | on removal, but the backlog drains slowly | no | realism |

**The heartbeat does not notice a suspended process.** Both processes were held
for 30 s each and `cubrid heartbeat status` still listed the same pids in
`state registered`, with nothing in the master log. It monitors process
*existence*, not progress — the 10 ms reaction the CBRD-26983 session saw was to
a *dead* process. That is what makes suspension safe to use as a control, and it
is also the more uncomfortable fact underneath: **the heartbeat will not tell
anyone that replication has stopped while the process is alive.**

`--delay` applies `netem` to the node's interface. It grew the apply lag by
about 15,000 log pages in 30 s in the measured run and had not drained 30 s
after removal — realistic, and stage-blind.

## 5. `splitbrain` — no misconfiguration required

```
csb fault splitbrain [--flavour ping-survives|no-ping-hosts]
```

Two flavours, both producing two masters, and **the interesting one needs a
correct configuration**:

| Flavour | Config | What the master logs | Time |
|---|---|---|---|
| `ping-survives` (default) | `ha_ping_hosts` set, ping host reachable from both | `[Failback] [Cancelled] Ping check succeeded … determining that it is not a network partition` | 9 s |
| `no-ping-hosts` | `ha_ping_hosts` unset — the default a real deployment starts from | `[Failback] [Cancelled] No hosts are registered in ha_ping_hosts …` | 13 s |

The asymmetry is in one function. A **master** cancels its failback when
`ping_try_count == 0` **or** the ping succeeded; a **slave** cancels its failover
only when it tried and failed (`master_heartbeat.c:1042-1054`). A ping host that
survives the partition satisfies both cancel-nots at once — the master reads
"reachable, so not partitioned, stay master" and the slave reads "reachable, so
nothing stops me, promote". **A single ping host is a quorum of one, and it votes
for whoever asks it.**

Two consequences for this layer:

- The verb is composed, not primitive: `ping-survives` is
  `fault partition master --keep <ping-host>`. It exists as its own verb because
  the *intent* is what a scenario means, and because getting the `--keep` right
  is precisely the knowledge the tool is supposed to hold.
- **Assertions belong on the cancel reason, not on the outcome.** Both flavours
  give two masters; only the log line distinguishes them. A test that asserts
  "two masters" passes for the wrong reason half the time.

## 6. `clear`

```
csb fault clear [<selector>] [--all]
```

Each condition knows its reversal: remove the blackhole routes, `SIGCONT` the
suspended process, delete the qdisc, restore `ha_ping_hosts` and restart what
needs restarting. `clear` is idempotent.

**Clearing is not the same as recovering, and the tool must not pretend it is.**
Two measured cases:

- After clearing a `splitbrain`, the engine resolves it *on its own*: seeing
  `num_master > 1` it logs
  `[Failback] [Diagnosis] Multiple master nodes (a, b) are detected` and demotes
  one, inside 45 s, restoring the original roles because priority decides who
  steps down. `clear` waits for that and reports it.
- After clearing a `partition` that caused a **clean failover**, nothing happens.
  The roles stay swapped, indefinitely. 45 s after the network healed the
  measured cluster was still inverted, and it stays that way — there is only one
  master, so nothing triggers.

So `fault clear` restores the *network*, and `csb cluster status` afterwards may
legitimately show a topology that is healthy and inverted. Returning it is
`ha failback`, which is a different operation with decisions in it — §7.

## 7. Failback is not a fault verb

CUBRID's engine `[Failback]` means "demote myself, another master exists". The
operational failback — return the cluster to its original master — has **no
engine path at all** after a clean failover, and it is a sequence with decisions
in it that are not this tool's to make: is the target caught up enough, has
write traffic been quiesced, did the old master's log diverge.

`csb ha failback` is therefore interactive by default
([`01-cli.md`](01-cli.md) §3). The mechanism is settled — the measured run
restored the original master in **2 seconds** with no row loss. The policy is
not, and [`../DESIGN.md`](../DESIGN.md) §9 OQ8 is where it stays until the
technical team marks up
[`../../harness/failback.sh`](../../harness/failback.sh).
