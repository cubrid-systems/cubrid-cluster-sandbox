#!/bin/bash
# N65 G8 — semi-automatic failback: return a CUBRID HA cluster to its ORIGINAL master.
#
# ============================================================================
#  THIS SCRIPT IS A QUESTION, NOT AN ANSWER.
#
#  It encodes ONE PROJECT'S GUESS at the sequence a technical team performs by
#  hand after a failover. Every DECIDE block below is a place where we do not
#  know what you actually do. Please mark them up: change the default, add a
#  step we missed, delete a step you would never take, and say why. The marks
#  are the requirement set -- the script is only the paper it is written on.
#  -- CUBRID Systems Research, N65 cluster-sandbox, 2026-08-27
# ============================================================================
#
# Why this script exists at all. CUBRID's engine already has something it calls
# failback, and it is NOT this. Measured 2026-08-27 (OQ9 arms A and C):
#
#   * The engine's [Failback] means "demote myself, because I should not be
#     master" -- it fires when a node is partitioned from its ping hosts, or when
#     the heartbeat sees two masters after a partition heals. It is automatic and
#     needs no operator.
#   * After a CLEAN failover, nothing brings the original master back. The
#     cluster runs on the node that took over, indefinitely, with the original
#     master as its slave. Arm C: the roles were still swapped 45 s after the
#     network healed, and they stay that way.
#   * An operator cannot simply demote the current master: cubrid changemode
#     refuses an active->standby transition the heartbeat did not drive
#     (server_support.c:1558).
#
# So the operational return trip is manual, and it is the manual part that this
# project knows nothing about. Hence the DECIDE blocks.
#
# Usage:
#   bash failback.sh --db hadb --current <node> --target <node> [--auto] [--exec docker]
#     --current   the node that is master NOW (took over during the failover)
#     --target    the node that SHOULD be master (the original)
#     --auto      take every default without asking (for a sandbox run)
#     --exec      how to reach a node: "docker" (default) or "ssh"
set -uo pipefail

DB=hadb; CUR=""; TGT=""; AUTO=no; EXECMODE=docker
while [ $# -gt 0 ]; do case "$1" in
  --db) DB=$2; shift 2;; --current) CUR=$2; shift 2;; --target) TGT=$2; shift 2;;
  --auto) AUTO=yes; shift;; --exec) EXECMODE=$2; shift 2;;
  *) echo "unknown option $1" >&2; exit 2;; esac; done
[ -n "$CUR" ] && [ -n "$TGT" ] || { echo "!! --current and --target are required" >&2; exit 2; }

# How to run a command on a node. Swap this for your own transport -- that is the
# point of it being one function.
on () { local n=$1; shift
  case "$EXECMODE" in
    docker) docker exec -e CUBRID="/work/$n/cubrid" -e CUBRID_DATABASES=/db \
              -e CUBRID_CONF_FILE="/work/$n/cubrid/conf/ha.conf" \
              -e CUBRID_HA_CONF_FILE="/work/$n/cubrid/conf/cubrid_ha.conf" \
              -e PATH="/work/$n/cubrid/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
              -e LD_LIBRARY_PATH="/work/$n/cubrid/lib:/work/$n/cubrid/cci/lib" \
              "$n" bash -lc "$*" ;;
    ssh)    ssh "$n" "$*" ;;
  esac; }

STEPNO=0
STEP   () { STEPNO=$((STEPNO+1)); echo; echo "── STEP $STEPNO ── $*"; }
NOTE   () { echo "     $*"; }
# A decision point. $1 = the question, $2 = the default, $3.. = why that default.
DECIDE () { local q=$1 def=$2; shift 2
  echo; echo "  ?? DECISION ─────────────────────────────────────────────"
  echo "     $q"; for l in "$@"; do echo "     · $l"; done
  echo "     default: $def"
  if [ "$AUTO" = yes ]; then echo "     --auto: taking the default"; REPLY=$def; return 0; fi
  read -r -p "     proceed? [Y/n/a(bort)] " REPLY </dev/tty
  case "${REPLY:-y}" in [Nn]*) echo "     skipped"; return 1;; [Aa]*) echo "     aborted"; exit 3;; *) return 0;; esac; }

