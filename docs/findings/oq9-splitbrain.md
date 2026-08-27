# OQ9 — does split brain need a deliberately broken configuration?

**Answer: no.** A correctly configured `ha_ping_hosts` produces two masters in
**9 seconds** when the ping host survives the partition. Measured 2026-08-27 on
a two-node containerised HA pair, CUBRID 11.5.0 (engine
`for-plan/importdb/cubrid/install.out`, heartbeat sources identical to
`/data/cub_sys/cubrid` at `5fd7b76c1`).

The foundation carried this as a code reading marked *read-not-run*
(`00-foundation.md` §9 OQ9). It now has three arms behind it.

## The arms

| Arm | `ha_ping_hosts` | What was cut | Outcome | Time to outcome |
|---|---|---|---|---|
| **A** | `n65-ping`, reachable from both | the `n1`↔`n2` link only | **two masters** | 9 s |
| **B** | unset (the default) | the `n1`↔`n2` link only | **two masters** | 13 s |
| **C** | `n65-ping`, cut from the master | the link **and** the master's path to the ping host | clean failover, master demoted itself | 9 s |

The cut is a blackhole route on each node (`ip route add blackhole <peer>`),
not `docker network disconnect`. A and C differ **only** in whether the ping
host stays reachable, and a whole-interface disconnect cannot express that
difference — which is the reason this experiment needed a route-level fault and
not the partition verb the CBRD-26983 session improvised.

## What each arm logged

**Arm A** — the master sees the partition, pings successfully, and concludes it
is not one:

```
[Failback] [Diagnosis] The master node has failed to receive heartbeat messages
                       from all other slave nodes, resulting in a network partition.
[Failback] [Cancelled] Ping check succeeded for the hosts registered in ha_ping_hosts,
                       determining that it is not a network partition.
```

repeating for as long as the partition holds. The slave's ping also succeeds, so
nothing cancels *its* failover and it promotes. Both nodes then log the same
pair, because both are now masters that cannot see a peer.

**Arm B** — the same shape with the other cancel reason:

```
[Failback] [Cancelled] No hosts are registered in ha_ping_hosts, or all registered
                       hosts are invalid, making it impossible to determine the
                       network partition.
```

**Arm C** — the control, and it demotes exactly as designed:

```
[Failback] [Success]   Current node has been successfully demoted to slave.
[Failover] [Cancelled] Ping check has been failed to all hosts registered in
                       ha_ping_hosts, indicating a network partition.
```

the second line repeating afterwards: having demoted itself, the old master keeps
evaluating a failover back and correctly refuses, because its own ping still fails.

## Why A happens — the asymmetry is in one function

`hb_cluster_job_check_ping` (`master_heartbeat.c:1042-1054`) decides both
directions with opposite tests:

- a **master** cancels its failback when `ping_try_count == 0` **or**
  `ping_success == true`;
- a **slave** cancels its failover only when `ping_try_count > 0 && ping_success == false`.

A ping host that survives the partition therefore satisfies *both* cancel-nots at
once: the master reads "reachable, so not partitioned, stay master" and the slave
reads "reachable, so nothing stops me, promote". A single ping host is a quorum
of one, and it votes for whoever asks.

## Recovery is automatic, and it restores the original roles

On heal, arm A produced, on the node that had promoted:

```
[Failback] [Diagnosis] Multiple master nodes (n65-n2, n65-n1) are detected.
[Failback] [Success]   Current node has been successfully demoted to slave.
```

within 45 s, leaving `n1` active and `n2` standby — the original assignment,
because priority (`ha_node_list` order) decides who steps down.

**Arm C did not recover.** Forty-five seconds after the network healed the roles
were still swapped (`n1` standby, `n2` active), and they stay that way: there is
only one master, so nothing triggers. This is the finding that matters most for
G8 — CUBRID's `[Failback]` means *"demote myself, another master exists"*, and
there is no engine path that returns a cluster to its original master after a
clean failover. That trip is the operator's, and it is what `failback.sh` asks
the technical team about.

## Consequences for the provisioner

1. **Split brain is a first-class scenario, not a misconfiguration.** `01-05` §2
   G9 assumed one of the two routes needed a deliberately wrong config. Arm A
   shows the *documented* configuration reaches it. The sandbox does not need a
   deviant preset to reproduce split brain; it needs a **route-level partition**
   plus a surviving third host.
2. **Two flavours, and they are distinguishable by log line.** A and B both give
   two masters but for different reasons, and a test that asserts on the outcome
   alone cannot tell them apart. The assertion belongs on the `[Failback]
   [Cancelled]` reason.
3. **`ping` must be in the image.** `hb_check_ping` does not open a socket — it
   runs `popen("ping -w 1 -c 1 <host> >/dev/null 2>&1; echo $?")`. With no `ping`
   binary the shell returns 127, which the caller reads as `HB_PING_FAILURE`,
   indistinguishable from a partitioned ping host. A container image without
   `iputils-ping` therefore makes every master demote itself on any heartbeat
   loss. This is a provisioner responsibility, not a user's.
4. **A fifth ordering trap, and the only one that corrupts.** `databases.txt`
   gains its entry **before** `createdb` finishes. Seeding the slave on that
   signal copies a database with a live transaction in it; the slave's recovery
   then dies in its UNDO phase with `fetching deallocated pageid 705 of volume
   "/db/hadb"` → `LOG FATAL ERROR: log_recovery:locator_initialize`
   (`log_recovery.c:953`). N54's WU-51b harness has the same race and got away
   with it. The four traps in that harness's README all produce a *failed start*;
   this one produces a *corrupt slave*, which is worse, and it is exactly the
   class of thing a provisioner exists to own. Fixed here with a sentinel file
   written after `createdb` returns.

## Reproducing

```bash
bash oq9-splitbrain.sh A     # correct config, ping host survives  -> split brain
bash oq9-splitbrain.sh B     # default config                      -> split brain
bash oq9-splitbrain.sh C     # correct config, ping host also cut  -> clean failover
```

Artifacts land in `out/oq9-<arm>/` (`roles.log`, `masterlog-*.txt`, raw console
output in `out-oq9-<arm>.txt`).
