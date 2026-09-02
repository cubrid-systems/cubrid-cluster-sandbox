#!/bin/bash
# Which condition makes a gracefully stopped HA group refuse to come back?
#
# Observed three times on one cluster and not at all on a second built the same
# way: after `cubrid service stop` on both nodes and a restart, the node that was
# master holds registered_and_to_be_active indefinitely and the group has no
# master. The difference between the two clusters was visible but not isolated --
# the affected one had user data and an applier sitting two pages short.
#
# Four arms, one variable each. Every arm builds a cluster from nothing.
#
#   A  idle, both stopped together          the control: expected to come back
#   B  under load until the stop            outstanding replication at shutdown
#   C  master stopped first, slave 10s later stop order
#   D  slave stopped first, master 10s later stop order, reversed
set -uo pipefail
CSB=${CSB:-./bin/csb}
ENGINE=${ENGINE:-/data/workspace/for-plan/importdb/cubrid/install.out}
OUT=${OUT:-harness/results/isolate-to-be-active.tsv}
REPEATS=${REPEATS:-1}
[ -s "$OUT" ] || printf 'arm\trun\tmaster_state_after_up\tapply_eof\tapply_final\tstalled\n' > "$OUT"

arm () {
  local a=$1 runno=$2 name="tba$(date +%s%N | tail -c 6)"
  export CSB_CLUSTER=$name
  echo "== arm $a (run $runno)"
  $CSB cluster create --name "$name" --build "$ENGINE" --timeout 600s >/dev/null 2>&1 || {
    echo "   !! create failed"; return 1; }
  local n1 n2
  n1=$($CSB node exec master -- hostname --timeout 30s 2>/dev/null | tr -d '\r\n')
  n2=$($CSB node exec slave  -- hostname --timeout 30s 2>/dev/null | tr -d '\r\n')

  if [ "$a" = B ]; then
    $CSB load start --profile insert --rate 40/s --batch 50 --for 300s --timeout 60s >/dev/null 2>&1
    sleep 20
  fi

  case "$a" in
    A|B) $CSB node stop "$n1" --timeout 200s >/dev/null 2>&1
         $CSB node stop "$n2" --timeout 200s >/dev/null 2>&1 ;;
    C)   $CSB node stop "$n1" --timeout 200s >/dev/null 2>&1; sleep 10
         $CSB node stop "$n2" --timeout 200s >/dev/null 2>&1 ;;
    D)   $CSB node stop "$n2" --timeout 200s >/dev/null 2>&1; sleep 10
         $CSB node stop "$n1" --timeout 200s >/dev/null 2>&1 ;;
  esac

  # Start both heartbeats and watch the former master, without letting the tool's
  # own completion step mask the state we are here to observe.
  $CSB node start all --timeout 200s >/dev/null 2>&1
  local st="" i
  for i in $(seq 1 12); do
    sleep 5
    st=$($CSB ha status --json --timeout 30s 2>/dev/null | python3 -c '
import json,sys
try: d=json.load(sys.stdin)["data"]["nodes"]
except Exception: print(""); raise SystemExit
print(next((n["server_state"] for n in d if n["name"]==sys.argv[1]), ""))' "$n1")
    [ "$st" = "registered_and_active" ] && break
  done

  local pos eof final
  pos=$($CSB node exec "$n1" -- "csql -u dba -t -N -c 'SELECT eof_lsa_pageid, final_lsa_pageid FROM db_ha_apply_info' \$(basename \$CUBRID_DATABASES 2>/dev/null || echo $name) 2>/dev/null" --timeout 60s 2>/dev/null | tr -d '\r' | awk 'NF==2{print $1, $2; exit}')
  eof=$(echo "$pos" | awk '{print $1}'); final=$(echo "$pos" | awk '{print $2}')
  local stalled=no; [ "$st" != "registered_and_active" ] && stalled=YES

  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$a" "$runno" "${st:-<none>}" "${eof:-?}" "${final:-?}" "$stalled" >> "$OUT"
  echo "   $n1 -> ${st:-<none>}   stalled=$stalled"
  $CSB cluster destroy --cluster "$name" --purge --timeout 200s >/dev/null 2>&1
}

for i in $(seq 1 "$REPEATS"); do
  for a in A B C D; do arm "$a" "$i"; done
done
echo; column -t -s $'\t' "$OUT"

# Arm E, added after A-D all came back clean: reproduce what the tool itself was
# doing when the stall was first seen. `cluster up` re-seeded the standby --
# copying the master's volumes over a database that had been serving and
# replicating since -- and that bug was fixed before these arms were written. If
# the stall follows the re-seed, the engine was never the subject.
arm_E () {
  local runno=$1 name="tba$(date +%s%N | tail -c 6)"
  export CSB_CLUSTER=$name
  echo "== arm E (run $runno): re-seed a live standby, then restart"
  $CSB cluster create --name "$name" --build "$ENGINE" --timeout 600s >/dev/null 2>&1 || return 1
  local n1 n2 work
  n1=$($CSB node exec master -- hostname --timeout 30s 2>/dev/null | tr -d '\r\n')
  n2=$($CSB node exec slave  -- hostname --timeout 30s 2>/dev/null | tr -d '\r\n')
  work=$CSB_HOME/clusters/$name/work
  $CSB node exec master -- "csql -u dba -t -N -c 'CREATE TABLE t(i INT PRIMARY KEY)' $name" --timeout 60s >/dev/null 2>&1
  $CSB node exec master -- "csql -u dba -t -N -c 'INSERT INTO t VALUES (1),(2)' $name" --timeout 60s >/dev/null 2>&1
  sleep 6

  $CSB node stop "$n1" --timeout 200s >/dev/null 2>&1
  $CSB node stop "$n2" --timeout 200s >/dev/null 2>&1
  # the bug, by hand: the master's volumes over the standby's live database
  cp -a "$work/$n1/db/$name"* "$work/$n2/db/" 2>/dev/null
  rm -f "$work/$n2/db/${name}_lgat__lock"
  $CSB node start all --timeout 200s >/dev/null 2>&1

  local st="" i
  for i in $(seq 1 12); do
    sleep 5
    st=$($CSB ha status --json --timeout 30s 2>/dev/null | python3 -c '
import json,sys
try: d=json.load(sys.stdin)["data"]["nodes"]
except Exception: print(""); raise SystemExit
print(next((n["server_state"] for n in d if n["name"]==sys.argv[1]), ""))' "$n1")
    [ "$st" = "registered_and_active" ] && break
  done
  local stalled=no; [ "$st" != "registered_and_active" ] && stalled=YES
  printf 'E\t%s\t%s\t%s\t%s\t%s\n' "$runno" "${st:-<none>}" "-" "-" "$stalled" >> "$OUT"
  echo "   $n1 -> ${st:-<none>}   stalled=$stalled"
  $CSB cluster destroy --cluster "$name" --purge --timeout 200s >/dev/null 2>&1
}

if [ "${ARM_E:-}" = "1" ]; then
  for i in $(seq 1 "${REPEATS:-1}"); do arm_E "$i"; done
  echo; column -t -s $'\t' "$OUT"
fi
