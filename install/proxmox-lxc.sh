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
umask 022 # pct and what it runs inherit this, not whatever umask root has

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

# listen_ok VALUE succeeds for :PORT, HOST:PORT and [IPV6]:PORT with a port from
# 1 to 65535, the forms dyno can bind. A host is letters, digits, . and -; an
# IPv6 address is hex digits, : and . inside one pair of brackets.
listen_ok() {
	case $1 in
	\[*\]:*)
		host=${1%\]:*}; host=${host#\[}; port=${1##*\]:}
		case $host in "" | *[!0-9A-Fa-f:.]*) return 1 ;; esac ;;
	*:*)
		host=${1%:*}; port=${1##*:}
		case $host in *[!A-Za-z0-9.-]*) return 1 ;; esac ;;
	*) return 1 ;;
	esac
	# Six digits or more is too big, and keeps [ from seeing a number it can't hold.
	case $port in "" | *[!0-9]* | ??????*) return 1 ;; esac
	[ "$port" -ge 1 ] && [ "$port" -le 65535 ]
}

# check_listen NAME VALUE passes an empty value (not set) or one listen_ok accepts.
# install.sh writes the value into dyno.env and enforces the same rule, but only
# once the container exists.
check_listen() {
	[ -z "$2" ] || listen_ok "$2" || die "$1 must be a port or a host and port, like :8080 or 127.0.0.1:3967"
}

# link_host prints the host to open from the addresses in $1 (hostname -I): the
# first IPv4 one, else the first IPv6 one in brackets, which a :port needs.
link_host() {
	v6=
	for a in $1; do
		case $a in
		*:*) [ -n "$v6" ] || v6="[$a]" ;;
		*) echo "$a"; return ;;
		esac
	done
	echo "$v6"
}

[ "$(id -u)" -eq 0 ] || die "run as root on the Proxmox host"
command -v pct >/dev/null 2>&1 && command -v pveam >/dev/null 2>&1 ||
	die "this runs on a Proxmox VE host (no pct or pveam here). On any other Debian or Ubuntu machine, use install.sh"
if [ "$IP" != dhcp ] && [ -z "${DYNO_GW:-}" ]; then die "a static DYNO_IP needs DYNO_GW too"; fi
check_listen DYNO_LISTEN "${DYNO_LISTEN:-}"
check_listen DYNO_METRICS_LISTEN "${DYNO_METRICS_LISTEN:-}"

case $(uname -m) in
x86_64) ARCH=amd64 ;;
aarch64) ARCH=arm64 ;;
*) die "no dyno build for $(uname -m)" ;;
esac

if [ $MODE = upgrade ]; then
	CTID=${DYNO_CTID:?--upgrade needs DYNO_CTID, the id of the dyno container}
else
	# A pvesh that fails leaves CTID empty, which is refused below.
	CTID=${DYNO_CTID:-$(pvesh get /cluster/nextid)} || CTID=
fi
case $CTID in
"") die "pvesh gave no free container id; set DYNO_CTID, for example DYNO_CTID=105" ;;
*[!0-9]*) die "'$CTID' isn't a container id; use a number, for example DYNO_CTID=105" ;;
esac
if [ $MODE = upgrade ]; then
	pct status "$CTID" 2>/dev/null | grep -q running || die "container $CTID isn't running"
	# This script tags what it creates, so a mistyped id can't push dyno into
	# someone else's container. Tags are split on ; , and spaces.
	pct config "$CTID" 2>/dev/null | sed -n 's/^tags:[[:space:]]*//p' | tr ';, ' '\n\n\n' | grep -qx dyno ||
		die "container $CTID isn't tagged dyno; check DYNO_CTID. If it is dyno's container, add the tag dyno to it in the Proxmox UI and run this again"
elif pct status "$CTID" >/dev/null 2>&1; then
	die "container $CTID already exists; to upgrade it, add --upgrade"
fi

