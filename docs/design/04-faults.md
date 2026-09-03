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

**Measured 2026-09-03.** `ha promote slave` moved the role in **1.8 s**, and it
emitted `stop_not_returned`: the roles had moved while `cubrid heartbeat stop`
was still running. That is the phase-0 finding turned into behaviour rather than
a warning in a document — the step is bounded, and it decides on the observed
roles rather than on the command's exit status
([`../findings/failback.md`](../findings/failback.md)). The verb refuses two
states by name: a group with two masters is a split brain rather than one waiting
for a promotion, and a group with no master has nothing to take away.

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

**A cut has two shapes and the engine can tell them apart.** A field report
(fixed as CBRD-23692 in 11.0) turns on the errno `connect()` returns: with *no
route* to the peer's network it is `ENETDOWN`, the call times out, and
`copylogdb` **exits**
— which used to fail `heartbeat start` outright; with the *route intact and the
peer absent* it is `EINPROGRESS`, becomes
`ERR_CSS_TCP_CANNOT_CONNECT_TO_MASTER`, and `copylogdb` **retries**. Both start
successfully today, with different messages
(`-353 … Operation now in progress` versus
`-1144 Timed out attempting to connect … (timeout: 5 sec(s))`)
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §5).

A blackhole route produces the first shape — the kernel has nowhere to send the
packet, so the connect fails as unreachable. The second needs the route left
intact and the packets discarded instead (a `DROP` rule on the peer's address),
which is what makes the connect hang and then time out. So `partition` takes a
mechanism the way `lag` does, `--mechanism blackhole|drop`, and the two are
different scenarios rather than cosmetic variants: one used to kill the copier
and the other does not.

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
csb fault splitbrain [--flavour ping-survives|no-ping-hosts|calc-score-window]
```

Three flavours, all producing two masters, and **the interesting one needs a
correct configuration**:

| Flavour | Config | What the master logs | Time |
|---|---|---|---|
| `ping-survives` (default) | `ha_ping_hosts` set, ping host reachable from both | `[Failback] [Cancelled] Ping check succeeded … determining that it is not a network partition` | 9 s |
| `no-ping-hosts` | `ha_ping_hosts` unset — the default a real deployment starts from | `[Failback] [Cancelled] No hosts are registered in ha_ping_hosts …` | 13 s |
| `calc-score-window` | `ha_calc_score_interval_in_msecs` raised | the cancel reason is still whichever of the two above the ping config gives; this flavour is about what happens **after** the heal | the split as above; then **~12 s** of Active-Active at 15000 ms, against ~1 s at the default ([`../findings/active-active-window.md`](../findings/active-active-window.md)) |

The asymmetry is in one function. A **master** cancels its failback when
`ping_try_count == 0` **or** the ping succeeded; a **slave** cancels its failover
only when it tried and failed (`master_heartbeat.c:1042-1054`). A ping host that
survives the partition satisfies both cancel-nots at once — the master reads
"reachable, so not partitioned, stay master" and the slave reads "reachable, so
nothing stops me, promote". **A single ping host is a quorum of one, and it votes
for whoever asks it.**

**The third flavour is a different anomaly wearing the same name, and it now
reproduces — one half of it.** The field's own hidden-parameter test reports
that with
`ha_calc_score_interval_in_msecs` raised, a cluster whose slave was promoted
during a partition runs **Active-Active for the length of that interval once the
network heals**, with data syncing *both ways* — where the two measured flavours
give two masters and one-directional replication. The same ticket reports a
master describing itself as `to-be-master`. Both are recorded as `특이사항` in a
test that could not tell an engine behaviour from a test artefact, which is why
that ticket has been open since 2022 and why this flavour is the one to build
first ([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §2).
Measured 2026-09-03, six runs, three per arm
([`../findings/active-active-window.md`](../findings/active-active-window.md)).
**The window is real and it is the length of the interval**: both nodes accepted
writes for 11, 12 and 12 seconds with the interval at 15000 ms, against 2, 1 and
0 at the default, and the first second at which only one node still accepted a
write was 13, 13, 13 against 4, 2, 1. Every run ended with the original roles
restored, so the parameter does not prevent recovery — it lengthens the interval
in which two nodes accept writes.

**"Syncing both ways" is not what happens.** Rows crossed in one direction only,
and it is the opposite one from the settled roles: the promoted slave's row came
back to the restored master in five of six runs, and the master's row reached the
slave in none. What is left is a permanent divergence that `repl status`,
`repl check` and `ha resync` all call healthy — because replication is healthy;
it simply never carried one row and no view remembers that. **A master calling
itself `to-be-master` was not observed in any of the six**, which is recorded as
unreproduced rather than dismissed.

The consequence for anyone tuning: `ha_calc_score_interval_in_msecs` is the one
parameter shown to move the switchover threshold
([`../findings/switchover-threshold.md`](../findings/switchover-threshold.md)) and
it widens this window by the same amount. Any recommendation to raise it has to
say both.

**The flavour follows from the configuration, so the tool refuses to fake one.**
Asking for `ping-survives` on a cluster with no `ha_ping_hosts` is a request the
mechanism cannot satisfy — there is no ping host to keep — and the verb says so
and names the `--set` that would make it available, rather than producing the
other flavour and calling it the one that was asked for.

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
needs restarting. `clear` is idempotent. **One verb is exempt and says so** —
§8's damage is data, and data does not un-happen.

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
`ha failback` — return-to-original-master, a different operation with decisions
in it, and a name with a collision in it — §7.

## 7. Return-to-original-master is not a fault verb

**And it is not what "failback" means.** To the engine *and to the technical
team*, `failback` is a master demoting itself:

> 큐브리드에서 Fail Over는 슬레이브 노드가 마스터 노드가 되는 것을 의미하며,
> Fail Back은 마스터 노드가 슬레이브 노드가 되는 것을 의미합니다.
> — the team's own HA study notes

Every `[Failback]` line in a CUBRID log is about stepping down, including the
three this project measured (§5, §6). The operation of returning service to the
node that was master before a failover has **no name in the tracker and no
engine path at all** after a clean failover — and it is a sequence with decisions
in it that are not this tool's to make: is the target caught up enough, has
write traffic been quiesced, did the old master's log diverge. This document
uses **return-to-original-master** for it, and the collision is itself a
requirement: the script that goes to the team has to say which operation it is
asking about, or the answers come back about demotion
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §1).

`csb ha failback` keeps its name to match the engine's log vocabulary, and is
interactive by default
([`01-cli.md`](01-cli.md) §3). The mechanism is settled — the measured run
restored the original master in **2 seconds** with no row loss. The policy is
not, and [`../DESIGN.md`](../DESIGN.md) §9 OQ8 is where it stays until the
technical team marks up
[`../../harness/failback.sh`](../../harness/failback.sh).

---

## Three that break the rules above

The seven sections above are phase-0 work: measured mechanisms, and a vocabulary
built out of what running the thing showed. The three below came from the
requirements pass over the field's tracker
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md)),
and they are grouped because each one **violates something §1–§7 established**.
Saying which rule, and why the exception is real, is most of the design.

## 8. `failcount` — the first fault whose reversal is a repair

```
csb fault failcount <selector> [--table t] [--rows N]
```

The mechanism is the field's own recipe, written down in the team's HA study
notes: create a table, insert rows, **then** add the primary key, then delete the
pre-key rows on the master. The pre-key rows never replicated, so the applier
meets a delete for a row it does not have and fails:

```
ERROR CODE = -1034 … failed to apply delete replication log.
class: "public.users", key: "1", server error: -711
                                        (log_applier.c:4796)
```

`fail_counter` moves, and that is the point:
[`05-inspect.md`](05-inspect.md) §2 makes `fail_counter` **the** separator
between *broken* and *behind*, and until now the tool had no way to move it. An
inspector claim that nobody can test is not a claim.

**Which rule it breaks: §6.** `clear` cannot undo this. The rows are absent on
one node and present on the other; the divergence is durable, and removing a
route or resuming a process reverses nothing. So:

- **`fault clear` refuses a `failcount`** and says why — exit 3, a precondition,
  not a silent no-op ([`01-cli.md`](01-cli.md) §6).
- **The reversal is `ha resync`**, and it is a repair rather than a toggle.

```
csb ha resync [<selector>] [--path resume|table|slave] [--dry-run]
```

Three paths, because those are the three the field actually chooses between: do
nothing and let replication continue, rebuild the affected table from the master,
or rebuild the slave outright — with an informal cut-off around **a thousand**
fail rows, above which sites rebuild
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §4).
Without `--path` the tool chooses and **reports how it decided**, which is the
same rule the assembly follows: decide on observed state and say so.

**A zero fail counter is not evidence that the two sides agree, and this command
used to treat it as if it were.** On a cluster whose standby was permanently
missing a row it answered *"resume — fail_counter is 0: replication is behind at
worst, not broken"*. Both halves of that sentence were true and the conclusion
was wrong. A split brain fails nothing — each side wrote its own log and both
succeeded — so the counter never moves and the applier's log names no table,
which is exactly when the divergence is largest. The comparison now comes from
the catalog and runs **before** "nothing is wrong" is said out loud
([`05-inspect.md`](05-inspect.md) §4a), and on that same cluster it now answers:

```
would take path "slave" — fail_counter is 0 and nothing failed to apply, but
1 table(s) hold different row counts (w): that is what a healed split brain
leaves, and replication will not carry it
```

**And it will not carry it later either.** The standby's recorded position is
already past the write it is missing — a canary written afterwards arrives — so
nothing re-fetches it. Only a rebuild resets that bookkeeping, which is why the
answer is `slave`.

**The `slave` path performs the engine's own rebuild.** Not this project's
invention: the steps and their order are `share/scripts/ha/ha_make_slavedb.sh`
from the build in use, with the `ssh` and `scp` removed because both nodes'
database directories are on the same host.

```
cubrid backupdb -z --no-check -C -D <dir> <db>@localhost   on the master, online
rm -rf <db>*                                              on the standby
copy the backup, then <db>_bkvinf                         in that order
cubrid restoreslave -s master -m <master> -B <dir> <db>    on the standby
cubrid heartbeat start                                    and it rejoins
```

**`restoreslave` is a different command from `restoredb`, and that difference is
the whole reason this works.** A plain restore produces a copy of the master; it
does not produce a database that knows where replication should resume.
`restoreslave` takes the source's state and the master's host name and writes the
bookkeeping a slave needs — which is exactly the bookkeeping a healed split brain
leaves wrong.

One ordering detail is not obvious and the script has it for the same reason:
`<db>_bkvinf` is copied **after** the old database is removed, because it lives
among the files that removal deletes.

Measured three times on diverged pairs: **19 s, 21 s and 23 s**, each ending with
`registered_and_standby`, every user table matching, and a canary arriving in
0.08–0.61 s against the 25.97 s the same check took on the diverged cluster. The
verb compares again afterwards and fails with `still_diverged` if the rebuild did
not actually repair anything, because a repair that reports success without
checking is the failure mode this whole section exists to avoid.

**And `resync` never zeroes the counter to make its output tidy.** The engine
deliberately leaves `fail_counter` standing, because a zero would let an operator
conclude master and slave agree when they do not — the field's own argument
against auto-clearing, and the strongest support
[`05-inspect.md`](05-inspect.md) §3 has. `resync` reports what it repaired and
re-reads the counter; if the counter stands, the output says the counter stands.

**What the `table` path does today is compare, not repair, and it says so.** It
reads the affected tables out of the applier's error log and counts rows on both
nodes. That answers the question the field says it answers by hand, one key at a
time: *"fail count는 있지만 오류 데이터를 조회하면 문제가 없어 엔지니어 판단에 의해
fail count만 초기화하는 경우도 많이 발생"* — a counter with no divergence under it
is their common case. Measured here: seven induced failures, `master=0
standby=0`, verdict **the fail count is a scar rather than a divergence**. When
the counts *do* differ the command stops and names what the field does instead —
a table rebuild from the master, or the whole slave — because a repair that does
nothing under a name that says otherwise is worse than an honest refusal.

## 9. `quiesce` — not a fault, and it still needs every property of a condition

```
csb cluster quiesce [--mode ro|so] [--mechanism broker|load]
csb cluster resume
```

It is entered, it holds, something must clear it, it must be visible in
`status`, and it must travel in `describe` — §1's entire definition of a
condition. It is nonetheless **not an anomaly**, so it is not a fault verb, and
it lives on `cluster`.

**Which rule it breaks: §1's split is about failure shapes, and this is an
operational state.** The distinction earns its keep: a scenario asserting "no
writes reached the master during the switch" is asserting something the tool
did on purpose, not something that went wrong.

It exists because the field's way of blocking writes before it touches
replicated data is to move the broker's `ACCESS_MODE` to RO/SO, and because
that is the missing step 0 of a return-to-original-master (§7) — one of the four
things the tracker could not answer, and the one it turned out to answer
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §4).

Two mechanisms, because a sandbox has two doors:

| Mechanism | What it closes | Requires |
|---|---|---|
| `broker` | the door applications come through — the field's mechanism | a broker in the topology (`--with-broker`, [`02-topology.md`](02-topology.md) §1) |
| `load` | this tool's own driver, the only writer in a default sandbox | a load running ([`06-load.md`](06-load.md)) |

**And the honest limit belongs in the output, not in a footnote.** Neither
mechanism closes a door the tool does not own: a user's own `csql` session on the
node writes regardless. `cluster quiesce` with no broker **refuses** rather than
reporting success it cannot deliver (exit 3), and `cluster status` shows which
mechanism is in force so a reader knows which writers were actually stopped.

Measured through the door itself, with a client that goes through the broker
rather than around it. Quiesced at `RO`, a write returns `FAIL(-581)` and a read
returns `OK`; after `cluster resume`, the same write returns `OK`. The mechanism
is `broker_changer`, which applies the mode to a running broker rather than
needing a restart.

## 10. `ping-unavailable` — a condition on the mechanism the engine decides with

```
csb fault ping-unavailable <selector> [--mechanism binary|icmp]
```

| Mechanism | What it does | What it reproduces |
|---|---|---|
| `binary` | the `ping` executable is absent or not permitted | the field's deployment blocker: where security policy denies the DB account permission to run `ping`, setting `ha_ping_hosts` makes the engine **fail to start** |
| `icmp` | the binary stays, ICMP is dropped | a network condition in which the check runs and fails |

**Which rule it breaks: §3's model of a cut.** `partition` changes what a node
can *reach*; this changes whether the engine can *ask at all*. The split-brain
finding is entirely about the answer to that question — a master cancels its
failback when its ping **succeeds**, a slave cancels its failover only when its
own ping **fails** — so "the ping host is unreachable" and "the ping check is
broken" are different scenarios with the same-looking log.

This project met the `binary` case as an accident: an image without
`iputils-ping` returns 127, the caller reads `HB_PING_FAILURE`, and **every
master demotes itself on any heartbeat loss**
([`03-assembly.md`](03-assembly.md) §4). Phase 0 fixed it by putting `ping` in
the image. As a verb it stops being a trap and becomes a scenario — and with
`--ping-mode tcp` ([`02-topology.md`](02-topology.md) §5) the pair covers both
ping mechanisms the engine has.

Reversal is ordinary: restore the binary, drop the rule. `clear` handles it —
and it restores the binary's **original** mode, read before the change, because
ping is setuid and guessing `0755` on the way back leaves a third state that runs
but cannot open a raw socket.

**Measured 2026-09-03.** `--mechanism binary` on a running master produced
`rc=126` from `ping`, and `clear` put it back to `rc=0`. `--mechanism icmp` on a
standby took ping from `rc=0` to `rc=1` and back. The verb says what it cannot do
as clearly as what it does: on a cluster with no `ha_ping_hosts` nothing consults
ping, and on one using `ha_tcp_ping_hosts` the check is a TCP connect, so
`--mechanism icmp` there changes nothing the engine asks. Both are notes rather
than refusals, because the condition really is in force — it just is not being
consulted.

**Writing it found that the ICMP half could not run at all**, and neither could
§3's `drop`: `iptables` was missing from the base image, so the packet-level half
of both mechanisms had never executed since it was written. The recipe now
carries it, and `partition --mechanism drop` — run for the first time on
2026-09-03 — produced two masters in about twelve seconds with the route still in
the table, which is the distinction §3 exists to make. A mechanism nobody has run
is a mechanism nobody has.
