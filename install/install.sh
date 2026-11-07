#!/bin/sh
# Installs or upgrades dyno as a systemd service on Debian, Ubuntu or any
# systemd Linux on amd64 or arm64 (ADR-0024). Run it again to upgrade.
#
#   curl -fsSLO https://github.com/redlinelabs-dev/dyno/releases/latest/download/install.sh
#   sudo sh install.sh                 install or upgrade to the latest release
#   sudo sh install.sh --uninstall     remove dyno, keep its data and settings
#   sudo sh install.sh --purge         remove dyno and its data and settings
#
# Environment:
#   DYNO_VERSION         release tag to install, e.g. v0.1.0 (default: latest)
#   DYNO_BINARY          install this local binary instead of downloading one
#   DYNO_LISTEN          written to /etc/dyno/dyno.env on first install
#   DYNO_METRICS_LISTEN  written to /etc/dyno/dyno.env on first install
#
# It's POSIX sh rather than Bun, the repo's usual script language, because a
# fresh host won't have Bun (ADR-0024).

set -eu
# The service runs as a throwaway user, so whatever umask root has, the binary
# has to stay runnable by others and nothing should come out writable by them.
umask 022

REPO=redlinelabs-dev/dyno
VERSION=${DYNO_VERSION:-latest}
# For tests: DYNO_BASE_URL replaces the GitHub release URL, DYNO_ROOT installs
# under a directory instead of /, and SYSTEMCTL replaces systemctl.
BASE_URL=${DYNO_BASE_URL:-}
ROOT=${DYNO_ROOT:-}
SYSTEMCTL=${SYSTEMCTL:-systemctl}

BIN=$ROOT/usr/local/bin/dyno
UNIT=$ROOT/etc/systemd/system/dyno.service
CONF_DIR=$ROOT/etc/dyno
CONF=$CONF_DIR/dyno.env

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
# The value goes into dyno.env, which systemd reads line by line, so nothing else
# may get in, and dyno refuses anything else at startup.
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

usage() {
	sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
	exit "${1:-0}"
}

preflight() {
	[ "$(uname -s)" = Linux ] || die "dyno runs on Linux; this is $(uname -s)"
	if [ -z "$ROOT" ]; then
		[ "$(id -u)" -eq 0 ] || die "run as root: sudo sh $0 $*"
		[ -d /run/systemd/system ] || die "systemd isn't running here; use the container image instead (docs/install/docker.md)"
	fi
}

arch() {
	case $(uname -m) in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) die "no dyno build for $(uname -m); builds exist for amd64 and arm64" ;;
	esac
}

fetch() { # url file
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 --connect-timeout 20 --max-time 600 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -T 30 -t 3 -O "$2" "$1"
	else
		die "needs curl or wget: apt-get install -y curl"
	fi || die "download failed: $1"
}

# get_binary leaves a verified binary at $TMP/dyno.
get_binary() {
	if [ -n "${DYNO_BINARY:-}" ]; then
		[ -f "$DYNO_BINARY" ] || die "DYNO_BINARY: no file at $DYNO_BINARY"
		say "using local binary $DYNO_BINARY (no checksum to check against)"
		cp "$DYNO_BINARY" "$TMP/dyno"
		return
	fi
	name=dyno-linux-$(arch)
	if [ -z "$BASE_URL" ]; then
		if [ "$VERSION" = latest ]; then
			BASE_URL=https://github.com/$REPO/releases/latest/download
		else
			BASE_URL=https://github.com/$REPO/releases/download/$VERSION
		fi
	fi
	say "downloading $name ($VERSION)"
	fetch "$BASE_URL/$name" "$TMP/$name"
	fetch "$BASE_URL/SHA256SUMS" "$TMP/SHA256SUMS"
	grep " $name\$" "$TMP/SHA256SUMS" >"$TMP/sum" || die "SHA256SUMS has no entry for $name"
	(cd "$TMP" && sha256sum -c --status sum) || die "checksum mismatch for $name; nothing was installed"
	mv "$TMP/$name" "$TMP/dyno"
}

write_unit() {
	mkdir -p "$(dirname "$UNIT")"
	cat >"$UNIT" <<'EOF'
# Written by dyno's install.sh; re-running it replaces this file. Put your
# settings in /etc/dyno/dyno.env, which upgrades keep.
[Unit]
Description=dyno network speed test
Documentation=https://github.com/redlinelabs-dev/dyno
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/local/bin/dyno
Environment=DYNO_DATA=/var/lib/dyno
# Keeps a burst from leaving the heap high (ADR-0006).
Environment=GOMEMLIMIT=64MiB
EnvironmentFile=-/etc/dyno/dyno.env
DynamicUser=yes
StateDirectory=dyno
Restart=on-failure
RestartSec=2s
LimitNOFILE=65536

# dyno needs the network, its data directory and nothing else.
CapabilityBoundingSet=
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged
SystemCallErrorNumber=EPERM
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
}