TMP=$(mktemp -d)
CREATED= # set once pct create has worked
# On the way out the temp folder goes, and a failure after the create says the
# container is still there, since nothing here removes it.
cleanup() {
	rc=$?
	rm -rf "$TMP"
	if [ "$rc" -ne 0 ] && [ -n "$CREATED" ]; then
		printf 'dyno: container %s was created and is still there; remove it with: pct stop %s; pct destroy %s\n' "$CTID" "$CTID" "$CTID" >&2
	fi
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP # dash skips the EXIT trap when a signal kills it

fetch() { curl -fsSL --retry 3 --connect-timeout 20 --max-time 600 -o "$2" "$1" || die "download failed: $1"; }

# verify FILE... checks each file in $TMP against $TMP/SHA256SUMS. Each file
# needs its own line, or a missing one would leave it unchecked.
verify() {
	for f in "$@"; do
		grep " $f\$" "$TMP/SHA256SUMS" >"$TMP/sum" || die "SHA256SUMS has no entry for $f"
		(cd "$TMP" && sha256sum -c --status sum) || die "checksum mismatch for $f; nothing was changed"
	done
}

if [ -z "$BASE_URL" ]; then
	if [ "$VERSION" = latest ]; then
		BASE_URL=https://github.com/$REPO/releases/latest/download
	else
		BASE_URL=https://github.com/$REPO/releases/download/$VERSION
	fi
fi

# The binary and install.sh are fetched and checked here on the host and
# pushed in, so the container needs no curl and no route to GitHub. With
# DYNO_BINARY the binary is yours and goes unchecked. An install.sh beside
# this script is yours too; without one, the release's is fetched and
# checked, since it runs as root in the container.
if [ -n "${DYNO_BINARY:-}" ]; then
	[ -f "$DYNO_BINARY" ] || die "DYNO_BINARY: no file at $DYNO_BINARY"
	cp "$DYNO_BINARY" "$TMP/dyno"
	if [ -f "$HERE/install.sh" ]; then
		cp "$HERE/install.sh" "$TMP/install.sh"
	else
		fetch "$BASE_URL/SHA256SUMS" "$TMP/SHA256SUMS"
		fetch "$BASE_URL/install.sh" "$TMP/install.sh"
		verify install.sh
	fi
else
	say "downloading dyno ($VERSION, $ARCH)"
	fetch "$BASE_URL/SHA256SUMS" "$TMP/SHA256SUMS"
	fetch "$BASE_URL/dyno-linux-$ARCH" "$TMP/dyno-linux-$ARCH"
	fetch "$BASE_URL/install.sh" "$TMP/install.sh"
	verify "dyno-linux-$ARCH" install.sh
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
	CREATED=1

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
until ADDR=$(link_host "$(pct exec "$CTID" -- hostname -I 2>/dev/null)") && [ -n "$ADDR" ]; do
	i=$((i + 1))
	[ $i -lt 30 ] || die "container $CTID has no address after 30 s; check DYNO_BRIDGE and DYNO_IP"
	sleep 1
done

pct exec "$CTID" -- mkdir -p /root/dyno-install
pct push "$CTID" "$TMP/dyno" /root/dyno-install/dyno --perms 0755
pct push "$CTID" "$TMP/install.sh" /root/dyno-install/install.sh
status=0
pct exec "$CTID" -- env DYNO_BINARY=/root/dyno-install/dyno \
	DYNO_LISTEN="${DYNO_LISTEN:-}" DYNO_METRICS_LISTEN="${DYNO_METRICS_LISTEN:-}" \
	sh /root/dyno-install/install.sh || status=$?
# The pushed binary goes whether or not the install worked.
pct exec "$CTID" -- rm -rf /root/dyno-install
if [ $status -ne 0 ]; then
	printf 'dyno: the install inside container %s failed (exit %s); its message is above. Fix that, then run: DYNO_CTID=%s sh proxmox-lxc.sh --upgrade\n' "$CTID" "$status" "$CTID" >&2
	exit "$status"
fi

if [ $MODE = create ] && [ "$TAILSCALE" = 1 ]; then
	say "installing Tailscale"
	pct exec "$CTID" -- sh -c 'apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl ca-certificates >/dev/null'
	pct exec "$CTID" -- sh -c 'curl -fsSL --connect-timeout 20 --max-time 600 https://tailscale.com/install.sh -o /tmp/tailscale-install.sh && sh /tmp/tailscale-install.sh && rm /tmp/tailscale-install.sh'
	if [ -n "${TS_AUTHKEY:-}" ]; then
		# The key goes in as a file: an argument would show in the process list.
		printf '%s' "$TS_AUTHKEY" >"$TMP/ts-authkey"
		pct push "$CTID" "$TMP/ts-authkey" /root/ts-authkey --perms 0600
		status=0
		pct exec "$CTID" -- tailscale up --auth-key=file:/root/ts-authkey --hostname "$HOSTNAME_" || status=$?
		pct exec "$CTID" -- rm -f /root/ts-authkey
		[ $status -eq 0 ] || exit "$status"
	else
		say "join the tailnet with: pct exec $CTID -- tailscale up --hostname $HOSTNAME_"
	fi
fi

if [ $MODE = create ]; then
	LISTEN=${DYNO_LISTEN:-:3966}
	OPEN=${LISTEN%:*}
	case $OPEN in
	"" | 0.0.0.0 | :: | "[::]") OPEN=$ADDR ;; # every address: the container's own
	esac
	say "done. dyno is container $CTID; open http://$OPEN:${LISTEN##*:}"
fi
say "upgrade later with: DYNO_CTID=$CTID sh proxmox-lxc.sh --upgrade"