mode  () { on "$1" "cubrid changemode $DB 2>/dev/null | grep -oE '\b(active|standby|maintenance)\b' | tail -1" 2>/dev/null | tr -d '\r\n'; }
applied () { on "$1" "csql -u dba -t -N -c \"SELECT eof_lsa_pageid, final_lsa_pageid, eof_lsa_pageid-final_lsa_pageid, fail_counter FROM db_ha_apply_info\" $DB 2>/dev/null | tr -s ' '" 2>/dev/null | tr -d '\r' | tail -1; }

echo "════════════════════════════════════════════════════════════════════"
echo " CUBRID HA failback — $CUR (current master)  ->  $TGT (original master)"
echo " db=$DB  transport=$EXECMODE  auto=$AUTO"
echo "════════════════════════════════════════════════════════════════════"

STEP "Confirm which node is actually master right now"
NOTE "$CUR : $(mode $CUR)"; NOTE "$TGT : $(mode $TGT)"
if [ "$(mode $CUR)" != active ]; then
  NOTE "!! $CUR is not active. Either the failover already reverted, or you named the wrong node."
  exit 1
fi
if [ "$(mode $TGT)" = active ]; then
  NOTE "!! BOTH nodes are active -- this is split brain, not a completed failover."
  NOTE "   Failback is the wrong operation here; one side's writes are about to be lost."
  DECIDE "Continue anyway?" n \
    "The engine resolves split brain on its own once the nodes can see each other:" \
    "it logs [Failback] [Diagnosis] Multiple master nodes and demotes one (measured, OQ9 arm A)." \
    "Doing it by hand instead means YOU choose which side's writes survive." || exit 3
fi

STEP "Check that the target has applied everything it has been sent"
A=$(applied "$TGT"); NOTE "db_ha_apply_info on $TGT (eof / final / lag / fail): ${A:-<none>}"
LAG=$(echo "$A" | awk '{print $3}'); FAIL=$(echo "$A" | awk '{print $4}')
NOTE "apply lag = ${LAG:-?} log pages   fail_counter = ${FAIL:-?}"
NOTE "NOTE: this number is blind to a COPY stall. eof_lsa is how far copylogdb has"
NOTE "      FETCHED, so if copying is stuck, eof stops moving and lag reads 0 while"
NOTE "      the target is arbitrarily far behind the master (measured, OQ7 phase 4)."
NOTE "      The only view that sees it is: cubrid applyinfo -L <logpath> -r $CUR -a -i 1 $DB"
DECIDE "Is the target caught up enough to take over?" y \
  "A non-zero lag means the writes not yet applied are lost when $CUR steps down." \
  "A non-zero fail_counter means replication is BROKEN, not merely behind, and" \
  "failing back onto it will not fix itself." \
  "We do not know your threshold. Zero? A page? A second of business time?" || exit 3

STEP "Decide what happens to the application during the switch"
NOTE "Stopping the heartbeat on $CUR takes its SERVER down with it -- clients on"
NOTE "$CUR are disconnected, and there is a window with no master at all."
DECIDE "Has write traffic been quiesced / drained?" y \
  "CUBRID has no read-only mode to hold a master still while replication catches up." \
  "Anything committed on $CUR after the lag reading above is at risk." \
  "How do you actually do this -- connection draining at the broker, at the app," \
  "or do you simply accept the loss?" || exit 3

STEP "Stop the heartbeat on the current master ($CUR)"
NOTE "This is the switch. With $CUR out of the group, $TGT is the only node left"
NOTE "and the heartbeat promotes it."
DECIDE "Run: cubrid heartbeat stop on $CUR ?" y \
  "There is no supported way to demote a master in place: changemode refuses an" \
  "active->standby transition the heartbeat did not drive (server_support.c:1558)." \
  "If you use a different mechanism -- deregister, service stop, killing cub_master --" \
  "tell us which and why." || exit 3
