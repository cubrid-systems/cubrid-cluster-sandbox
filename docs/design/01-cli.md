---
title: cluster-sandbox — Command surface
category: design
project: cluster-sandbox
summary: The command surface and its output contract. Nouns and verbs, role selectors, the structured-output rule, and stable exit codes. This is the interface cubrid-testkit provisions through, so it is a contract from the first release rather than a convenience added later.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# Command surface

The CLI is the primary consumer surface and, from phase 2, the *only* one:
`cubrid-testkit` calls the same commands a developer types, and a web front end
sits over them later rather than beside them
([`../DESIGN.md`](../DESIGN.md) §4). That is why the output contract and the
exit codes are specified here and not left to fall out of the implementation.

Working name for the binary: `csb`.

## 1. Shape

`csb <noun> <verb> [selector] [flags]`

The `<noun> <verb>` shape is inherited from `cubrid-contrib/sandbox`
(`img new|rm|ls`, `pod run|rm|ls`) rather than reinvented — one of the three
conventions [`../DESIGN.md`](../DESIGN.md) §9 OQ1 found worth taking.

Seven nouns, one per thing a user can hold in their head:

| Noun | What it addresses |
|---|---|
| `cluster` | the topology as a whole |
| `node` | one node, by role or by name |
| `fault` | the failure vocabulary |
| `repl` | replication, as an observable |
| `ha` | role transitions |
| `load` | the workload driver ([`06-load.md`](06-load.md)) |
| `record` | what happened to this cluster ([`07-record.md`](07-record.md)) |

The last two are late additions and the reason is worth keeping: phase 0 assumed
a scenario brings its own traffic and leaves its own notes. The field's tracker
showed that assumption is what left its threshold measurement unusable for four
years — a load nobody specified, and a result nobody could attribute.

## 2. Selectors

Every verb that acts on a node takes a **role selector**, resolved at call time:

```
master          the node that is active now
slave           the single standby; error if there is more than one
slave[0]        the nth standby, in ha_node_list order
replica[0]      the nth replica node
n1              a node by name, when the scenario genuinely means that node
all             every node
```

`master` is a *query*, not a label. After a failover it names the other machine,
and a scenario script that ran before the failover runs unchanged after it —
which is the whole point (`../DESIGN.md` §2 G3, and the gap the CBRD-26983
assembly hit when it had to re-read `changemode` to find out who to act on).

Name-based selection stays available because some scenarios mean "the node that
*was* master", and no role name can express that.

## 3. Commands

### `cluster`

```
csb cluster create [--preset ha] [--nodes N] [--name NAME]
                   [--build PATH | --version V]
                   [--set key=value]... [--set-hidden key=value]...
                   [--ping-mode icmp|tcp|none] [--with-broker]
                   [--cpus N] [--from FILE]
csb cluster up                 start everything, in the order that works
csb cluster down               graceful stop, servers flushed
csb cluster destroy            containers, network, volumes
csb cluster status             per-node liveness, HA role, process state
csb cluster describe           the reproducible artifact (§5)
csb cluster quiesce [--mode ro|so] [--mechanism broker|load]
csb cluster resume
csb cluster ls                 clusters on this machine
```

`create` builds and starts. `up` and `down` are for a cluster that already
exists; `down` is graceful — it runs the shutdown flush, which is a distinct
scenario from `node kill` and produces different engine behaviour
([`04-faults.md`](04-faults.md) §2).

`quiesce` blocks writes and `resume` releases them. It sits on `cluster` rather
than under `fault` because it is an operational state the tool enters on purpose,
not an anomaly — and it refuses rather than half-succeeding when the topology has
no broker to close ([`04-faults.md`](04-faults.md) §9).

### `node`

```
csb node start   <selector>
csb node stop    <selector>          graceful: the server flushes
csb node kill    <selector>          crash: it does not
csb node status  <selector>
csb node logs    <selector> [--follow] [--which server|master|copylogdb|applylogdb]
csb node shell   <selector>
csb node exec    <selector> -- <command...>
```

`--which` exists because CUBRID scatters a node's logs across
`<db>_<peer>_copylogdb.err`, `<db>@localhost_applylogdb_<db>_<peer>.err`,
`<host>_master.err`, and `log/server/<db>_<date>.err`. A user should not have to
know that naming to read a failure.

### `fault`

```
csb fault partition <selector> [--from <selector>] [--keep <selector>]
                               [--mechanism blackhole|drop]
csb fault lag       <selector> [--stage copy|apply] [--mechanism suspend|delay]
                               [--delay 200ms]
csb fault splitbrain           [--flavour ping-survives|no-ping-hosts|calc-score-window]
csb fault failcount <selector> [--table t] [--rows N]
csb fault ping-unavailable <selector> [--mechanism binary|icmp]
csb fault clear     [<selector>] [--all]
csb fault ls                   what is currently in force
```

Semantics, the two shapes, and why `--keep` has to exist are
[`04-faults.md`](04-faults.md). **`failcount` is the one verb `clear` cannot
reverse** — its damage is data — so it exits 3 there and points at `ha resync`
([`04-faults.md`](04-faults.md) §8). `fault ls` is not decoration: a condition that
outlives its scenario silently poisons the next one, and that is a named failure
mode ([`../DESIGN.md`](../DESIGN.md) §7).

### `repl`

```
csb repl status [--node <selector>]      both stages, against the master
csb repl watch  [--interval 0.5s] [--for 60s] [--out FILE]
```

