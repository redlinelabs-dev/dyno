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
		curl -fsSL --retry 3 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
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

# listen_addr prints DYNO_LISTEN as the service will see it.
listen_addr() {
	(
		DYNO_LISTEN=
		# shellcheck disable=SC1090
		set -a && . "$CONF" && set +a
		echo "${DYNO_LISTEN:-:3966}"
	)
}

install_dyno() {
	TMP=$(mktemp -d)
	trap 'rm -rf "$TMP"' EXIT
	get_binary
	chmod 0755 "$TMP/dyno"
	version=$("$TMP/dyno" -version 2>/dev/null) || die "the binary doesn't run on this machine"
	old=
	[ -f "$BIN" ] && old=$("$BIN" -version 2>/dev/null || echo "an unknown version")
	mkdir -p "$(dirname "$BIN")"
	cp "$TMP/dyno" "$BIN.new" && mv -f "$BIN.new" "$BIN"
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
	host=$(hostname -I 2>/dev/null | cut -d' ' -f1)
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
		# DynamicUser keeps the real directory under /var/lib/private.
		rm -rf "$ROOT/var/lib/private/dyno" "$ROOT/var/lib/dyno" "$CONF_DIR"
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
