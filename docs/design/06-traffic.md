---
title: Traffic, contention, and why neither is a verb any more
category: design
project: cluster-sandbox
summary: This tool had a load driver. It was the wrong shape — a benchmark nobody asked for, capped at about twenty statements a second because it spawned a client process per statement, and read as though its numbers were the engine's. Loading data is a program's job and the program is the user's. What the driver did that was not a workload — starving the engine's own cgroup — was never a load at all and is now a fault condition.
created: 2026-08-28
updated: 2026-09-03
lang: en
---

# Traffic, contention, and why neither is a verb any more

## 1. There was a `load` verb, and removing it is the design

It generated `insert`, `update` and `mixed` traffic from inside a node, paced to
a stated rate, and reported whether it held. It was carefully built: the rate
contract was honest, it reported the driver's own cost beside the figure, and it
learned to report `p50/p90/p99`.

None of that made it the right thing to have.

- **It could not be a benchmark and kept being read as one.** It spawned a `csql`
  process per statement, measured at about **twenty statements a second per
  client**; two clients asking for twenty each held eleven, with no errors,
  because the limit was the driver.
- **Loading data is not this project's purpose.** Standing up the place where a
  loader runs is. A user brings their own — a script, a jar, `sysbench`,
  `loaddb` over a dump — and the tool's job ends at giving it somewhere to run
  and somewhere to write.
- **Every feature it grew was a worse copy of a tool that already exists.**

So it is gone, and what replaces it is an **example** rather than a feature:
[`examples/load-client/`](../../examples/load-client/) shows the whole route in
forty lines — `/tools` read-only for your program, `/results` writable and
outliving the cluster, `node exec client` to run it, and the broker answering at
`<node>:33000` from inside with no port published on your machine.

## 2. What was never a workload: `fault contend`

```
csb fault contend <selector> [--kind cpu|io] [--workers N]
```

The old `host-cpu` and `host-io` profiles did not generate load at all. They
existed to **starve the engine of the resource it runs on**, which is the field's
failover-loop condition: a build competing for the same CPU as the database. What
makes that reproduce is precisely that it runs *inside the node*, in the cgroup
the engine was given — a generator on a client would starve the wrong quota and
reproduce nothing.

That is a **condition**, not a workload, and it now behaves like every other one:
held until cleared, listed by `fault ls`, reversed by `fault clear`. Measured on a
two-node cluster: two workers took the master's CPU to 99.8% each and `clear`
returned it to the engine.

Without `--cpus` it takes whatever the machine happens to have, so the verb says
so — "saturated" on a thirty-two core host and on a four-core runner are
different experiments.

## 3. Traffic for a measurement is a step, not a subsystem

Several measurements need the cluster to be busy rather than idle — the
switchover threshold is meaningless on a cluster with nothing to replicate. That
is now a scenario step like any other:

```json
{ "run": ["node", "exec", "client", "--",
          "setsid", "nohup", "sh", "/tools/example.sh",
          "${cluster}", "${cluster}-n1", "100000", "20", ">/dev/null", "2>&1", "&"] }
```

`${cluster}` is bound to the run's own cluster, so a scenario can name the
database and the node its traffic should talk to. The harness scripts in
[`../../harness/`](../../harness/) do the same thing with the same file.

**What was lost with the verb, honestly:** the describe artifact used to carry
the load spec, so "this cluster was under 2000 inserts a second" travelled with
it. It does not any more — what a scenario ran is in the scenario and in the run
record, which is where the rest of what happened already lives.
