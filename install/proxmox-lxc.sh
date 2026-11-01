#!/bin/sh
# Creates a small unprivileged Debian container on a Proxmox VE host and
# installs dyno in it (ADR-0024). Run it on the Proxmox host as root:
#
#   curl -fsSLO https://github.com/redlinelabs-dev/dyno/releases/latest/download/proxmox-lxc.sh
#   sh proxmox-lxc.sh
#
# To upgrade, run it again with the container's id:
#
#   DYNO_CTID=105 sh proxmox-lxc.sh --upgrade
#
# Everything has a default; override with environment variables:
#   DYNO_CTID       container id (default: the next free id)
#   DYNO_HOSTNAME   default dyno
#   DYNO_STORAGE    storage for the root disk (default: first active one that holds containers)
#   DYNO_BRIDGE     default vmbr0
#   DYNO_IP         dhcp (default) or an address like 192.168.1.50/24
#   DYNO_GW         gateway, needed with a static DYNO_IP
#   DYNO_CORES      default 1
#   DYNO_MEMORY     MiB, default 256
#   DYNO_DISK       GiB, default 2
#   DYNO_VERSION    release tag, e.g. v0.1.0 (default: latest)
#   DYNO_BINARY     install this dyno binary from the host instead of downloading one
#   DYNO_LISTEN, DYNO_METRICS_LISTEN   passed to the install (see install.sh)
#   DYNO_TAILSCALE  1 to also install Tailscale in the container
#   TS_AUTHKEY      with DYNO_TAILSCALE=1, joins the tailnet without a login link

set -eu

MODE=create
case ${1:-} in
"") ;;
--upgrade) MODE=upgrade ;;
-h | --help) sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//' && exit 0 ;;
*) printf 'dyno: unknown option %s\n' "$1" >&2 && exit 1 ;;
esac

REPO=redlinelabs-dev/dyno
VERSION=${DYNO_VERSION:-latest}
HOSTNAME_=${DYNO_HOSTNAME:-dyno}
BRIDGE=${DYNO_BRIDGE:-vmbr0}
IP=${DYNO_IP:-dhcp}
CORES=${DYNO_CORES:-1}
MEMORY=${DYNO_MEMORY:-256}
DISK=${DYNO_DISK:-2}
TAILSCALE=${DYNO_TAILSCALE:-0}
# For tests: DYNO_BASE_URL replaces the GitHub release URL and PVE_LXC_DIR
# replaces /etc/pve/lxc.
BASE_URL=${DYNO_BASE_URL:-}
LXC_DIR=${PVE_LXC_DIR:-/etc/pve/lxc}
HERE=$(cd "$(dirname "$0")" && pwd)

say() { printf 'dyno: %s\n' "$*"; }
die() { printf 'dyno: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run as root on the Proxmox host"
command -v pct >/dev/null 2>&1 && command -v pveam >/dev/null 2>&1 ||
	die "this runs on a Proxmox VE host (no pct or pveam here). On any other Debian or Ubuntu machine, use install.sh"
if [ "$IP" != dhcp ] && [ -z "${DYNO_GW:-}" ]; then die "a static DYNO_IP needs DYNO_GW too"; fi

case $(uname -m) in
x86_64) ARCH=amd64 ;;
aarch64) ARCH=arm64 ;;
*) die "no dyno build for $(uname -m)" ;;
esac

if [ $MODE = upgrade ]; then
	CTID=${DYNO_CTID:?--upgrade needs DYNO_CTID, the id of the dyno container}
	pct status "$CTID" 2>/dev/null | grep -q running || die "container $CTID isn't running"
else
	CTID=${DYNO_CTID:-$(pvesh get /cluster/nextid)}
	if pct status "$CTID" >/dev/null 2>&1; then die "container $CTID already exists; to upgrade it, add --upgrade"; fi
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

fetch() { curl -fsSL --retry 3 -o "$2" "$1" || die "download failed: $1"; }

if [ -z "$BASE_URL" ]; then
	if [ "$VERSION" = latest ]; then
		BASE_URL=https://github.com/$REPO/releases/latest/download
	else
		BASE_URL=https://github.com/$REPO/releases/download/$VERSION
	fi
fi

# The binary and install.sh are fetched and checked here on the host and
# pushed in, so the container needs no curl and no route to GitHub.
if [ -n "${DYNO_BINARY:-}" ]; then
	[ -f "$DYNO_BINARY" ] || die "DYNO_BINARY: no file at $DYNO_BINARY"
	cp "$DYNO_BINARY" "$TMP/dyno"
	if [ -f "$HERE/install.sh" ]; then cp "$HERE/install.sh" "$TMP/install.sh"; else fetch "$BASE_URL/install.sh" "$TMP/install.sh"; fi