write_conf() {
	if [ -f "$CONF" ]; then
		say "keeping your settings in /etc/dyno/dyno.env"
		if [ -n "${DYNO_LISTEN:-}${DYNO_METRICS_LISTEN:-}" ]; then
			say "DYNO_LISTEN and DYNO_METRICS_LISTEN apply only on the first install; to change them, edit /etc/dyno/dyno.env and run: systemctl restart dyno"
		fi
		return
	fi
	mkdir -p "$CONF_DIR"
	{
		echo "# dyno settings. Restart after editing: systemctl restart dyno"
		echo "# Address and port for the test page and API (default :3966)."
		if [ -n "${DYNO_LISTEN:-}" ]; then echo "DYNO_LISTEN=$DYNO_LISTEN"; else echo "#DYNO_LISTEN=:3966"; fi
		echo "# Prometheus /metrics on its own address; off when unset (ADR-0017)."
		if [ -n "${DYNO_METRICS_LISTEN:-}" ]; then echo "DYNO_METRICS_LISTEN=$DYNO_METRICS_LISTEN"; else echo "#DYNO_METRICS_LISTEN=127.0.0.1:3967"; fi
	} >"$CONF"
}

# listen_addr prints DYNO_LISTEN as the service will see it. It reads dyno.env
# as text, the way systemd does (blanks around the = included): sourcing it
# would run whatever the file holds, as root.
listen_addr() {
	addr=$(sed -n 's/^[[:space:]]*DYNO_LISTEN[[:space:]]*=[[:space:]]*//p' "$CONF" | tail -n 1 | sed 's/[[:space:]]*$//')
	case $addr in
	\"*\" | \'*\') addr=${addr#?}; addr=${addr%?} ;;
	esac
	echo "${addr:-:3966}"
}

install_dyno() {
	check_listen DYNO_LISTEN "${DYNO_LISTEN:-}"
	check_listen DYNO_METRICS_LISTEN "${DYNO_METRICS_LISTEN:-}"
	TMP=$(mktemp -d)
	# Whichever way the run ends, no temp folder and no half-written dyno.new stay.
	trap 'rm -rf "$TMP"; rm -f "$BIN.new"' EXIT
	trap 'exit 1' INT TERM HUP # dash skips the EXIT trap when a signal kills it
	get_binary
	mkdir -p "$(dirname "$BIN")"
	# The new binary is staged and tried in the install folder: a hardened host
	# mounts /tmp noexec, and the final mv then stays on one filesystem.
	cp "$TMP/dyno" "$BIN.new" && chmod 0755 "$BIN.new" || die "couldn't write $BIN.new; nothing was changed"
	version=$("$BIN.new" -version 2>/dev/null) || die "the binary doesn't run on this machine"
	old=
	[ -f "$BIN" ] && old=$("$BIN" -version 2>/dev/null || echo "an unknown version")
	mv -f "$BIN.new" "$BIN" || die "couldn't replace $BIN; nothing was changed"
	write_unit
	write_conf
	$SYSTEMCTL daemon-reload
	$SYSTEMCTL enable --quiet dyno
	$SYSTEMCTL restart dyno
	if [ -z "$old" ]; then
		say "installed $version"
	elif [ "$old" = "$version" ]; then
		say "reinstalled $version"
	else
		say "upgraded $old to $version"
	fi

	listen=$(listen_addr)
	if [ -z "$ROOT" ]; then
		i=0
		until DYNO_LISTEN=$listen "$BIN" -healthcheck 2>/dev/null; do
			i=$((i + 1))
			[ $i -lt 10 ] || die "dyno didn't answer within 10 s; see: journalctl -u dyno"
			sleep 1
		done
	fi
	port=${listen##*:}
	host=${listen%:*}
	case $host in
	"" | 0.0.0.0 | :: | "[::]") host=$(link_host "$(hostname -I 2>/dev/null)") ;; # every address: use this host's own
	esac
	say "running. Open http://${host:-<this-host>}:$port"
	say "settings: /etc/dyno/dyno.env   data: /var/lib/dyno   logs: journalctl -u dyno"
}

uninstall_dyno() {
	if [ -f "$UNIT" ]; then
		$SYSTEMCTL disable --now --quiet dyno || true
		rm -f "$UNIT"
		$SYSTEMCTL daemon-reload
	fi
	rm -f "$BIN"
	if [ "$1" = purge ]; then
		# DynamicUser keeps the real directory under /var/lib/private, and
		# systemctl edit keeps its overrides in dyno.service.d.
		rm -rf "$ROOT/var/lib/private/dyno" "$ROOT/var/lib/dyno" "$CONF_DIR" "$UNIT.d"
		say "removed dyno, its test history and its settings"
	else
		say "removed dyno; kept its test history (/var/lib/dyno) and settings (/etc/dyno)"
	fi
}

case ${1:-} in
"") preflight "$@" && install_dyno ;;
--uninstall) preflight "$@" && uninstall_dyno keep ;;
--purge) preflight "$@" && uninstall_dyno purge ;;
-h | --help) usage 0 ;;
*) printf 'dyno: unknown option %s\n\n' "$1" >&2 && usage 1 ;;
esac
