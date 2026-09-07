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
| `scenario` | a sequence of the above, written down and run against a build |
| `record` | what happened to this cluster ([`07-record.md`](07-record.md)) |

`record` is a late addition and the reason is worth keeping: phase 0 assumed a
scenario leaves its own notes. The field's tracker showed that assumption is what
left its threshold measurement unusable for four years — a load nobody specified,
and a result nobody could attribute.

There was an eighth, `load`, and it is gone: the driver could not exceed about
twenty statements a second and its numbers kept being read as the engine's, so
the workload went back to being the caller's program and this tool's job is the
place it runs ([`06-traffic.md`](06-traffic.md)). What it did that was not a
workload is `fault contend`.

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
csb cluster destroy [--purge]  containers, network, volumes
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

`destroy` removes the containers, the network and the node volumes, and **keeps
the describe artifact and the run record**: the cluster is gone, but what it did
is evidence and destroying it is a separate decision. `--purge` makes that
decision.

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
know that naming to read a failure. The default is every kind, newest lines
first read; `--which` narrows it to the process the user suspects.

**The logs are on the host, and `node logs` says where.** The engine writes into
a bind-mounted work directory, so this verb tails files on this machine rather
than shelling into a container — which is why it still answers for a node whose
container is stopped, or gone, and why it is the right verb for a node that
crashed. The path was nonetheless hidden: it was `json:"-"` on the field, and the
directory was named in exactly one place, the note that says there are *no* logs.
The address was therefore available when there was nothing to read and absent
when there was something. Every file now carries its host `path` in the envelope,
`data.dir` carries the work directory, and the human output prints the path under
each header — so a reader can grep across the files, attach the directory to a
ticket, or point another tool at it without already knowing the layout.

Two details are from running it. The engine keeps a `<db>_latest.err` symlink
beside each dated file, and following it prints the same log twice — or fails
outright when it is stale and points at a file that has been rotated away, which
is how this verb failed on its first run against a healthy cluster. It skips
symlinks. And **`--follow` is bounded by `--timeout` rather than by Ctrl-C**:
every other verb here is bounded, and a command that can only be stopped by hand
cannot go in a script. `--follow` has no envelope to close, so it refuses
`--json` — as does `node shell`, which replaces this process with `docker exec
-it` because a real TTY has to come from docker's own stdin and not through a
pipe this tool sits in.

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

### `scenario`

```
csb scenario run <file> --build <tree> [--keep]
```

A scenario is a sequence of this tool's own verbs and the state they are expected
to reach, so that *does my change still behave* is one command against a build
rather than knowledge somebody already has. Until now that knowledge lived in
`harness/*.sh` — eight scripts, each encoding a sequence and its expectations,
written for this project's own findings rather than to be pointed at somebody
else's engine.

