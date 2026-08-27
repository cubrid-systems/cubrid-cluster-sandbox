# G8 — the failback script, and what running it exposed

`failback.sh` returns a CUBRID HA cluster to its original master. It ran
end-to-end on 2026-08-27 (`harness/failback-demo.sh`; console output at `harness/results/failback-console.txt`) and
completed: `rc=0`, the original master active again, the other node its slave,
and both nodes still holding the three rows written before the failover.

It is not a tool. It is a question addressed to the technical team, and the run
sharpened two of the five things it asks.

## What it validated

| Step | Result |
|---|---|
| 1 confirm the current master | correct, and it refuses to proceed if both nodes are active (that is split brain, not a completed failover) |
| 4 `cubrid heartbeat stop` on the current master | works — but see the `--init` requirement below |
| 5 target promoted | **2 s** |
| 6 rejoin the old master (`service stop` / `service start` / `heartbeat start`) | works; the plain `heartbeat start` trap from CBRD-26983 is real and the full service cycle is what gets past it |
| 7 verify | original roles restored, no row loss |

The whole return trip is therefore mechanically possible with commands that
already exist. What does not exist is the *judgement* around it, which is why
five of the seven steps stop and ask.

## What running it exposed

**1. The one check the operator needs is empty at exactly the moment they need
it.** STEP 2 asks "has the target applied everything it was sent" and reads
`db_ha_apply_info` on the target. Both runs printed:

```
db_ha_apply_info on n65-n1 (eof / final / lag / fail): <none>
```

The node had just been demoted, and its `applylogdb` had not yet written the
row. Queried directly a few minutes later the row existed and read
`173 / 173 / 0 / 0` — caught up. STEP 7 showed the same blank on the other node
after the rejoin. So the view is not merely incomplete as a lag source
(`replication-lag.md`); it is **absent across a role change**, which is the
only time a failback decision is ever made. A monitor that treats "no row" as
"no lag" will approve a failback onto a node it knows nothing about.

**2. `cubrid heartbeat stop` hangs forever without a reaping PID 1.** In the
first run the command sat in `hrtimer_nanosleep` for five minutes while
`cub_server`, `cub_pl` and both `cub_admin` processes were `<defunct>` and
reparented to `tail -F /dev/null`. The deactivation had *already succeeded* —
the master log carried *Command execution: deactivate. Success.* and the peer
had been promoted — but `us_hb_deactivate` polls
`COMMDB_HA_DEACT_CONFIRM_NO_SERVER` on a one-second sleep
(`util_service.c:3995-4004`) and a zombie `cub_server` answers "still running"
forever. Adding `--init` to the container removed it: the second run showed no
zombies and the command returned normally.

Two consequences, and both belong to the provisioner rather than to the
operator: **the container needs an init process**, and **anything driving this
step must be bounded and judge by the observed roles, not by the command's exit
status**. `failback.sh` now bounds it at 180 s and says so.

## The five questions, unchanged

The script ends by asking the technical team to write in:

1. the threshold for "caught up" at STEP 2, and what to do when it is not met;
2. how write traffic is actually quiesced at STEP 3, or whether it simply is not;
3. whether `heartbeat stop` is the mechanism they use, or something else;
4. how they detect that the old master's log diverged before rejoining it;
5. every step this project did not write down.

Question 1 is now sharper than when it was written: the answer has to survive
the view being empty.

## Reproducing

```bash
cd harness && bash failback-demo.sh      # builds a pair, forces a clean failover, heals, runs failback.sh --auto
```