NOTE "(bounded at 180 s: this command can hang forever if the node's HA processes"
NOTE " become zombies -- us_hb_deactivate polls \"is any cub_server running\" and a"
NOTE " zombie answers yes. Measured 2026-08-27 in a container without a reaping PID 1.)"
TMO=180; on_t () { local n=$1; shift; case "$EXECMODE" in
    docker) timeout $TMO docker exec -e CUBRID="/work/$n/cubrid" -e CUBRID_DATABASES=/db \
              -e CUBRID_CONF_FILE="/work/$n/cubrid/conf/ha.conf" \
              -e CUBRID_HA_CONF_FILE="/work/$n/cubrid/conf/cubrid_ha.conf" \
              -e PATH="/work/$n/cubrid/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
              -e LD_LIBRARY_PATH="/work/$n/cubrid/lib:/work/$n/cubrid/cci/lib" \
              "$n" bash -lc "$*" ;;
    ssh)    timeout $TMO ssh "$n" "$*" ;;
  esac; }
on_t "$CUR" "cubrid heartbeat stop" 2>&1 | sed 's/^/     /'
if [ "${PIPESTATUS[0]}" = 124 ]; then
  NOTE "!! heartbeat stop did not return within ${TMO}s. The deactivation itself may"
  NOTE "   still have succeeded -- checking the roles below is what decides."
fi

STEP "Wait for the target to be promoted"
for i in $(seq 1 30); do M=$(mode "$TGT"); [ "$M" = active ] && break; sleep 2; done
NOTE "$TGT is now: ${M:-<unknown>} (after $((i*2))s)"
[ "$M" = active ] || { NOTE "!! $TGT did not take over. STOP HERE -- the cluster has no master."; exit 1; }

STEP "Bring the old master back as a slave"
NOTE "TRAP: after 'heartbeat stop', 'heartbeat start' alone fails with"
NOTE "      \"CUBRID heartbeat feature is being deactivated\". A full service"
NOTE "      stop/start is required first (measured in the CBRD-26983 session)."
DECIDE "Run: cubrid service stop; cubrid service start; on $CUR ?" y \
  "If $CUR's log diverged -- it accepted writes $TGT never received -- rejoining" \
  "may not be possible at all, and it needs a fresh backupdb/restoreslave instead." \
  "Do you check for divergence before rejoining, and how?" || exit 3
on "$CUR" "cubrid service stop"  2>&1 | tail -3 | sed 's/^/     /'
on "$CUR" "cubrid service start" 2>&1 | tail -3 | sed 's/^/     /'
sleep 5
on "$CUR" "cubrid heartbeat start" 2>&1 | tail -3 | sed 's/^/     /'

STEP "Verify the cluster"
for i in $(seq 1 30); do
  MC=$(mode "$CUR"); MT=$(mode "$TGT")
  [ "$MT" = active ] && [ "$MC" = standby ] && break; sleep 2
done
NOTE "$TGT : ${MT:-?}   $CUR : ${MC:-?}"
NOTE "replication on $CUR (eof / final / lag / fail): $(applied $CUR)"
if [ "$MT" = active ] && [ "$MC" = standby ]; then echo; echo "  ✓ failback complete: $TGT is master, $CUR is its slave"
else echo; echo "  ✗ NOT in the expected state -- $TGT=$MT $CUR=$MC"; exit 1; fi

cat <<'EOT'

────────────────────────────────────────────────────────────────────
 What we still do not know, and would like you to write in:
   1. Your threshold for "caught up" at STEP 2, and what you do when it
      is not met.
   2. How write traffic is actually quiesced at STEP 3 -- or whether it
      simply is not.
   3. Whether STEP 5's heartbeat stop is what you use, or something else.
   4. How you detect that the old master's log diverged before rejoining
      it at STEP 6, and what you do when it has.
   5. Every step we did not write down.
────────────────────────────────────────────────────────────────────
EOT
