#!/bin/bash
# Switchover-threshold validation: vary one heartbeat setting and measure what
# the cluster actually does.
#
# The field asked for exactly this in 2021 and said developers cannot do it --
# validation belongs in a user's environment. A test that tried it has been open
# since 2022, unable to say whether an 8-11 s role change was the engine, the
# parameter, or the network that afternoon, because nothing recorded enough to
# separate them.
#
# The design of this sweep is self-validating. ha_max_heartbeat_gap and
# ha_heartbeat_interval_in_msecs multiply into the same predicted interval, so
# varying each in turn distinguishes "the parameter is inert" from "the value
# never reached the process": if the interval moves the measurement and the gap
# does not, the configuration is arriving and the gap specifically does nothing.
set -uo pipefail
CSB=${CSB:-./bin/csb}
ENGINE=${ENGINE:-/data/workspace/for-plan/importdb/cubrid/install.out}
OUT=${OUT:-harness/results/sweep-switchover.tsv}
REPEATS=${REPEATS:-1}

[ -s "$OUT" ] || printf 'param\tvalue\trun\tpredicted\tmeasured\tmasters_after\tcancel_reason\n' > "$OUT"

run () {                       # run <param> <value> <other-setting> [run-no]
  local param=$1 value=$2 other=$3 runno=${4:-1} name="sw$(date +%s%N | tail -c 6)"
  export CSB_CLUSTER=$name
  echo "== $param=$value  (run $runno)"
  $CSB cluster create --name "$name" --build "$ENGINE" \
       --set-hidden "$param=$value" ${other:+--set-hidden "$other"} --timeout 600s >/dev/null 2>&1 || {
    echo "   !! create failed"; return 1; }

  # Under load, because a threshold reached on an idle cluster is not the
  # threshold the field meets.
  $CSB load start --profile insert --rate 40/s --batch 50 --for 180s --timeout 60s >/dev/null 2>&1
  sleep 8

  # A partition, not a kill: the gap governs MISSED HEARTBEATS, and a dead
  # process is noticed immediately rather than waited for.
  $CSB fault partition master --timeout 60s >/dev/null 2>&1
  sleep 32

  local json=/tmp/$name.json
  $CSB record export --out "$json" --timeout 90s >/dev/null 2>&1
  local line
  line=$(python3 - "$json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
rc=[r for r in d["role_changes"] if r.get("trigger")=="fault.partition"]
if not rc:
    rc=[r for r in d["role_changes"] if r.get("measured")]
r=rc[-1] if rc else {}
print("%s\t%s" % (r.get("predicted","-"), r.get("measured","-")))
PY
)
  local masters reason
  masters=$($CSB ha status --json --timeout 30s 2>/dev/null | python3 -c 'import json,sys; print(sum(1 for n in json.load(sys.stdin)["data"]["nodes"] if n["server_state"]=="registered_and_active"))' 2>/dev/null)
  reason=$($CSB node exec all -- "grep -ho '\[Fail[a-z]*\] \[[A-Za-z]*\]' /work/\$(hostname)/cubrid/log/*master.err 2>/dev/null | tail -1" --timeout 60s 2>/dev/null | tr -d '\r' | tail -1)

  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$param" "$value" "$runno" "$line" "${masters:-?}" "${reason:-?}" >> "$OUT"
  echo "   predicted/measured: $line   masters=$masters"
  $CSB cluster destroy --cluster "$name" --purge --timeout 180s >/dev/null 2>&1
  rm -f "$json"
}

# Four points, repeated. A single run per point is what left the field's own
# measurement arguable; the baseline is what makes an outlier an effect, so it is
# repeated too rather than assumed.
for i in $(seq 1 "$REPEATS"); do
  run ha_max_heartbeat_gap             5     "ha_heartbeat_interval_in_msecs=500" "$i"   # all defaults
  run ha_max_heartbeat_gap             20    "ha_heartbeat_interval_in_msecs=500" "$i"
  run ha_heartbeat_interval_in_msecs   2000  "ha_max_heartbeat_gap=5"             "$i"
  run ha_calc_score_interval_in_msecs  15000 ""                                   "$i"
done

echo; column -t -s $'\t' "$OUT"

# The discriminator. The first two parameters moved nothing, which leaves two
# explanations that matter very differently: they are inert, or the values never
# reached cub_master. The field reported that this third one is the only one it
# could feel. If it moves the measurement, the delivery path works and the other
# two are genuinely inert.
echo; column -t -s $'\t' "$OUT"
