# Getting your program into the cluster

This project stands a CUBRID HA topology up and breaks it in named ways. **It
does not load your data.** That is a program's job, and the program is yours —
what you need from us is how it gets in and where its output comes out.

`example.sh` is here so that trying the project out does not require writing one
first. Read it, run it, then throw it away and put your own thing in its place:
a python script, a jar, `sysbench`, `cubrid loaddb` over a dump you dropped in
the same directory. The route is identical and needs nothing from us.

## The whole thing

```bash
csb cluster create --name demo --clients 1 --with-broker \
    --tools ./examples/load-client --build ~/cubrid/install.out

csb node exec client -- sh /tools/example.sh demo demo-n1 500 20

csb repl status                  # the standby is now behind, and by how much
csb repl check                   # a write, and whether it arrives
csb node kill master             # break it, and watch the record
csb record show
```

## Three facts, and there is nothing else to learn

| | |
|---|---|
| `/tools` | the directory you passed to `--tools`, **read-only**. It stays on your disk and we never write to it, so a run cannot damage your scripts |
| `/results` | **writable**, and it survives `cluster destroy` — removed only by `--purge`, the same rule the run record follows |
| the broker | answers at `<node>:33000` from a client, with **no port published on your machine**, because the client is inside the cluster's network. That is the path JDBC and CCI take |

`csql`, `loaddb` and `broker_tester` are already on the client's `PATH`: the
engine tree you built is mounted read-only there too, so nothing needs
installing.

## Why there is no `csb load`

There was one, and it was the wrong shape. A driver built into a provisioning
tool is a benchmark nobody asked for — measured at about twenty statements a
second because it spawned a client process per statement — and it invited people
to read its numbers as the engine's. Loading data is not this project's purpose.
Standing up the place where your loader runs is.

What the built-in driver did that was *not* a workload has moved to where it
belongs: `csb fault contend --kind cpu|io` starves the engine of the resource it
is running on, which is a condition the field's failover-loop report describes
and not a load at all.