`repl status` reports the **copy** stage and the **apply** stage separately and
always against a master-side reference. It does not emit a single number called
"replication delay". [`05-inspect.md`](05-inspect.md) says why that restriction
is not fastidiousness.

`repl watch` samples and *retains*, so that "when did it start falling behind,
and on which stage" is answerable after the episode rather than only during it.

### `ha`

```
csb ha status
csb ha promote  <selector>
csb ha failback --to <selector> [--yes] [--dry-run]
csb ha resync   [<selector>] [--path resume|table|slave] [--dry-run]
```

`ha failback` is deliberately not symmetric with the others. CUBRID's engine
`[Failback]` means "demote myself, another master exists"; after a *clean*
failover nothing returns the cluster to its original master
([`../findings/split-brain.md`](../findings/split-brain.md)). The operational
return trip is a sequence with decision points that are not the tool's to make,
so `ha failback` is **interactive by default**, `--dry-run` prints the plan and
the evidence for each decision, and `--yes` is for scripts that have already
decided. The decision points themselves are still open —
[`../DESIGN.md`](../DESIGN.md) §9 OQ8, and the current guess is
[`../../harness/failback.sh`](../../harness/failback.sh).

`ha resync` is the repair half: three paths — resume, rebuild the table, rebuild
the slave — chosen the way the field chooses between them, and reported rather
than assumed ([`04-faults.md`](04-faults.md) §8). It is on `ha` rather than under
`fault clear` because it changes data, and every verb that changes data should be
where a reader expects to find a decision.

### `load`

```
csb load start [--profile insert|update|mixed|bulkload|host-cpu|host-io]
               [--rate 2000/s] [--concurrency 4] [--for 60s]
               [--table t] [--seed 42] [--require-rate]
csb load stop
csb load status                requested rate, achieved rate, and whether it held
```

The profiles, the two kinds of load and why the rate is part of the contract are
[`06-load.md`](06-load.md). One thing belongs here because it is a CLI promise:
**`load status` always reports achieved next to requested**, and
`--require-rate` turns a miss into exit 1. A driver that quietly under-delivers
turns every figure measured beside it into a figure about the driver.

### `record`

```
csb record show   [--since 5m] [--json]
csb record export --out FILE   the timeline plus the describe that opened it
```

There is no `record start`: every command that changes cluster state appends,
from `cluster create` onward ([`07-record.md`](07-record.md) §2).

## 4. Output contract

**Every command takes `--json`.** Human output is for humans and may change;
`--json` is the contract and changes only with the schema version.

```json
{
  "schema": "csb/v1",
  "command": "cluster status",
  "cluster": "hadb",
  "at": "2026-08-28T07:14:22Z",
  "ok": true,
  "data":  { },
  "notes": [ ]
}
```

Three rules that come from measurement rather than taste:

- **`data` never carries a derived figure whose source cannot support it.** If
  the copy stage cannot be measured because the master is unreachable, the field
  is `null` and `notes` says why. It is never zero.
- **`notes` is machine-readable too** — a list of `{code, severity, message}`,
  not prose. `stale_apply_info`, `no_master_reference`, `fault_active`,
  `ambiguous_apply_info`, `load_rate_not_held`, `quiesce_active`,
  `hidden_parameter_set` and `clock_skew` are the codes so far, and each
  corresponds to something that was observed or measured.
- **Timestamps are the sample's, not the report's.** A `repl status` that
  reports a row `applylogdb` wrote four seconds ago says so, because during an
  apply stall that row stops moving while looking perfectly healthy.

## 5. `describe` — the reproducible artifact

```
csb cluster describe [--json] [--out FILE]
csb cluster create --from FILE
```

`describe` emits everything needed to stand the same cluster up elsewhere:
preset and node count, roles, engine identity (version, or the build's
provenance — path, commit, and build host, since a build tree is not portable
but its *identity* is), every non-default parameter, **and every fault currently
in force**. That last one is the part a naive implementation drops, and dropping
it means the artifact reproduces a healthy cluster when the bug needed a
partitioned one.

It has to be small enough to paste into a JIRA issue — the model is `mlaunch`'s
`list --startup` (`../survey/01-03-mongodb.md` §4 I3).

## 6. Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | the command failed for a reason the tool understands and reported |
| 2 | usage error — unknown flag, bad selector, missing argument |
| 3 | precondition not met: the cluster is not in a state where this makes sense |
| 4 | timeout waiting for the engine to reach a state |
| 5 | the engine reached a state the tool did not expect |

3, 4 and 5 are separate on purpose. A test harness needs to distinguish "I asked
for something impossible" from "it did not finish in time" from "CUBRID did
something we have not modelled" — and the third is a bug report, not a retry.
The harness measured a concrete case: `cubrid heartbeat stop` returns success
and then hangs forever if the node's HA processes cannot be reaped, so a
wrapper that trusts the command's exit status hangs with it
([`../findings/failback.md`](../findings/failback.md)). **Every step that waits
on the engine is bounded and decides on the observed state, not on the exit
code of the command that was supposed to cause it.**

## 7. Global flags

```
--cluster NAME     which cluster (default: the only one, or the one in cwd)
--json             structured output
--timeout DURATION override the default bound on any engine wait
--quiet / -q       suppress progress, keep errors
--verbose / -v     show the engine commands being run
```

`--verbose` is worth its keep for a tool whose main value is knowing an ordering
a user does not: seeing what it ran is how somebody learns the assembly, and how
they debug it when an engine release changes the sequence.