else
	say "downloading dyno ($VERSION, $ARCH)"
	fetch "$BASE_URL/SHA256SUMS" "$TMP/SHA256SUMS"
	fetch "$BASE_URL/dyno-linux-$ARCH" "$TMP/dyno-linux-$ARCH"
	fetch "$BASE_URL/install.sh" "$TMP/install.sh"
	(cd "$TMP" && grep -E " (dyno-linux-$ARCH|install\.sh)\$" SHA256SUMS | sha256sum -c --status) ||
		die "checksum mismatch; nothing was changed"
	mv "$TMP/dyno-linux-$ARCH" "$TMP/dyno"
fi

create_container() {
	STORAGE=${DYNO_STORAGE:-$(pvesm status -content rootdir | awk 'NR > 1 && $3 == "active" { print $1; exit }')}
	[ -n "$STORAGE" ] || die "no active storage holds containers; set DYNO_STORAGE"
	TSTORE=$(pvesm status -content vztmpl | awk 'NR > 1 && $3 == "active" { print $1; exit }')
	[ -n "$TSTORE" ] || die "no active storage holds container templates"

	say "finding the Debian template"
	pveam update >/dev/null || say "couldn't refresh the template list; using the cached one"
	TEMPLATE=
	for release in 13 12; do
		TEMPLATE=$(pveam available --section system | awk -v r="debian-$release-standard_" 'index($2, r) == 1 { print $2 }' | sort -V | tail -n 1)
		[ -n "$TEMPLATE" ] && break
	done
	[ -n "$TEMPLATE" ] || die "no Debian 12 or 13 template is available"
	if ! pveam list "$TSTORE" | grep -q "$TEMPLATE"; then
		say "downloading $TEMPLATE to $TSTORE"
		pveam download "$TSTORE" "$TEMPLATE" >/dev/null
	fi

	NET="name=eth0,bridge=$BRIDGE,ip=$IP"
	[ "$IP" = dhcp ] || NET="$NET,gw=$DYNO_GW"

	say "creating container $CTID ($HOSTNAME_, $CORES core, $MEMORY MiB, $DISK GiB on $STORAGE)"
	# nesting=1 lets the sandboxing in dyno's systemd unit work in an
	# unprivileged container.
	pct create "$CTID" "$TSTORE:vztmpl/$TEMPLATE" \
		--hostname "$HOSTNAME_" --description "dyno network speed test" --tags dyno \
		--cores "$CORES" --memory "$MEMORY" --swap 256 --rootfs "$STORAGE:$DISK" \
		--net0 "$NET" --unprivileged 1 --features nesting=1 --onboot 1 >/dev/null

	if [ "$TAILSCALE" = 1 ]; then
		# Tailscale needs /dev/net/tun, which an unprivileged container lacks.
		cat >>"$LXC_DIR/$CTID.conf" <<'EOF'
lxc.cgroup2.devices.allow: c 10:200 rwm
lxc.mount.entry: /dev/net/tun dev/net/tun none bind,create=file
EOF
	fi
	pct start "$CTID"
}

[ $MODE = create ] && create_container

say "waiting for the container's network"
i=0
until ADDR=$(pct exec "$CTID" -- hostname -I 2>/dev/null | cut -d' ' -f1) && [ -n "$ADDR" ]; do
	i=$((i + 1))
	[ $i -lt 30 ] || die "container $CTID has no address after 30 s; check DYNO_BRIDGE and DYNO_IP"
	sleep 1
done

pct exec "$CTID" -- mkdir -p /root/dyno-install
pct push "$CTID" "$TMP/dyno" /root/dyno-install/dyno --perms 0755
pct push "$CTID" "$TMP/install.sh" /root/dyno-install/install.sh
pct exec "$CTID" -- env DYNO_BINARY=/root/dyno-install/dyno \
	DYNO_LISTEN="${DYNO_LISTEN:-}" DYNO_METRICS_LISTEN="${DYNO_METRICS_LISTEN:-}" \
	sh /root/dyno-install/install.sh
pct exec "$CTID" -- rm -rf /root/dyno-install

if [ $MODE = create ] && [ "$TAILSCALE" = 1 ]; then
	say "installing Tailscale"
	pct exec "$CTID" -- sh -c 'apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl ca-certificates >/dev/null'
	pct exec "$CTID" -- sh -c 'curl -fsSL https://tailscale.com/install.sh -o /tmp/tailscale-install.sh && sh /tmp/tailscale-install.sh && rm /tmp/tailscale-install.sh'
	if [ -n "${TS_AUTHKEY:-}" ]; then
		pct exec "$CTID" -- tailscale up --auth-key "$TS_AUTHKEY" --hostname "$HOSTNAME_"
	else
		say "join the tailnet with: pct exec $CTID -- tailscale up --hostname $HOSTNAME_"
	fi
fi

if [ $MODE = create ]; then
	PORT=${DYNO_LISTEN:-:3966}
	say "done. dyno is container $CTID; open http://$ADDR:${PORT##*:}"
fi
say "upgrade later with: DYNO_CTID=$CTID sh proxmox-lxc.sh --upgrade"