**A step is an argv this tool already accepts**, run through the same dispatch
with the same envelope and the same exit code, which is what keeps a scenario
from becoming a second way of asking. Most judgements therefore need no new
vocabulary at all: `repl check` already exits 4 when the row does not arrive and
`repl diff` exits 1 when the two sides differ. Three expectations were added on
top, and each because a real scenario needed it — `contains`/`absent` for what a
step printed (a bug reproduction is almost always *these rows*, and the
split-brain flavours are told apart by one line of the engine's log),
`role_change_within` against the record's measured interval, and `await` for a
state to arrive.

**The build is not in the file.** `--build` is an argument to the run, because a
scenario is a statement about behaviour and the engine under test is the variable
(§2 G2). The same file runs against the build you just made and the one you are
comparing it with.

**`matrix` and `repeats` turn one scenario into many runs**, because half of what
people write is a sweep rather than a reproduction: vary one thing, hold the
rest, repeat, read a table. `${name}` is substituted into the cluster's
parameters and every step's arguments, and `measure` names what to collect —
from a closed list, every entry a field this tool already emits, so a table
cannot report something nobody can go and look at.

JSON rather than YAML because this tool has no dependencies and is not acquiring
one for a config format.

**A scenario is refused for what it says, before a cluster is stood up for it.**
Two rules, and both were learned from what the format used to tolerate.

*Unknown keys are refused and named.* This is the rule the tool already applies
to a CTP `ha_repl.conf` it did not write ([`02-topology.md`](02-topology.md) §7)
— the engine accepts a file with a key it ignores, so a typo takes effect nowhere
and is reported by nothing. Its own format had the weaker rule, and the cost is
worse here than there: half the keys in a step are *assertions*, so `contain` for
`contains` produced a step that ran, checked nothing and printed `ok`. A green
result that verified nothing is the one failure a tool like this cannot have.

*Everything checkable is checked before `cluster create`.* Every step's argv goes
through the same `lookup` the command line dispatches through, which is what
makes "a step is an argv this tool already accepts" a property rather than a
sentence; `within` and `role_change_within` have to parse as durations, where an
unparseable `within` used to fall back to 60 s in silence; `master_is` has to be
a role a node can be created with; `measure` has to be on the closed list its own
paragraph claims, where an unknown name used to produce a column of nulls; and
every `${name}` has to be filled by a matrix key or by one of the two bindings
the runner supplies itself (`cluster`, `repeat`) — substitution is a string
replace, so an unfilled reference travels into the argv as the literal text
`${score}` and a sweep runs every point against the same value.

A verb misspelt in step nine used to be a two-minute round trip to learn. It is
now an exit **2** before anything is created.

### `repl`

```
csb repl status [--node <selector>]      both stages, against the master
csb repl check  [<selector>] [--wait 30s]  a write that has to arrive
csb repl watch  [--interval 0.5s] [--for 60s] [--out FILE]
csb repl diff   [--table t]              what the two databases actually hold
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

### there is no `load`

There was one, and removing it is the design rather than an omission
([`06-traffic.md`](06-traffic.md)). A driver built into a provisioning tool is a
benchmark nobody asked for — it could not exceed about twenty statements a second
because it spawned a client process per statement, and its numbers kept being
read as the engine's. **Loading data is a program's job and the program is the
user's**; this tool's job ends at giving it somewhere to run
([`examples/load-client/`](../../examples/load-client/)).

What the driver did that was *not* a workload moved to where it belongs:
`fault contend` starves the engine of the resource it runs on, which is a
condition and now behaves like one.

### `record`

```
csb record show   [--since 5m] [--json]
csb record export --out FILE [--format json|html]
                               the timeline plus the describe that opened it
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
  not prose. Severity is `info`, `warn` or `error`: a consumer has to tell "this
  number is missing and here is why" from "this run is not trustworthy".
  `stale_apply_info`, `no_master_reference`, `fault_active`,
  `ambiguous_apply_info`, `load_rate_not_held`, `quiesce_active`,
  `hidden_parameter_set` and `clock_skew` are the measurement codes, and each
  corresponds to something that was observed. The operational ones are
  `no_such_cluster`, `no_describe`, `stale_state`, `docker_unavailable` and
  `not_implemented`.
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
--cluster NAME     which cluster (default: $CSB_CLUSTER)
--json             structured output
--timeout DURATION override the default bound on any engine wait (default 180s)
--quiet / -q       suppress progress, keep errors
--verbose / -v     show the engine commands being run
--version          the binary's version. Only as the first argument: `cluster
                   create --version 11.5` selects an engine release
```

Two environment variables, because a path and a default cluster are not worth a
flag on every invocation: **`CSB_HOME`** is the state root, holding one directory
per cluster with its `describe` artifact and its record (default
`~/.local/share/csb`), and **`CSB_CLUSTER`** supplies `--cluster` when it is
absent.

**Every failure answers in the envelope, including the ones that happen before a
command starts.** An unknown noun, an unknown verb and a flag that does not parse
all produce `ok: false` with `unknown_noun`, `unknown_verb` or `usage` when
`--json` was asked for. This is not free — those failures happen before the flag
set exists, so the raw arguments are scanned for `--json` — and it is not
optional: a consumer that has to read stderr to tell a typo from a real
precondition has no contract at all. The end-to-end suite caught this one.

A verb the surface defines and has not built exits **1** with a
`not_implemented` note — not 2. The command exists, so "unknown verb" would be a
lie, and a consumer needs to tell a gap from a typo. **Since 2026-09-03 no verb
uses it**: the surface names 35 and all 35 are built, so the helper that returned
that answer is gone rather than kept warm. The rule above is what to do if the
surface ever again promises something ahead of its implementation.

`--verbose` is worth its keep for a tool whose main value is knowing an ordering
a user does not: seeing what it ran is how somebody learns the assembly, and how
they debug it when an engine release changes the sequence.

## 8. Help

`--help` is resolved against the surface rather than by scanning for the token,
because where it appears says what it is asking about:

```
csb --help                 the whole surface: seven nouns and their verbs
csb cluster --help         one noun's verbs
csb cluster create --help  one command, and the flags IT declares
csb node exec master -- csql --help    a question for csql, not for csb
```

The last line is the reason it is resolved rather than scanned. A bare `--` ends
this tool's arguments — the same rule `--json` follows for the same reason — and
a help token after it belongs to the program being run on the node. Scanning
answered it here and never ran the command.

**A command's own flags are part of the surface, and were reachable only from
the README.** Forty-five of them, each declared with a written description beside
it and printed nowhere: the only way to learn that `fault lag` takes `--stage`,
or that `cluster create` takes `--from-ctp`, was to read a document. `--help`
after a noun and a verb now prints that command's flag set, rendered with the
double dash every line of documentation uses rather than the flag package's
single one. A test walks the registry and fails if any declared flag is missing
from its command's help, so a flag cannot go back to being invisible.

The **selector grammar** (§2) and the **exit codes** (§6) are in the global help
for the same reason: `[selector]` in a usage line means nothing to somebody who
has not read §2, and a harness author should not have to.

A noun on its own — `csb cluster` — is an incomplete command and still exits
**2**, but what it prints is that noun's verbs rather than all thirty-five.

**A flag that does not parse names the remedy.** `flag provided but not defined:
-stage` is the flag package's vocabulary: one dash where every line of this
project's documentation uses two, and no way to find out what *is* defined —
which, until the paragraph above, was true. It now reads `unknown flag --stag
(csb fault lag --help lists this command's flags)`.
