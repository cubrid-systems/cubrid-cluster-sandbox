# N65 cluster-sandbox — the experiment image for OQ7 (lag) and OQ9 (split brain).
#
# Derived from N54's WU-51b harness image (for-plan/importdb/m5/ha51b-docker), which
# established that a host-built CUBRID tree runs unmodified in a stock ubuntu:24.04
# container when it is bind-mounted read-only and a writable $CUBRID is assembled
# over it out of symlinks. Two additions, both required by what N65 is testing:
#
#   iputils-ping  hb_check_ping() does not open a socket -- it runs
#                 popen("ping -w 1 -c 1 <host> >/dev/null 2>&1; echo $?")
#                 (master_heartbeat.c). With no ping binary, sh returns 127, which
#                 the caller reads as HB_PING_FAILURE. A missing ping is therefore
#                 indistinguishable from a partitioned ping host, so OQ9 cannot be
#                 run without it -- and a provisioner cannot ship an image without it.
#   iproute2      already present in the 51b image; N65 uses it for both fault
#                 mechanisms: `ip route add blackhole <peer>` for a link cut that
#                 leaves the ping host reachable, and `tc qdisc netem delay` for the
#                 network half of OQ7. Both need --cap-add=NET_ADMIN at run time.
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 bc procps iproute2 iputils-ping gawk sed grep coreutils util-linux \
    && rm -rf /var/lib/apt/lists/*

ARG UID=1000
ARG GID=1000
RUN if ! getent group $GID >/dev/null; then groupadd -g $GID cub; fi \
    && useradd -u $UID -g $GID -m -s /bin/bash cub 2>/dev/null || true

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh

USER $UID
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["node"]
