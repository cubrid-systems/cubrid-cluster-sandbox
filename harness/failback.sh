#!/bin/bash
# N65 G8 — return a CUBRID HA cluster to its ORIGINAL master, semi-automatically.
#          "failback" means two different things here -- see VOCABULARY below.
#
# ============================================================================
#  RUN IT. IT STOPS WHERE YOU HAVE TO DECIDE.
#
#  This started as a questionnaire: a guess at the sequence, written to be
#  marked up. Most of what it guessed at has since been answered out of the
#  field's own records, so the guesses are gone and what is left is a script
#  that does the thing and pauses where a person genuinely has to choose.
#
#  Every DECIDE block prints what is known, what was measured, and what the
#  default is; --auto takes them all. One of them -- STEP 3 -- is the one
#  nothing found so far answers, and it is marked. If your site does it
#  differently, that disagreement is the most useful thing you can send back,
#  and the repository is where it goes.
#  -- CUBRID Systems Research, N65 cluster-sandbox, 2026-08-27 (revised 2026-09-02)
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
# VOCABULARY -- please read this before answering anything below.
#   The word means two things in your own material and we cannot tell from a
#   sentence which one is meant:
#     the ENGINE logs [Failback] when a master steps down, and your study notes
#     define it that way -- 마스터 노드가 슬레이브 노드가 되는 것;
#     your OPERATIONS tickets say "failback 작업" for putting the service back on
#     the node that was master, which is the opposite direction. One of them is
#     explicit about it: "재구성 없이 1번을 마스터로 failback만 하는 작업".
#   THIS SCRIPT MEANS THE SECOND ONE. Every question below is about the return
#   trip, not about a demotion.
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

echo; echo "── STEP 0 ── Should this be happening at all?"
NOTE "The failback that costs the field most is not a deliberate return: four sites"
NOTE "reported ten or more failover / split-brain / failback cycles A DAY under load,"
NOTE "with no PING failure in the logs. If this is one of those, moving the service"
NOTE "back to $TGT fixes nothing and the cycle repeats tonight."
NOTE ""
NOTE "ha_ping_hosts has three states and all three have failed in production:"
NOTE "  unreachable host   -> the slave cannot promote at all; the service stays down"
NOTE "  unset              -> a partition is never diagnosed, so: split brain"
NOTE "  set and reachable  -> split brain anyway, when the ping host survives the cut"
NOTE "                        (measured here: two masters in 9 s)"
DECIDE "Do you know what triggered the failover, and is it a one-off?" y \
  "If it recurs, the thing to fix is the trigger, not the roles." \
  "Which of the three ha_ping_hosts states are your clusters in?" \
  "Which of them have you met in production, and what did you do about it?" \
  || NOTE "-> then this may be the wrong operation today. Continuing anyway."

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
NOTE "If that reads <none>: a JUST-DEMOTED node has no db_ha_apply_info row at all"
NOTE "      until its applier writes one. Expected here, not an error -- and it is the"
NOTE "      third distinct way this view misleads (measured 2026-08-27)."
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
  "Your own rebuild procedure answers this as a METHOD rather than a number:" \
  "cubrid applyinfo -r <master> -L <copylog> -a, plus a repl_test table you" \
  "create and insert into and watch arrive. So the question is narrower than we" \
  "first asked it -- is that canary the whole check here too, or is there also a" \
  "number you will not go below? And do you read fail_counter before failing" \
  "back at all, or only after a rebuild?" || exit 3

STEP "Decide what happens to the application during the switch"
NOTE "Stopping the heartbeat on $CUR takes its SERVER down with it -- clients on"
NOTE "$CUR are disconnected, and there is a window with no master at all."
DECIDE "Has write traffic been quiesced / drained?" y \
  "CUBRID has no read-only mode to hold a master still while replication catches up." \
  "Anything committed on $CUR after the lag reading above is at risk." \
  "The tracker says your answer is the BROKER: ACCESS_MODE moved to RO or SO before" \
  "anyone touches replicated data. Is that what you do here too?" \
  "If so: before or after STEP 2's reading, and who puts it back afterwards?" \
  "If you do not quiesce at all, say so -- that is an answer, and we will design" \
  "for it rather than around it." || exit 3

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
  "may not be possible at all, and your path back is the rebuild." \
  "We have read your procedures for that: the seventeen-step manual order, and" \
  "the 2025 online procedure that backs up from a REPLICA so the master is never" \
  "touched, costed at about eleven hours end to end. Neither is a thing anyone" \
  "does casually." \
  "So the question is which parts of it a RETURN TRIP borrows. We have seen one" \
  "of your own calls ask for exactly this -- 재구성 없이 1번을 마스터로 failback -- " \
  "so we know it is sometimes none of it. What tells you which case you are in," \
  "and is it the same signal either way?" \
  "Two details from those documents we would like confirmed as current: that you" \
  "pause replication with cubrid heartbeat deregister <pid> rather than by" \
  "signalling the process, and that the slave db_ha_apply_info row is still" \
  "hand-written from the LSA values in the backup log." || exit 3
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
 What this script knows, and the one thing it does not.

 Most of what it used to ask was answered by reading the field's own
 records rather than by asking, so those questions are gone:

   caught up enough   a method, not a number: applyinfo -r with -a, plus a
                      row you insert and watch arrive
   quiescing          the broker's ACCESS_MODE, moved to RO or SO
   is the return trip worth doing
                      yes -- it is scheduled, it is routine, and it is
                      sometimes done with no rebuild at all

 What is left, and what this script cannot decide for you:

   *  WHO AUTHORISES THIS, AND ON WHAT EVIDENCE.  Your records show it
      scheduled -- a week after the incident, on a named date, carried out
      by the first-line vendor with support on the phone. They do not show
      who signs it off, or what they look at before they do. That is the
      one decision this script leaves entirely to the person running it,
      and STEP 3 is where it lands.

 Smaller things we would still take a correction on:

   -  whether the canary is your whole "caught up" check, or a number sits
      behind it;
   -  whether STEP 4's heartbeat stop is what you use, or something else;
   -  how much of the rebuild a return trip borrows at your site;
   -  every step we did not write down.

 Six is answered, and we have removed the pretence of asking it: your
 records show the return trip is routine, scheduled, and sometimes done
 with no rebuild at all. FIVE is the one nothing we have found answers,
 and it is the one that decides what this script should do without
 asking.
────────────────────────────────────────────────────────────────────
EOT
