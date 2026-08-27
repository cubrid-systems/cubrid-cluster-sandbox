#!/bin/bash
# N65 OQ9 — does split brain need a deliberately broken configuration?
#
# The foundation carries a code reading and marks it read-not-run. The ping check
# (master_heartbeat.c:1042-1054) cancels a MASTER's failback when
# `ping_try_count == 0` OR the ping succeeded, and cancels a SLAVE's failover only
# when it tried and failed. Two routes therefore end with two masters:
#
#   A  ha_ping_hosts set to a third host that SURVIVES the partition.
#      Master pings fine -> "not a network partition" -> stays active.
#      Slave  pings fine -> no cancel -> promotes.            => predicted SPLIT BRAIN
#   B  ha_ping_hosts unset (the default, and what the 2026-08-18 session hit).
#      Master has no host to ping -> cancel for want of one -> stays active.
#      Slave has nothing to fail  -> no cancel -> promotes.   => predicted SPLIT BRAIN
#   C  ha_ping_hosts set, and the MASTER is cut from the ping host as well.
#      Master's ping fails -> failback -> demotes.
#      Slave's ping succeeds -> promotes.                     => predicted CLEAN FAILOVER
#
# C is the control: if it does not demote, the mechanism is not what the code says
# and A/B prove nothing. The link cut is a blackhole route rather than
# `docker network disconnect`, because A and C differ only in whether the ping host
# stays reachable -- which a whole-interface disconnect cannot express.
#
# Usage: bash oq9-splitbrain.sh [A|B|C]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ARM=${1:-A}
NET=n65-oq9-net; N1=n65-n1; N2=n65-n2; PINGHOST=n65-ping
WORK=${WORK:-$HERE/out/oq9-$ARM}
export N1 N2 PINGHOST
. "$HERE/lib.sh"

case "$ARM" in
  A) PING=$PINGHOST; CUT_PING_FROM_MASTER=no  ;;
  B) PING="";        CUT_PING_FROM_MASTER=no  ;;
  C) PING=$PINGHOST; CUT_PING_FROM_MASTER=yes ;;
  *) echo "!! arm must be A, B or C" >&2; exit 2 ;;
esac

cleanup () { docker rm -f "$N1" "$N2" "$PINGHOST" >/dev/null 2>&1; docker network rm "$NET" >/dev/null 2>&1; }
trap cleanup EXIT
cleanup
rm -rf "$WORK"; mkdir -p "$WORK"

echo "############ OQ9 arm $ARM ############"
echo "  ha_ping_hosts = ${PING:-<unset>}   master also cut from ping host: $CUT_PING_FROM_MASTER"
cs_build || exit 2
docker network create "$NET" >/dev/null 2>&1
docker run -d --name "$PINGHOST" --hostname "$PINGHOST" --network "$NET" ubuntu:24.04 tail -f /dev/null >/dev/null
cs_up "$NET" "$WORK" "$PING" || { echo "!! cluster never came up"; exit 1; }

IP1=$(cs_ip "$N1" "$NET"); IP2=$(cs_ip "$N2" "$NET"); IPP=$(cs_ip "$PINGHOST" "$NET")
echo "== addresses: $N1=$IP1  $N2=$IP2  $PINGHOST=$IPP"
echo "== ping reachability before the cut"
for c in "$N1" "$N2"; do printf "   %-8s -> %s : " "$c" "$PINGHOST"
  docker exec "$c" bash -c "ping -w 1 -c 1 $PINGHOST >/dev/null 2>&1; echo \$?" | tr -d '\r\n'; echo; done
echo "== baseline roles"
for c in "$N1" "$N2"; do printf "   %-8s %s\n" "$c" "$(cs_mode $c)"; done

echo
echo "== CUT: blackhole the peer on both nodes$([ $CUT_PING_FROM_MASTER = yes ] && echo ', and the ping host on the master')"
cs_cut "$N1" "$IP2"; cs_cut "$N2" "$IP1"
[ "$CUT_PING_FROM_MASTER" = yes ] && cs_cut "$N1" "$IPP"
T0=$(date +%s)
{
  echo "t  $N1  $N2"
  for i in $(seq 1 40); do
    printf "%3ds  %-12s %-12s\n" "$(( $(date +%s) - T0 ))" "$(cs_short_mode $N1)" "$(cs_short_mode $N2)"
    sleep 3
  done
} | tee "$WORK/roles.log" &
SAMPLER=$!
sleep 75; kill $SAMPLER 2>/dev/null; wait $SAMPLER 2>/dev/null

echo
echo "== roles while partitioned"
M1=$(cs_short_mode "$N1"); M2=$(cs_short_mode "$N2")
printf "   %-8s %s\n   %-8s %s\n" "$N1" "${M1:-?}" "$N2" "${M2:-?}"
if [ "$M1" = active ] && [ "$M2" = active ]; then echo "   >>> TWO MASTERS (split brain)"; 
elif [ "$M1" = standby ] && [ "$M2" = active ]; then echo "   >>> clean failover (master demoted)";
else echo "   >>> neither: $M1 / $M2"; fi
echo "== heartbeat log, $N1"; cs_hblog "$N1" 10 | sed 's/^/     /'
echo "== heartbeat log, $N2"; cs_hblog "$N2" 10 | sed 's/^/     /'

echo
echo "== HEAL: remove the blackhole routes, then watch for the split-brain diagnosis"
cs_heal "$N1" "$IP2"; cs_heal "$N2" "$IP1"
[ "$CUT_PING_FROM_MASTER" = yes ] && cs_heal "$N1" "$IPP"
sleep 45
echo "== roles after heal"
for c in "$N1" "$N2"; do printf "   %-8s %s\n" "$c" "$(cs_short_mode $c)"; done
echo "== heartbeat log after heal, $N1"; cs_hblog "$N1" 12 | sed 's/^/     /'
echo "== heartbeat log after heal, $N2"; cs_hblog "$N2" 12 | sed 's/^/     /'
for c in "$N1" "$N2"; do cs_ex "$c" "cat /work/$c/cubrid/log/* 2>/dev/null" > "$WORK/masterlog-$c.txt" 2>&1; done
echo "== artifacts: $WORK"
