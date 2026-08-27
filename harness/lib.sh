#!/bin/bash
# N65 cluster-sandbox — shared cluster lifecycle for the OQ7/OQ9 experiments.
#
# This file is the honest floor the foundation's Phase 0 talks about: the manual
# assembly, written down once, so the experiments below stop re-deriving it. Every
# function here is a step a provisioner would own.
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ENGINE=${ENGINE:-/data/workspace/for-plan/importdb/cubrid/install.out}
IMAGE=${IMAGE:-cubrid-n65:local}
DB=${DB:-hadb}

cs_build () {
  [ -x "$ENGINE/bin/cub_server" ] || { echo "!! no engine at $ENGINE" >&2; return 2; }
  docker build -q -t "$IMAGE" --build-arg UID="$(id -u)" --build-arg GID="$(id -g)" "$HERE" >/dev/null
}

# cs_launch <name> <net> <role> <workdir> [ping_hosts]
cs_launch () {
  local c=$1 net=$2 role=$3 work=$4 ping=${5:-}
  mkdir -p "$work/$c/db"
  # --init is NOT optional. Without a reaping PID 1, `cubrid heartbeat stop` never
  # returns: us_hb_deactivate loops on COMMDB_HA_DEACT_CONFIRM_NO_SERVER with
  # sleep(1) until no cub_server is running (util_service.c:3995-4004), and a
  # zombie cub_server still counts as running. Measured 2026-08-27 -- the command
  # sat in hrtimer_nanosleep for five minutes with cub_server, cub_pl and both
  # cub_admin processes defunct and reparented to `tail -F /dev/null`.
  docker run -d --name "$c" --hostname "$c" --network "$net" \
    --init --cap-add=NET_ADMIN \
    -v "$ENGINE":/opt/cubrid-ro:ro -v "$work":/work -v "$work/$c/db":/db \
    -e HA_NODES="$N1:$N2" -e HA_DBS="$DB" -e ROLE="$role" \
    -e HA_PING_HOSTS="$ping" -e CUBRID_DATABASES=/db --shm-size=1g \
    "$IMAGE" node >/dev/null
}

# cs_ex <container> <command...>  — run with the node's CUBRID environment
cs_ex () { local c=$1; shift; docker exec \
    -e CUBRID="/work/$c/cubrid" -e CUBRID_DATABASES=/db \
    -e CUBRID_CONF_FILE="/work/$c/cubrid/conf/ha.conf" \
    -e CUBRID_HA_CONF_FILE="/work/$c/cubrid/conf/cubrid_ha.conf" \
    -e PATH="/work/$c/cubrid/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
    -e LD_LIBRARY_PATH="/work/$c/cubrid/lib:/work/$c/cubrid/cci/lib" \
    "$c" bash -lc "$*"; }

cs_ip () { docker inspect -f "{{(index .NetworkSettings.Networks \"$2\").IPAddress}}" "$1" 2>/dev/null; }

# The link cut. A blackhole route on each side drops everything to the peer while
# leaving every other destination -- crucially the ping host -- reachable. This is
# the partition `docker network disconnect` cannot express, and it is exactly the
# shape OQ9 needs.
cs_cut  () { docker exec -u 0 "$1" ip route add blackhole "$2" 2>&1 | sed 's/^/     /'; }
cs_heal () { docker exec -u 0 "$1" ip route del blackhole "$2" 2>&1 | sed 's/^/     /'; }

cs_mode () { cs_ex "$1" "cubrid changemode $DB 2>&1 | tail -1" 2>/dev/null | tr -d '\r'; }
cs_short_mode () { cs_mode "$1" | grep -oE '\b(active|standby|maintenance|to-be-active|to-be-standby)\b' | tail -1; }
cs_hblog () { cs_ex "$1" "grep -ho '\[Fail[a-z]*\] \[[A-Za-z]*\].*' /work/$1/cubrid/log/* 2>/dev/null | tail -${2:-8}" 2>/dev/null; }

# The four-step assembly, in the one order that works (N54 WU-51b paid for each).
cs_up () {
  local net=$1 work=$2 ping=${3:-}
  docker network create "$net" >/dev/null 2>&1
  echo "== [1/4] launching $N1 (master) and $N2 (slave) on $net   ping_hosts=${ping:-<unset>}"
  cs_launch "$N1" "$net" master "$work" "$ping"
  cs_launch "$N2" "$net" slave  "$work" "$ping"
  local i
  # Wait for the SENTINEL, not for databases.txt -- see entrypoint.sh.
  for i in $(seq 1 90); do [ -e "$work/$N1/db/.createdb-done" ] && break; sleep 2; done
  echo "   master createdb done after $((i*2))s"

  echo "== [2/4] seeding the slave from the master's volumes"
  cs_ex "$N2" "cp -a /work/$N1/db/${DB}* /work/$N2/db/ && rm -f /work/$N2/db/${DB}_lgat__lock && \
    sed 's#/work/$N1/db#/work/$N2/db#g' /work/$N1/db/databases.txt > /work/$N2/db/databases.txt && \
    ls /work/$N2/db | tr '\n' ' '" 2>&1 | sed 's/^/   /'

  echo "== [3/4] heartbeat start on BOTH nodes concurrently (it blocks until the group forms)"
  ( cs_ex "$N1" "cubrid heartbeat start" > "$work/hb-$N1.log" 2>&1 ) &
  ( cs_ex "$N2" "cubrid heartbeat start" > "$work/hb-$N2.log" 2>&1 ) &
  wait

  echo "== [4/4] waiting for the master to reach registered_and_active"
  local st=""
  for i in $(seq 1 40); do
    st=$(cs_ex "$N1" "cubrid heartbeat status 2>/dev/null | grep -o 'registered_and_[a-z_]*' | head -1" 2>/dev/null | tr -d '\r\n ')
    [ "$st" = "registered_and_active" ] && break
    sleep 2
  done
  echo "   master server: ${st:-<unknown>} (after $((i*2))s)"
  local st2
  st2=$(cs_ex "$N2" "cubrid heartbeat status 2>/dev/null | grep -o 'registered_and_[a-z_]*' | head -1" 2>/dev/null | tr -d '\r\n ')
  echo "   slave server:  ${st2:-<unknown>}"
  if [ -z "$st2" ]; then
    echo "   !! the slave never registered -- its start log:"
    tail -6 "$work/hb-$N2.log" 2>/dev/null | sed 's/^/      /'
  fi
  [ "$st" = "registered_and_active" ] && [ -n "$st2" ]
}

cs_down () { docker rm -f "$N1" "$N2" ${PINGHOST:-} >/dev/null 2>&1; docker network rm "$1" >/dev/null 2>&1; }
