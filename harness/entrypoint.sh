#!/bin/bash
# N65 cluster-sandbox — one node of a CUBRID HA group in a container.
#
# Lifted from N54's WU-51b entrypoint with one addition: HA_PING_HOSTS, because
# OQ9 is about what the ping check decides and the whole experiment turns on
# whether that parameter is set. Everything else is 51b's, including the two
# configuration facts it paid for:
#
#   ha_copy_sync_mode is deliberately ABSENT -- it takes one entry per node in
#   ha_node_list (util_common.c:894 iterates num_ha_nodes), so a value correct for
#   one node is a hard "Invalid Parameter" startup failure for two. Unset, every
#   node defaults to "sync" and cannot go out of step with the node count.
#
#   Both nodes mount their own database directory at the SAME container path
#   (/db), because ${DB}_vinf stores ABSOLUTE volume paths -- a slave whose
#   directory sits elsewhere mounts the MASTER's files and dies on "is in use by
#   ... on host <master>".
set -uo pipefail

RO=${CUBRID_RO:-/opt/cubrid-ro}
export CUBRID=${CUBRID:-/work/$(hostname)/cubrid}

[ -x "$RO/bin/cub_server" ] || { echo "!! $RO is not a CUBRID install" >&2; exit 2; }

mkdir -p "$CUBRID"
for d in bin lib cci msg locales timezones share include 3rdparty demo vm java; do
  [ -e "$RO/$d" ] && ln -sfn "$RO/$d" "$CUBRID/$d"
done
mkdir -p "$CUBRID/conf" "$CUBRID/databases" "$CUBRID/log" "$CUBRID/var" "$CUBRID/tmp"
cp -f "$RO"/conf/*.conf "$CUBRID/conf/" 2>/dev/null || true

export PATH=$CUBRID/bin:$PATH
export LD_LIBRARY_PATH=$CUBRID/lib:$CUBRID/cci/lib:${LD_LIBRARY_PATH:-}
export CUBRID_DATABASES=${CUBRID_DATABASES:-/work/$(hostname)/db}
mkdir -p "$CUBRID_DATABASES"

DB=${HA_DBS:-hadb}
NODES=${HA_NODES:?HA_NODES must list both hosts, e.g. "n1:n2"}
GROUP=${HA_GROUP:-hagrp}

sed '/^ha_mode[ ]*=/d' "$CUBRID/conf/cubrid.conf" > "$CUBRID/conf/base.conf"
awk '/^\[common\]/{print; print "ha_mode=on"; next} {print}' "$CUBRID/conf/base.conf" > "$CUBRID/conf/ha.conf"
{ echo "[common]"; echo "cubrid_port_id=${CUBRID_PORT_ID:-31523}"; } >> "$CUBRID/conf/ha.conf"
export CUBRID_CONF_FILE="$CUBRID/conf/ha.conf"

cat > "$CUBRID/conf/cubrid_ha.conf" <<CONF
[common]
ha_port_id=${HA_PORT_ID:-59901}
ha_node_list=$GROUP@$(echo "$NODES" | tr ':' ' ' | tr ' ' ':')
ha_db_list=$DB
ha_apply_max_mem_size=300
ha_copy_log_max_archives=10
CONF

# The parameter OQ9 is about. Unset is the DEFAULT and is one of the two routes to
# split brain; set to a third host that survives the partition is the other. Note
# hb_check_ping() refuses a host that is also a cluster node (HB_PING_USELESS_HOST),
# and a useless host does not increment ping_try_count -- so pointing this at a peer
# is the same as leaving it unset.
if [ -n "${HA_PING_HOSTS:-}" ]; then
  echo "ha_ping_hosts=$HA_PING_HOSTS" >> "$CUBRID/conf/cubrid_ha.conf"
fi
export CUBRID_HA_CONF_FILE="$CUBRID/conf/cubrid_ha.conf"

echo "== $(hostname): \$CUBRID=$CUBRID  group=$GROUP  nodes=$NODES  db=$DB  ping=${HA_PING_HOSTS:-<unset>}"
sed 's/^/   /' "$CUBRID/conf/cubrid_ha.conf"

case "${1:-node}" in
  node)
    if [ "${ROLE:-master}" = "master" ]; then
      if ! grep -qw "^$DB" "$CUBRID_DATABASES/databases.txt" 2>/dev/null; then
        rm -f "$CUBRID_DATABASES/.createdb-done"
        ( cd "$CUBRID_DATABASES" && cubrid createdb --db-volume-size=${DBVOL:-512M} \
            --log-volume-size=${LOGVOL:-256M} "$DB" en_US.utf8 ) >/dev/null
        # The sentinel exists because databases.txt does NOT mean "createdb finished".
        # Seeding the slave on the databases.txt entry copies a database with a live
        # transaction still in it: the slave's recovery then reaches its UNDO phase
        # and dies on "fetching deallocated pageid ... of volume /db/hadb"
        # (log_recovery.c:953, LOG FATAL ERROR: log_recovery:locator_initialize).
        # Measured 2026-08-27. This is the fifth ordering trap in this assembly and
        # the only one that produces a corrupt slave rather than a failed start.
        touch "$CUBRID_DATABASES/.createdb-done"
        echo "== $(hostname): created $DB"
      fi
    fi
    exec /usr/bin/tail -F /dev/null
    ;;
  shell) exec /bin/bash ;;
  *)     exec "$@" ;;
esac
