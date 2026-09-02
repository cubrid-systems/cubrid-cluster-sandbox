---
title: cluster-sandbox — The run record
category: design
project: cluster-sandbox
summary: The evidence artifact. describe reproduces the cluster; the record says what happened to it — the inputs that were in force, the timeline, and for a role change both intervals: the one measured and the one the settings predict. It exists because a threshold-caused switchover may leave nothing in the engine log, and because the field's own measurement stalled unable to separate engine behaviour from test artefact.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# The run record

[`02-topology.md`](02-topology.md) §4's `describe` answers *what cluster was
this*. The record answers *what happened to it*, and the two together are what a
second person needs to believe a result.

It is not a monitoring system. It is scenario-scoped, bounded, local, and
discarded with the cluster unless exported — the same rule that keeps
`repl watch` from becoming a second operational collector
([`05-inspect.md`](05-inspect.md) §5). The organization's operational metrics
contract is a different thing with a different owner
([`../DESIGN.md`](../DESIGN.md) §3, §9 OQ2).

## 1. Why it is a component and not a log file

Two measured facts make an unstructured log insufficient.

**A threshold-caused switchover may leave nothing in the engine log.** The
field's own request says so — *"어떤 상황에 절체가 될수있는지 로그에 남지 않는
부분들이 많습니다"* — and asks for engine-side logging that does not exist yet
([`../requirements/01-failback-field-evidence.md`](../requirements/01-failback-field-evidence.md) §3).
Until it does, **the inputs are the evidence**: the settings in force, the load,
the timings. A reproduction that records only the outcome is unattributable.

**The field's measurement stalled on exactly this.** The hidden-parameter test
ended on three unresolved candidate explanations of its own result — the test
case, the parameter value, or network variance that afternoon — because nothing
recorded enough to separate them
([`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §2).
The record is the answer to that, and it is why
[`../ROADMAP.md`](../ROADMAP.md) M2.5's acceptance now names both intervals.

## 2. Shape

```
csb record show    [--since 5m] [--json]
csb record export  --out FILE          the record plus the describe that opened it
```

There is no `record start`. **Every command that changes cluster state appends
to the record**, from `cluster create` onward, because a record a user has to
remember to switch on is a record that is missing from the run that mattered.
The cost is an append per state change, which is nothing next to the assembly.

## 3. Contents

```yaml
schema: csb/v1
cluster: hadb
opened: 2026-08-28T07:02:11Z
describe: { ... }            # the artifact as it stood when the record opened
timeline:
  - t: 2026-08-28T07:12:00Z
    actor: tool              # tool | engine | load
    event: fault.partition
    detail: { target: master, keep: ping-host, mechanism: blackhole }
  - t: 2026-08-28T07:12:09Z
    actor: engine
    event: role.change
    detail:
      node: hadb-n2
      from: standby
      to: to_be_active
      source: "hadb-n2_master.err:1841"
      line: "[Failover] [Success] ..."
role_changes:
  - node: hadb-n2
    measured: 9.1s           # from the fault to the engine's own line
    predicted: 2.5s          # arithmetic from the settings in force
    decided_by:
      ha_heartbeat_interval_in_msecs: 500
      ha_max_heartbeat_gap: 5
      ha_calc_score_interval_in_msecs: 3000
      ha_ping_hosts: ping-host
load:
  requested: 2000/s
  achieved: 1180/s
  held: false
validity:
  valid: false
  reasons: [load_rate_not_held]
```

Four things in that schema are load-bearing.

**`actor` separates the tool from the engine.** "The tool cut the route at
07:12:00" and "the engine logged a failover at 07:12:09" are different classes
of fact, and the interval between them is the measurement. A timeline that
blends them loses the only number the run was for.

**`source` is a file and a line, and `line` is the engine's own text.** The
split-brain finding's whole content is that two flavours are indistinguishable
by outcome and distinguishable only by the cancel reason
([`04-faults.md`](04-faults.md) §5), so the record quotes the line rather than
summarising it. An assertion belongs on text the engine wrote.

Measured on the first run that produced one: a promotion **5.9 s** after
`node kill` against a predicted **2.5 s**, with `ha_heartbeat_interval_in_msecs`,
`ha_max_heartbeat_gap`, `ha_calc_score_interval_in_msecs` and `ha_ping_hosts`
recorded beside it. That is one document containing both numbers and the inputs
that decide them, produced without anyone remembering to write anything down —
which is the thing the field's stalled measurement never had.

**`predicted` is arithmetic, and the record says so.** 5 × 500 ms is what the
documented behaviour implies; the lab restated that documented behaviour to a
customer in 2023. It is **not** a claim about what the engine does — the field
measured 8–11 s against it, three runs. The record's job is to put the two
numbers side by side and let the disagreement be visible, which is the thing
nobody has yet done in one place.

**`validity` is explicit and never inferred.** A run is invalid when a load did
not hold its rate ([`06-load.md`](06-load.md) §3), when a fault was already in
force at the start, or when the node clocks disagree by more than the interval
being measured. Reasons are codes, the same shape as `notes`
([`01-cli.md`](01-cli.md) §4), and each corresponds to something that has
actually gone wrong in a run.

## 4. Clock skew is a first-class invalidity

The measurements this record exists for are **single-digit seconds** — a 9 s
split brain, a 13 s one, a 2 s return to the original master, a 10 ms reaction to
a dead process. Two containers on one host share a clock, so today the risk is
small; the moment a topology spans hosts it is not, and a 3-second skew silently
becomes a 3-second finding. The record compares the nodes' clocks when it opens
and at every role change, and refuses to publish an interval it cannot defend
rather than publishing one that is wrong by more than the effect.

## 5. What the record does not do

- It does not sample. Sampling is `repl watch`, which produces a series with its
  own retention rule; the record references a series rather than embedding one.
- It does not interpret. There is no `conclusion` field. A record that says
  "threshold X caused this" is asserting the thing the engine cannot log, which
  is the gap that created this document in the first place.
- It does not survive the cluster. `record export` is how a result leaves, and
  it carries the `describe` with it, because a timeline without the topology it
  ran against is not evidence.
