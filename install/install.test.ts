import { afterAll, expect, setDefaultTimeout, test } from "bun:test";
import {
  appendFileSync,
  chmodSync,
  copyFileSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  readlinkSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join, relative } from "node:path";

// Each test runs the real script under dash with stubs for everything that would touch the host.
// Test names start with their case id from install/cases.md.
setDefaultTimeout(30_000);
// For a test that runs a script many times in one body (the R3, R12, R16, R18 and R19 loops), so that a slow machine
// doesn't push one past the default.
const MANY_RUNS_MS = 120_000;

// dash is the sh of Debian and Proxmox, where the scripts run. The signal tests (R7, R10, R15, R20) need it: when
// a signal kills dash it skips the EXIT trap, so only there does a script without `trap 'exit 1' INT TERM HUP PIPE`
// leave its temp folder behind. bash and busybox run the EXIT trap, so under them those tests pass without the trap line.
const dash = Bun.which("dash");
if (!dash) {
  throw new Error(
    "dash isn't on PATH. The installer tests run the scripts under dash, the sh of Debian and Proxmox (apt-get install dash); under another shell the signal tests can't fail.",
  );
}
const SH = dash;
const INSTALL = join(import.meta.dir, "install.sh");
const PROXMOX = join(import.meta.dir, "proxmox-lxc.sh");
const DOCS = join(import.meta.dir, "..", "docs", "install");
const REAL_CURL = Bun.which("curl");
const VERSION = "v0.1.0-test";
const RELEASES = "https://github.com/redlinelabs-dev/dyno/releases";

// The only real tools a script gets. No curl, wget, systemctl or Proxmox commands: tests add stubs. Its sh is dash too.
const TOOLS = mkdtempSync(join(tmpdir(), "dyno-tools-"));
symlinkSync(SH, join(TOOLS, "sh"));
for (const name of [
  "cat", "cp", "mv", "rm", "mkdir", "mktemp", "chmod", "grep", "sed", "awk", "sort", "tail", "cut",
  "dirname", "basename", "sha256sum", "printf", "env", "tr", "head", "install", "ln", "ls", "wc", "touch",
  "rmdir", "readlink", "realpath", "tee", "uniq", "xargs", "find", "stat", "date", "cmp", "diff", "expr",
]) {
  const real = Bun.which(name);
  if (real) symlinkSync(real, join(TOOLS, name));
}
afterAll(() => rmSync(TOOLS, { recursive: true, force: true }));

// makeCtx adds each test's folder here, and they all go once the file is done. Bun runs a file's tests one after
// another, so an afterEach would work as well: this is only a choice of when.
const dirs: string[] = [];
afterAll(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

type Env = Record<string, string | undefined>;
type Result = { code: number; stdout: string; stderr: string; out: string };

// A script that hangs (a loop around the stubbed sleep spins) is killed after this long, so it can't outlive its test.
const STUCK_MS = 20_000;

// umask, when given, is what the script is started under.
async function run(script: string, args: string[], env: Env, cwd: string, umask?: string): Promise<Result> {
  const clean = Object.fromEntries(Object.entries(env).filter(([, v]) => v !== undefined)) as Record<string, string>;
  const argv = umask ? [SH, "-c", `umask ${umask}; exec "$@"`, "sh", SH, script, ...args] : [SH, script, ...args];
  const proc = Bun.spawn(argv, {
    env: clean,
    cwd,
    stdin: "ignore",
    stdout: "pipe",
    stderr: "pipe",
    timeout: STUCK_MS,
    killSignal: "SIGKILL",
  });
  const [stdout, stderr, code] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ]);
  if (proc.signalCode === "SIGKILL") {
    throw new Error(`${basename(script)} was still running after ${STUCK_MS / 1000} s and was killed. Its last output:\n${(stdout + stderr).slice(-2000)}`);
  }
  return { code, stdout, stderr, out: stdout + stderr };
}

const readText = (path: string) => readFileSync(path, "utf8");
const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

function exists(path: string): boolean {
  try {
    lstatSync(path); // lstat, so a dangling symlink still counts
    return true;
  } catch {
    return false;
  }
}

function put(path: string, content: string, mode = 0o644) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content);
  chmodSync(path, mode);
}

// Every file under dir with its mode, so "untouched" and "nothing installed" are one comparison. With withDirs every
// folder is listed too (name and a trailing slash), so an empty one made before a failure shows. A run that stops
// after making usr/local/bin ahead of its copy (R10) leaves one legitimately, so only some tests ask for it.
function snapshot(dir: string, withDirs = false): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (d: string) => {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const f = join(d, e.name);
      if (e.isDirectory()) {
        if (withDirs) out[`${relative(dir, f)}/`] = "dir";
        walk(f);
      } else if (e.isSymbolicLink()) out[relative(dir, f)] = `-> ${readlinkSync(f)}`;
      else out[relative(dir, f)] = `${readText(f)} [${(statSync(f).mode & 0o777).toString(8)}]`;
    }
  };
  if (exists(dir)) walk(dir);
  return out;
}

function succeeds(r: Result) {
  if (r.code !== 0) throw new Error(`expected exit 0, got ${r.code}:\n${r.out}`);
}

function fails(r: Result) {
  if (r.code === 0) throw new Error(`expected a non-zero exit, got 0:\n${r.out}`);
}

function expectInOrder(lines: string[], patterns: RegExp[]) {
  let from = 0;
  for (const re of patterns) {
    const i = lines.findIndex((l, n) => n >= from && re.test(l));
    if (i < 0) throw new Error(`${re} not found after line ${from} in:\n${lines.join("\n")}`);
    from = i + 1;
  }
}

const sha256 = (content: string) => new Bun.CryptoHasher("sha256").update(content).digest("hex");

// The fake dyno: a shell script that answers -version and exits 0 for everything else.
const fakeDyno = (version: string, arch: string) =>
  `#!/bin/sh\n# dyno-fake ${arch} ${version}\n[ "$1" = -version ] && echo ${version}\nexit 0\n`;
const BROKEN_DYNO = "#!/bin/sh\n# dyno-fake broken\necho 'cannot execute here' >&2\nexit 1\n";

type ReleaseOpts = {
  version?: string;
  binary?: string; // content for both binaries
  badSum?: string[]; // names whose SHA256SUMS line is wrong
  noSum?: string[]; // names left out of SHA256SUMS
  missing?: string[]; // files left out of the folder (their sums stay)
};

// A release folder as `make release` writes it. The copy of install.sh carries a marker so a test can
// tell it from the one beside the script.
function makeRelease(dir: string, o: ReleaseOpts = {}) {
  const version = o.version ?? VERSION;
  const files: Record<string, string> = {
    "dyno-linux-amd64": o.binary ?? fakeDyno(version, "amd64"),
    "dyno-linux-arm64": o.binary ?? fakeDyno(version, "arm64"),
    "install.sh": `${readText(INSTALL)}\n# release-marker\n`,
    "proxmox-lxc.sh": readText(PROXMOX),
  };
  let sums = "";
  for (const [name, content] of Object.entries(files)) {
    put(join(dir, name), content, 0o755);
    if (o.noSum?.includes(name)) continue;
    sums += `${o.badSum?.includes(name) ? "0".repeat(64) : sha256(content)}  ${name}\n`;
  }
  put(join(dir, "SHA256SUMS"), sums);
  for (const name of o.missing ?? []) rmSync(join(dir, name), { force: true });
}

// Stub bodies. @TOKENS@ are filled in per test; every stub logs one line per call.
const systemctlStub = (failOn = "") =>
  `echo "systemctl $*" >> '@CALLS@'\n${failOn ? `[ "$1" = ${failOn} ] && exit 1\n` : ""}exit 0`;

// A systemctl that also logs, on its own line after each call, whether the unit file is still there at that moment.
const unitStateStub = (unit: string) =>
  `echo "systemctl $*" >> '@CALLS@'
if [ -e '${unit}' ]; then echo "[unit file there]" >> '@CALLS@'; else echo "[unit file gone]" >> '@CALLS@'; fi
exit 0`;

const idStub = (uid: number) =>
  `case "$1" in
  -u) echo ${uid};;
  -un) echo ${uid === 0 ? "root" : "jimmy"};;
  *) echo "uid=${uid}(x) gid=${uid}(x) groups=${uid}(x)";;
esac`;

const unameStub = (sys: string, arch: string) =>
  `case "$1" in
  -m) echo ${arch};;
  -s|"") echo ${sys};;
  -sm|-ms) echo "${sys} ${arch}";;
  -r) echo 6.1.0-test;;
  -n) echo testhost;;
  -o) echo GNU/Linux;;
  *) echo "${sys} testhost 6.1.0-test #1 ${arch} GNU/Linux";;
esac`;

const HOSTNAME_STUB = `case "$1" in
  -I) echo "192.168.1.50 ";;
  -i) echo 192.168.1.50;;
  *) echo testhost;;
esac`;

const curlPassthrough = (real: string) => `echo "curl $*" >> '@CALLS@'\nexec '${real}' "$@"`;
const CURL_404 = `echo "curl $*" >> '@CALLS@'\necho "curl: (22) The requested URL returned error: 404" >&2\nexit 22`;
const WGET_FAIL = `echo "wget $*" >> '@CALLS@'\nexit 8`;

// Serve the release folder for any URL, by file name: lets a test see the URL the script asked for.
const CURL_MAP = `echo "curl $*" >> '@CALLS@'
out=''; url=''; remote=''
while [ $# -gt 0 ]; do
  case "$1" in
    --output) out=$2; shift;;
    --output=*) out=\${1#--output=};;
    --*) ;;
    -*o) out=$2; shift;;
    -*O) remote=1;;
    *://*) url=$1;;
  esac
  shift
done
name=\${url##*/}
src='@RELEASE@'/$name
case "$url" in file://*) src=\${url#file://};; esac
if [ ! -f "$src" ]; then echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; fi
[ -n "$remote" ] && out=$name
if [ -z "$out" ] || [ "$out" = - ]; then cat "$src"; else cp "$src" "$out"; fi`;

const WGET_MAP = `echo "wget $*" >> '@CALLS@'
out=''; url=''
while [ $# -gt 0 ]; do
  case "$1" in
    --output-document=*) out=\${1#--output-document=};;
    --output-document|-O) out=$2; shift;;
    -*O-) out=-;;
    -*O) out=$2; shift;;
    -O?*) out=\${1#-O};;
    -*) ;;
    *://*) url=$1;;
  esac
  shift
done
name=\${url##*/}
src='@RELEASE@'/$name
case "$url" in file://*) src=\${url#file://};; esac
if [ ! -f "$src" ]; then echo "wget: server returned error: 404 Not Found" >&2; exit 8; fi
[ -z "$out" ] && out=$name
if [ "$out" = - ]; then cat "$src"; else cp "$src" "$out"; fi`;

type Ctx = ReturnType<typeof makeCtx>;

// One temp dir per test: stubs, TMPDIR, HOME, the install root, the fake release and the call log.
function makeCtx() {
  const dir = mkdtempSync(join(tmpdir(), "dyno-inst-"));
  dirs.push(dir);
  const p = (...s: string[]) => join(dir, ...s);
  for (const d of ["stubs", "tmp", "home", "release", "cwd", "ctr", "lxc", "state", "started"]) mkdirSync(p(d));
  writeFileSync(p("calls.log"), "");
  writeFileSync(p("pushes.tsv"), "");

  const fill = (body: string) =>
    body
      .replaceAll("@CALLS@", p("calls.log"))
      .replaceAll("@PUSHES@", p("pushes.tsv"))
      .replaceAll("@RELEASE@", p("release"))
      .replaceAll("@STATE@", p("state"))
      .replaceAll("@CTR@", p("ctr"))
      .replaceAll("@LXC@", p("lxc"))
      .replaceAll("@STARTED@", p("started"));
  const stub = (name: string, body: string) => put(p("stubs", name), `#!/bin/sh\n${fill(body)}\n`, 0o755);
  const base = (): Env => ({ PATH: `${p("stubs")}:${TOOLS}`, HOME: p("home"), TMPDIR: p("tmp"), LC_ALL: "C" });

  stub("systemctl", systemctlStub());
  stub("id", idStub(0));
  stub("uname", unameStub("Linux", "x86_64"));
  stub("hostname", HOSTNAME_STUB);
  stub("sleep", "exit 0");
  stub("curl", REAL_CURL ? curlPassthrough(REAL_CURL) : CURL_MAP);

  return {
    dir,
    p,
    root: p("root"),
    tmp: p("tmp"),
    home: p("home"),
    release: p("release"),
    lxc: p("lxc"),
    stub,
    unstub: (name: string) => rmSync(p("stubs", name), { force: true }),
    calls: () => readText(p("calls.log")).split("\n").filter(Boolean),
    resetCalls: () => writeFileSync(p("calls.log"), ""),
    pushes: () =>
      readText(p("pushes.tsv"))
        .split("\n")
        .filter(Boolean)
        .map((line) => {
          const [id, src, dst, perms] = line.split("\t");
          const copy = join(p("ctr"), dst);
          return { id, src, dst, perms, content: readText(copy), mode: statSync(copy).mode & 0o777 };
        }),
    install: (args: string[] = [], extra: Env = {}, umask?: string) =>
      run(
        INSTALL,
        args,
        {
          ...base(),
          DYNO_BASE_URL: `file://${p("release")}`,
          DYNO_ROOT: p("root"),
          SYSTEMCTL: p("stubs", "systemctl"),
          ...extra,
        },
        p("cwd"),
        umask,
      ),
    proxmox: (args: string[] = [], extra: Env = {}, script = PROXMOX, umask?: string) =>
      run(
        script,
        args,
        { ...base(), DYNO_BASE_URL: `file://${p("release")}`, PVE_LXC_DIR: p("lxc"), ...extra },
        p("cwd"),
        umask,
      ),
  };
}

function installCtx(rel: ReleaseOpts = {}) {
  const c = makeCtx();
  makeRelease(c.release, rel);
  return c;
}

const systemctlCalls = (c: Ctx) => c.calls().filter((l) => l.startsWith("systemctl "));

function seedBinary(root: string, version: string) {
  put(join(root, "usr/local/bin/dyno"), fakeDyno(version, "amd64"), 0o755);
}

// What a finished install leaves behind, built by hand so the uninstall tests don't depend on install.
function seedInstall(root: string) {
  seedBinary(root, VERSION);
  put(join(root, "etc/systemd/system/dyno.service"), "[Service]\nExecStart=/usr/local/bin/dyno\n");
  put(join(root, "etc/dyno/dyno.env"), "DYNO_LISTEN=:8080\n");
  put(join(root, "var/lib/private/dyno/dyno.db"), "history");
  symlinkSync("private/dyno", join(root, "var/lib/dyno")); // what DynamicUser leaves
}

// ---- install.sh ----

test("I1 fresh install lays out the files, starts the service and prints the address", async () => {
  const c = installCtx();
  const r = await c.install();
  succeeds(r);

  const bin = join(c.root, "usr/local/bin/dyno");
  expect(readText(bin)).toBe(fakeDyno(VERSION, "amd64"));
  expect(statSync(bin).mode & 0o111).toBe(0o111); // the service runs as a throwaway user, so everyone needs x
  expect(exists(join(c.root, "etc/systemd/system/dyno.service"))).toBe(true);

  const env = readText(join(c.root, "etc/dyno/dyno.env"));
  expect(env).toMatch(/^#\s*DYNO_LISTEN=/m);
  expect(env).toMatch(/^#\s*DYNO_METRICS_LISTEN=/m);
  expect(env).not.toMatch(/^\s*DYNO_LISTEN=/m);
  expect(env).not.toMatch(/^\s*DYNO_METRICS_LISTEN=/m);

  expectInOrder(systemctlCalls(c), [
    /^systemctl daemon-reload$/,
    /^systemctl enable\b.*\bdyno\b/,
    /^systemctl restart\b.*\bdyno\b/,
  ]);
  expect(r.out).toMatch(/\binstalled v0\.1\.0-test/);
  expect(r.out).not.toMatch(/reinstalled|upgraded/);
  expect(r.out).toMatch(/http:\/\/\S+:3966(?!\d)/);
});

test("I2 DYNO_LISTEN and DYNO_METRICS_LISTEN are written on first install", async () => {
  const c = installCtx();
  const r = await c.install([], { DYNO_LISTEN: ":8080", DYNO_METRICS_LISTEN: "127.0.0.1:3967" });
  succeeds(r);
  const env = readText(join(c.root, "etc/dyno/dyno.env"));
  expect(env).toMatch(/^DYNO_LISTEN=["']?:8080["']?\s*$/m);
  expect(env).toMatch(/^DYNO_METRICS_LISTEN=["']?127\.0\.0\.1:3967["']?\s*$/m);
  expect(r.out).toMatch(/http:\/\/\S+:8080(?!\d)/);
  expect(r.out).not.toMatch(/:3966/);
});

test("I3 re-running with the same binary keeps the settings byte for byte", async () => {
  const c = installCtx();
  succeeds(await c.install());
  const envPath = join(c.root, "etc/dyno/dyno.env");
  const mine = "# my edits\nDYNO_LISTEN=:7000\n";
  writeFileSync(envPath, mine);

  const r = await c.install([], { DYNO_LISTEN: ":8080" });
  succeeds(r);
  expect(readText(envPath)).toBe(mine);
  expect(r.out).toMatch(/\b(keep|keeps|keeping|kept)\b/i);
  expect(r.out).toMatch(/\breinstalled v0\.1\.0-test/);
});

test("I4 re-running with a newer binary upgrades it and rewrites the unit", async () => {
  const c = installCtx();
  succeeds(await c.install());
  const envPath = join(c.root, "etc/dyno/dyno.env");
  const unitPath = join(c.root, "etc/systemd/system/dyno.service");
  const unit = readText(unitPath);
  const mine = "DYNO_LISTEN=:7000\n";
  writeFileSync(envPath, mine);
  appendFileSync(unitPath, "\n# hand edit\nEnvironment=EVIL=1\n");

  makeRelease(c.release, { version: "v0.2.0-test" });
  const r = await c.install();
  succeeds(r);
  expect(readText(join(c.root, "usr/local/bin/dyno"))).toBe(fakeDyno("v0.2.0-test", "amd64"));
  expect(r.out).toMatch(/upgraded v0\.1\.0-test to v0\.2\.0-test/);
  expect(readText(envPath)).toBe(mine);
  expect(readText(unitPath)).toBe(unit);
  expect(readText(unitPath)).not.toContain("EVIL");
});

test("I5 checksum mismatch installs nothing", async () => {
  const c = installCtx({ badSum: ["dyno-linux-amd64"] });
  seedBinary(c.root, "v0.0.9-old");
  const before = snapshot(c.root, true);
  const r = await c.install();
  fails(r);
  expect(r.out).toMatch(/checksum mismatch/i);
  expect(snapshot(c.root, true)).toEqual(before);
  expect(systemctlCalls(c)).toEqual([]);
});

test("I6 SHA256SUMS without a line for this arch installs nothing", async () => {
  const c = installCtx({ noSum: ["dyno-linux-amd64"] });
  const r = await c.install();
  fails(r);
  expect(r.out).toMatch(/SHA256SUMS/);
  expect(r.out).toMatch(/dyno-linux-amd64/);
  // Both words are in other lines too (the "downloading" line, a failed download of SHA256SUMS): this is the script's own.
  expect(r.stderr).toContain("SHA256SUMS has no entry for dyno-linux-amd64");
  expect(snapshot(c.root, true)).toEqual({});
  expect(systemctlCalls(c)).toEqual([]);
});

for (const row of [
  { name: "the binary is missing from the release", rel: { missing: ["dyno-linux-amd64"] }, curl404: false },
  { name: "SHA256SUMS is missing from the release", rel: { missing: ["SHA256SUMS"] }, curl404: false },
  { name: "the server answers 404", rel: {}, curl404: true },
]) {
  test(`I7 download fails when ${row.name}`, async () => {
    const c = installCtx(row.rel);
    if (row.curl404) c.stub("curl", CURL_404);
    const r = await c.install();
    fails(r);
    expect(r.out).toMatch(/download failed/i);
    expect(snapshot(c.root, true)).toEqual({});
    expect(systemctlCalls(c)).toEqual([]);
  });
}

test("I8 downloads from the latest release by default", async () => {
  const c = installCtx();
  c.stub("curl", CURL_MAP);
  const r = await c.install([], { DYNO_BASE_URL: undefined });
  succeeds(r);
  const curls = c.calls().filter((l) => l.startsWith("curl "));
  expect(curls.some((l) => l.includes(`${RELEASES}/latest/download/dyno-linux-amd64`))).toBe(true);
  expect(curls.some((l) => l.includes(`${RELEASES}/latest/download/SHA256SUMS`))).toBe(true);
});

test("I9 DYNO_VERSION picks that release's files", async () => {
  const c = installCtx({ version: "v0.1.0" });
  c.stub("curl", CURL_MAP);
  const r = await c.install([], { DYNO_BASE_URL: undefined, DYNO_VERSION: "v0.1.0" });
  succeeds(r);
  const curls = c.calls().filter((l) => l.startsWith("curl "));
  expect(curls.some((l) => l.includes(`${RELEASES}/download/v0.1.0/dyno-linux-amd64`))).toBe(true);
  expect(curls.some((l) => l.includes(`${RELEASES}/download/v0.1.0/SHA256SUMS`))).toBe(true);
  expect(curls.some((l) => l.includes("/latest/"))).toBe(false);
});

test("I10 downloads with wget when there is no curl", async () => {
  const c = installCtx();
  c.unstub("curl");
  c.stub("wget", WGET_MAP);
  const r = await c.install();
  succeeds(r);
  const calls = c.calls();
  expect(calls.some((l) => l.startsWith("wget ") && l.includes("dyno-linux-amd64"))).toBe(true);
  expect(calls.some((l) => l.startsWith("wget ") && l.includes("SHA256SUMS"))).toBe(true);
  expect(calls.some((l) => l.startsWith("curl "))).toBe(false);
  expect(readText(join(c.root, "usr/local/bin/dyno"))).toBe(fakeDyno(VERSION, "amd64"));
});

test("I11 neither curl nor wget fails and names curl", async () => {
  const c = installCtx();
  c.unstub("curl");
  const r = await c.install();
  fails(r);
  expect(r.out).toMatch(/curl/i);
  expect(snapshot(c.root, true)).toEqual({});
});

test("I12 DYNO_BINARY installs that file and downloads nothing", async () => {
  const c = installCtx();
  c.stub("wget", WGET_FAIL);
  const local = c.p("local", "dyno-custom");
  put(local, fakeDyno("v0.3.0-local", "amd64"), 0o755);
  const r = await c.install([], { DYNO_BINARY: local, DYNO_BASE_URL: "file:///nonexistent" });
  succeeds(r);
  expect(readText(join(c.root, "usr/local/bin/dyno"))).toBe(readText(local));
  expect(r.out).toMatch(/\binstalled v0\.3\.0-local/);
  expect(c.calls().filter((l) => /^(curl|wget)\b/.test(l))).toEqual([]);
});

test("I13 DYNO_BINARY pointing at nothing fails and installs nothing", async () => {
  const c = installCtx();
  const r = await c.install([], { DYNO_BINARY: c.p("local", "nope") });
  fails(r);
  expect(r.out).toMatch(/no (such )?file|does(n'?t| not) exist|not found/i);
  // That pattern also fits cp's "No such file or directory", which is what a script without its own check would say.
  expect(r.stderr).toContain(`DYNO_BINARY: no file at ${c.p("local", "nope")}`);
  expect(snapshot(c.root, true)).toEqual({});
  expect(systemctlCalls(c)).toEqual([]);
});

for (const via of ["release", "DYNO_BINARY"]) {
  test(`I14 a binary that can't run (${via}) is refused and the installed one stays`, async () => {
    const c = installCtx(via === "release" ? { binary: BROKEN_DYNO } : {});
    seedBinary(c.root, "v0.0.9-old");
    const before = snapshot(c.root);
    const broken = c.p("local", "dyno-broken");
    if (via === "DYNO_BINARY") put(broken, BROKEN_DYNO, 0o755);
    const r = await c.install([], via === "DYNO_BINARY" ? { DYNO_BINARY: broken } : {});
    fails(r);
    expect(r.out).toMatch(/(doesn'?t|does not|can'?t|cannot|won'?t|didn'?t|did not) run/i);
    expect(snapshot(c.root)).toEqual(before);
    expect(systemctlCalls(c)).toEqual([]);
  });
}

for (const [machine, arch, other] of [
  ["x86_64", "amd64", "arm64"],
  ["amd64", "amd64", "arm64"],
  ["aarch64", "arm64", "amd64"],
  ["arm64", "arm64", "amd64"],
]) {
  test(`I15 uname -m ${machine} installs dyno-linux-${arch}`, async () => {
    const c = installCtx();
    c.stub("uname", unameStub("Linux", machine));
    const r = await c.install();
    succeeds(r);
    expect(readText(join(c.root, "usr/local/bin/dyno"))).toContain(`dyno-fake ${arch} `);
    const curls = c.calls().filter((l) => l.startsWith("curl "));
    expect(curls.some((l) => l.includes(`dyno-linux-${arch}`))).toBe(true);
    expect(curls.some((l) => l.includes(`dyno-linux-${other}`))).toBe(false);
  });
}

test("I15 uname -m riscv64 fails and names the machine type", async () => {
  const c = installCtx();
  c.stub("uname", unameStub("Linux", "riscv64"));
  const r = await c.install();
  fails(r);
  expect(r.out).toContain("riscv64");
  expect(snapshot(c.root)).toEqual({});
  expect(systemctlCalls(c)).toEqual([]);
});

// I16 and I17 run without DYNO_ROOT, so a script that failed to stop would write to the real system when the tests
// run as root. DYNO_BASE_URL points at a folder that doesn't exist, so such a script dies at its first download
// instead, whatever the release folder (empty here) ever holds.
test("I16 a system that isn't Linux fails before anything is touched", async () => {
  const c = makeCtx();
  c.stub("uname", unameStub("Darwin", "arm64"));
  const r = await c.install([], { DYNO_ROOT: undefined, DYNO_BASE_URL: "file:///nonexistent" });
  fails(r);
  expect(r.out).toMatch(/linux/i);
  expect(c.calls()).toEqual([]);
  expect(readdirSync(c.tmp)).toEqual([]);
  expect(readdirSync(c.home)).toEqual([]);
});

test("I17 not root and no DYNO_ROOT says to run as root before anything is touched", async () => {
  const c = makeCtx();
  c.stub("id", idStub(1000));
  const r = await c.install([], { DYNO_ROOT: undefined, DYNO_BASE_URL: "file:///nonexistent" });
  fails(r);
  expect(r.out).toMatch(/root/i);
  expect(c.calls()).toEqual([]);
  expect(readdirSync(c.tmp)).toEqual([]);
  expect(readdirSync(c.home)).toEqual([]);
});

// Each scenario runs to its end and says how it ended (an early exit would leave nothing behind either, so the test
// checks the ending first); afterwards no dyno.new and an empty TMPDIR.
const finished: [string, () => Promise<{ c: Ctx; r: Result }>, (r: Result, c: Ctx) => void][] = [
  [
    "a fresh install",
    async () => {
      const c = installCtx();
      return { c, r: await c.install() };
    },
    (r) => succeeds(r),
  ],
  [
    "an upgrade",
    async () => {
      const c = installCtx();
      seedBinary(c.root, "v0.0.9-old");
      return { c, r: await c.install() };
    },
    (r) => succeeds(r),
  ],
  [
    "a checksum mismatch",
    async () => {
      const c = installCtx({ badSum: ["dyno-linux-amd64"] });
      seedBinary(c.root, "v0.0.9-old");
      return { c, r: await c.install() };
    },
    (r) => {
      fails(r);
      expect(r.stderr).toMatch(/checksum mismatch/);
    },
  ],
  [
    "a binary that can't run",
    async () => {
      const c = installCtx({ binary: BROKEN_DYNO });
      seedBinary(c.root, "v0.0.9-old");
      return { c, r: await c.install() };
    },
    (r) => {
      fails(r);
      expect(r.stderr).toMatch(/doesn't run/);
    },
  ],
  [
    "a failed download",
    async () => {
      const c = installCtx({ missing: ["dyno-linux-amd64"] });
      return { c, r: await c.install() };
    },
    (r) => {
      fails(r);
      expect(r.stderr).toMatch(/download failed/);
    },
  ],
  [
    "a failed restart",
    async () => {
      const c = installCtx();
      c.stub("systemctl", systemctlStub("restart"));
      return { c, r: await c.install() };
    },
    (r, c) => {
      fails(r);
      expect(systemctlCalls(c)).toContain("systemctl restart dyno");
    },
  ],
];
for (const [name, go, ended] of finished) {
  test(`I18 leaves no dyno.new and no temp directory after ${name}`, async () => {
    const { c, r } = await go();
    ended(r, c);
    const bindir = join(c.root, "usr/local/bin");
    expect((exists(bindir) ? readdirSync(bindir) : []).filter((f) => f.endsWith(".new"))).toEqual([]);
    expect(readdirSync(c.tmp)).toEqual([]);
  });
}

test("I19 the unit runs the binary with the settings the ADR lists", async () => {
  const c = installCtx();
  succeeds(await c.install());
  const unit = readText(join(c.root, "etc/systemd/system/dyno.service"));
  for (const re of [
    /^ExecStart=\/usr\/local\/bin\/dyno(\s|$)/m,
    /^DynamicUser=yes\s*$/m,
    /^StateDirectory=dyno\s*$/m,
    /^Environment=DYNO_DATA=\/var\/lib\/dyno\s*$/m,
    /^Environment=GOMEMLIMIT=64MiB\s*$/m,
    /^EnvironmentFile=-\/etc\/dyno\/dyno\.env\s*$/m,
    /^WantedBy=multi-user\.target\s*$/m,
  ]) {
    expect(unit).toMatch(re);
  }
  expect(unit).not.toContain(c.root); // DYNO_ROOT is a test detail, never part of the unit
});

test("I19 the unit carries the sandbox ADR-0024 lists", async () => {
  const c = installCtx();
  succeeds(await c.install());
  const unit = readText(join(c.root, "etc/systemd/system/dyno.service"));
  for (const re of [
    /^PrivateTmp=(yes|true)\s*$/m,
    /^PrivateDevices=(yes|true)\s*$/m,
    /^ProtectSystem=strict\s*$/m, // "a read-only system"
    /^SystemCallFilter=.*@system-service/m,
    /^SystemCallErrorNumber=EPERM\s*$/m,
    /^CapabilityBoundingSet=\s*$/m, // "no capabilities"
  ]) {
    expect(unit).toMatch(re);
  }
});

test("I20 a failing systemctl restart fails the install", async () => {
  const c = installCtx();
  c.stub("systemctl", systemctlStub("restart"));
  const r = await c.install();
  fails(r);
  expect(c.calls().at(-1)).toBe("systemctl restart dyno"); // it stopped at the restart, not somewhere earlier
  expect(r.out).not.toMatch(/\b(installed|reinstalled|upgraded) v\d/);
});

test("I21 --uninstall removes the service and binary and keeps settings and data", async () => {
  const c = makeCtx();
  seedInstall(c.root);
  c.stub("systemctl", unitStateStub(join(c.root, "etc/systemd/system/dyno.service")));
  const r = await c.install(["--uninstall"]);
  succeeds(r);

  expect(exists(join(c.root, "usr/local/bin/dyno"))).toBe(false);
  expect(exists(join(c.root, "etc/systemd/system/dyno.service"))).toBe(false);
  expect(readText(join(c.root, "etc/dyno/dyno.env"))).toBe("DYNO_LISTEN=:8080\n");
  expect(readText(join(c.root, "var/lib/private/dyno/dyno.db"))).toBe("history");
  expect(exists(join(c.root, "var/lib/dyno"))).toBe(true);
  expect(r.out).toMatch(/\bkept\b|\bkeep(s|ing)?\b|left in place/i);

  const sc = systemctlCalls(c);
  const disable = sc.findIndex((l) => /^systemctl disable\b.*\bdyno/.test(l));
  const stopped =
    sc.some((l) => /^systemctl stop\b.*\bdyno/.test(l)) || sc.some((l) => /^systemctl disable\b.*--now\b.*\bdyno/.test(l));
  expect(disable).toBeGreaterThanOrEqual(0);
  expect(stopped).toBe(true);
  expect(sc.indexOf("systemctl daemon-reload")).toBeGreaterThan(disable); // the unit is gone by then

  // The order of two calls doesn't say that: the stub also logged whether the unit file was there at each call.
  const calls = c.calls();
  const next = (re: RegExp) => calls[calls.findIndex((l) => re.test(l)) + 1];
  expect(next(/^systemctl disable\b.*\bdyno/)).toBe("[unit file there]");
  expect(next(/^systemctl daemon-reload$/)).toBe("[unit file gone]");
});

test("I22 --purge also removes the settings and the data", async () => {
  const c = makeCtx();
  seedInstall(c.root);
  const r = await c.install(["--purge"]);
  succeeds(r);

  expect(exists(join(c.root, "usr/local/bin/dyno"))).toBe(false);
  expect(exists(join(c.root, "etc/systemd/system/dyno.service"))).toBe(false);
  expect(exists(join(c.root, "var/lib/private/dyno"))).toBe(false);
  expect(exists(join(c.root, "var/lib/dyno"))).toBe(false);
  expect(exists(join(c.root, "etc/dyno"))).toBe(false);
  const sc = systemctlCalls(c);
  expect(sc.some((l) => /^systemctl disable\b.*\bdyno/.test(l))).toBe(true);
  expect(sc).toContain("systemctl daemon-reload");
});

test("I23 --uninstall when dyno was never installed is not an error", async () => {
  const c = makeCtx();
  mkdirSync(c.root);
  const r = await c.install(["--uninstall"]);
  succeeds(r);
  expect(r.stderr).not.toMatch(/error|fail/i);
});

test("I24 an unknown option exits 1 and names the option on stderr", async () => {
  const c = installCtx();
  const r = await c.install(["--bogus"]);
  expect(r.code).toBe(1);
  expect(r.stderr).toContain("--bogus");
});

for (const flag of ["--help", "-h"]) {
  test(`I24 ${flag} prints usage and exits 0`, async () => {
    const c = installCtx();
    const r = await c.install([flag]);
    succeeds(r);
    expect(r.out).toContain("--uninstall");
    expect(r.out).toContain("--purge");
    // The usage is a line range of the script's header: it has to reach the last setting and stop before the code.
    expect(r.out).toContain("DYNO_METRICS_LISTEN");
    expect(r.out).not.toContain("set -eu");
  });
}

// ---- proxmox-lxc.sh ----

type Row = [name: string, type: string, status: string];
type PveOpts = {
  nextId?: string;
  rootdir?: Row[]; // pvesm status -content rootdir
  vztmpl?: Row[]; // pvesm status -content vztmpl
  templates?: string[]; // pveam available --section system
  have?: string[]; // pveam list <storage>, for the first active vztmpl storage: pveam lists nothing for any other
  containers?: Record<string, "running" | "stopped">;
  tags?: string | null; // the tags line of those containers: "dyno" when unset, no tags line at all when null
  address?: string; // what the container answers to hostname -I
  failUpdate?: boolean; // pveam update fails
};

const TUN_ALLOW = /^lxc\.cgroup2?\.devices\.allow:\s*c 10:200 rwm\s*$/gm;
const TUN_MOUNT = /^lxc\.mount\.entry:\s*\/dev\/net\/tun\b.*$/gm;
const TEMPLATE_13 = "debian-13-standard_13.10-1_amd64.tar.zst";
const ADDRESS = "192.168.1.77";
// What pvesm lists for any call but the two the script must make: an active storage, so a script that asks the wrong
// question picks it, and the pct create line or the rootfs shows it.
const DECOY: Row[] = [["decoy", "dir", "active"]];

// Stand-ins for the Proxmox tools. pct keeps container state in files so status follows create/start,
// keeps the container's config as it was when it started, copies pushed files into a per-test container folder
// (which has a /root, as a real one does, and only the folders pct exec made: pct push doesn't make any), and only
// logs everything else. pvesm and pveam answer only what the script is meant to ask: pvesm status -content rootdir
// and -content vztmpl get their tables, any other pvesm call gets DECOY, and pveam list names the templates only for
// the storage that holds them, so a call with other arguments or for another storage changes what the test sees.
function pve(c: Ctx, o: PveOpts = {}) {
  const rootdir: Row[] = o.rootdir ?? [
    ["broken", "lvmthin", "disabled"],
    ["local-lvm", "lvmthin", "active"],
    ["local-zfs", "zfspool", "active"],
  ];
  const vztmpl: Row[] = o.vztmpl ?? [
    ["nfs-off", "nfs", "disabled"],
    ["local", "dir", "active"],
    ["other", "dir", "active"],
  ];
  const templates = o.templates ?? [
    "debian-12-standard_12.7-1_amd64.tar.zst",
    "debian-13-standard_13.2-1_amd64.tar.zst",
    TEMPLATE_13,
    "debian-13-standard_13.1-2_amd64.tar.zst",
    "ubuntu-24.04-standard_24.04-2_amd64.tar.zst",
    "alpine-3.22-default_20250617_amd64.tar.xz",
  ];
  const address = o.address ?? ADDRESS;
  const tagsLine = o.tags === null ? "" : `tags: ${o.tags ?? "dyno"}\n`;
  for (const [id, state] of Object.entries(o.containers ?? {})) {
    put(c.p("state", id), `${state}\n`);
    put(join(c.lxc, `${id}.conf`), `arch: amd64\n${tagsLine}`);
  }
  mkdirSync(c.p("ctr", "root"), { recursive: true });

  const table = (rows: Row[]) =>
    rows.map(([n, t, s]) => `    echo '${n.padEnd(16)} ${t.padEnd(8)} ${s.padEnd(10)} 1000 100 900 10.00%'`).join("\n");
  // The storage that holds the container templates, the one the script has to ask pveam list about.
  const templateStore = vztmpl.find(([, , status]) => status === "active")?.[0] ?? "";
  c.stub("pvesh", `echo "pvesh $*" >> '@CALLS@'\ncase "$*" in\n  *nextid*) echo ${o.nextId ?? "105"};;\n  *) exit 1;;\nesac`);
  c.stub(
    "pvesm",
    `echo "pvesm $*" >> '@CALLS@'
echo 'Name             Type     Status           Total            Used       Available        %'
case "$*" in
  "status -content rootdir")
${table(rootdir)}
    ;;
  "status -content vztmpl")
${table(vztmpl)}
    ;;
  *)
${table(DECOY)}
    ;;
esac`,
  );
  c.stub(
    "pveam",
    `echo "pveam $*" >> '@CALLS@'
case "$1" in
  update)
${o.failUpdate ? `    echo "update storage failed: 500 Internal Server Error" >&2; exit 255` : "    exit 0"}
    ;;
  available)
${templates.map((t) => `    echo 'system          ${t}'`).join("\n")}
    ;;
  list)
    echo 'NAME                                       SIZE'
${(o.have ?? []).map((t) => `    [ "$2" = '${templateStore}' ] && echo "$2:vztmpl/${t}    120.00MB"`).join("\n")}
    ;;
esac
exit 0`,
  );

  c.stub(
    "pct",
    `echo "pct $*" >> '@CALLS@'
cmd=$1
[ $# -gt 0 ] && shift
case "$cmd" in
  status)
    if [ -f '@STATE@'/"$1" ]; then echo "status: $(cat '@STATE@'/"$1")"; exit 0; fi
    echo "Configuration file 'nodes/pve/lxc/$1.conf' does not exist" >&2
    exit 2
    ;;
  config)
    [ -f '@LXC@'/"$1.conf" ] || exit 2
    cat '@LXC@'/"$1.conf"
    exit 0
    ;;
  create)
    if [ -f '@STATE@'/"$1" ]; then echo "CT $1 already exists on node 'pve'" >&2; exit 255; fi
    id=$1
    tags=''
    while [ $# -gt 0 ]; do
      case "$1" in --tags) tags=$2; shift;; esac
      shift
    done
    echo stopped > '@STATE@'/"$id"
    { echo "arch: amd64"; [ -z "$tags" ] || echo "tags: $tags"; } > '@LXC@'/"$id.conf"
    exit 0
    ;;
  start|reboot)
    [ -f '@LXC@'/"$1.conf" ] && cp '@LXC@'/"$1.conf" '@STARTED@'/"$1.conf"
    echo running > '@STATE@'/"$1"
    exit 0
    ;;
  stop|shutdown)
    echo stopped > '@STATE@'/"$1"
    exit 0
    ;;
  list)
    echo 'VMID       Status     Lock         Name'
    for f in '@STATE@'/*; do
      [ -f "$f" ] && printf '%-10s %-10s %-12s %s\\n' "$(basename "$f")" "$(cat "$f")" '' "ct-$(basename "$f")"
    done
    exit 0
    ;;
  push)
    [ $# -ge 3 ] || { echo "usage: pct push <vmid> <file> <destination>" >&2; exit 1; }
    id=$1; src=$2; dst=$3
    shift 3
    perms=0644
    while [ $# -gt 0 ]; do
      case "$1" in
        --perms) perms=$2; shift;;
        --perms=*) perms=\${1#--perms=};;
        --user|--group) shift;;
      esac
      shift
    done
    [ -f "$src" ] || { echo "no such file: $src" >&2; exit 1; }
    [ -d "@CTR@$(dirname "$dst")" ] || { echo "$dst: No such file or directory" >&2; exit 1; }
    cp "$src" "@CTR@$dst" || exit 1
    chmod "$perms" "@CTR@$dst"
    printf '%s\\t%s\\t%s\\t%s\\n' "$id" "$src" "$dst" "$perms" >> '@PUSHES@'
    exit 0
    ;;
  exec)
    shift
    [ "$1" = -- ] && shift
    case "$*" in
      "hostname -I") printf '%s \\n' '${address}' ;;
      *"hostname -I"*) printf '%s\\n' '${address}' ;;
    esac
    [ "$1" = mkdir ] && [ "$2" = -p ] && mkdir -p "@CTR@$3"
    exit 0
    ;;
esac
exit 0`,
  );
}

function pveCtx(o: PveOpts = {}, rel: ReleaseOpts = {}) {
  const c = installCtx(rel);
  pve(c, o);
  return c;
}

const pct = (calls: string[], sub: string) => calls.filter((l) => l.startsWith(`pct ${sub} `));
const opt = (line: string, name: string) => new RegExp(`--${name}[ =](\\S+)`).exec(line)?.[1];
const MUTATING = ["create", "start", "stop", "push", "exec", "set", "destroy", "reboot", "shutdown"];
const mutations = (calls: string[]) => calls.filter((l) => MUTATING.some((s) => l.startsWith(`pct ${s} `)));
const createLine = (c: Ctx) => pct(c.calls(), "create")[0] ?? "";
// Anything that reaches the network or Proxmox; a script that refuses its input must have made none.
const hostCalls = (c: Ctx) => c.calls().filter((l) => /^(curl|wget|pct|pveam|pvesm)\b/.test(l));

// The files an upgrade or install pushed: the fake binary and the release's install.sh.
function pushed(c: Ctx, installMarker = "# release-marker") {
  const pushes = c.pushes();
  const bin = pushes.find((x) => x.content.includes("# dyno-fake"));
  const inst = pushes.find((x) => x.content.includes(installMarker));
  if (!bin || !inst) throw new Error(`expected the binary and install.sh to be pushed, got:\n${JSON.stringify(pushes, null, 1)}`);
  return { bin, inst };
}

// install.sh ran inside container `id` with DYNO_BINARY set, then the push folder went.
function expectInstallRan(c: Ctx, id: string, installMarker = "# release-marker") {
  const { bin, inst } = pushed(c, installMarker);
  expect(bin.id).toBe(id);
  expect(inst.id).toBe(id);
  const calls = c.calls();
  const at = calls.findIndex((l) => l.startsWith(`pct exec ${id} `) && l.includes(basename(inst.dst)));
  const lastPush = calls.reduce((last, l, i) => (l.startsWith("pct push ") ? i : last), -1);
  if (at < 0) throw new Error(`install.sh was never run inside the container:\n${calls.join("\n")}`);
  expect(at).toBeGreaterThan(lastPush);
  // pct push makes no folders, so the push folder is made first (the stub's push fails without it)
  const made = calls.findIndex((l) => l === `pct exec ${id} -- mkdir -p ${dirname(bin.dst)}`);
  expect(made).toBeGreaterThanOrEqual(0);
  expect(made).toBeLessThan(calls.findIndex((l) => l.startsWith("pct push ")));
  expect(calls[at]).toMatch(new RegExp(`DYNO_BINARY=["']?${esc(bin.dst)}`));
  const rest = calls[at].slice(calls[at].lastIndexOf("install.sh")) + "\n" + calls.slice(at + 1).join("\n");
  expect(rest).toMatch(/\brm\b/);
  expect(rest).toContain(dirname(bin.dst));
  expect(rest).toContain(dirname(inst.dst));
  return { bin, inst, line: calls[at] };
}

test("P1 creates the container with every default and installs dyno in it", async () => {
  const c = pveCtx();
  const r = await c.proxmox();
  succeeds(r);

  const calls = c.calls();
  expect(calls.some((l) => l.startsWith("pvesh get /cluster/nextid"))).toBe(true);
  const create = pct(calls, "create");
  expect(create).toHaveLength(1);
  const line = create[0];
  // 13.10-1 is newer than 13.2-1 only by version sort; the first active vztmpl storage is "local"
  expect(line).toMatch(new RegExp(`^pct create 105 local:vztmpl/${esc(TEMPLATE_13)}( |$)`));
  expect(opt(line, "hostname")).toBe("dyno");
  expect(opt(line, "cores")).toBe("1");
  expect(opt(line, "memory")).toBe("256");
  expect(opt(line, "rootfs")).toBe("local-lvm:2"); // first active rootdir storage, size in GiB
  expect(opt(line, "unprivileged")).toBe("1");
  expect(opt(line, "features")).toBe("nesting=1");
  expect(opt(line, "onboot")).toBe("1");
  expect(opt(line, "net0")).toBe("name=eth0,bridge=vmbr0,ip=dhcp");

  // Both storage questions were asked as Proxmox wants them: for any other pvesm call the stub lists a decoy storage,
  // and it names the templates in pveam list only for the storage that holds them.
  expect(calls.filter((l) => l.startsWith("pvesm ")).sort()).toEqual([
    "pvesm status -content rootdir",
    "pvesm status -content vztmpl",
  ]);
  expect(calls).toContain("pveam list local");
  expect(calls).toContain(`pveam download local ${TEMPLATE_13}`);
  expectInOrder(calls, [
    /^pveam download /,
    /^pct create 105 /,
    /^pct start 105\b/,
    /^pct push 105 /,
    /^pct push 105 /,
    /^pct exec 105 .*install\.sh/,
  ]);

  const { bin } = expectInstallRan(c, "105");
  expect(bin.mode).toBe(0o755);
  expect(r.out).toContain(`http://${ADDRESS}:3966`);
  expect(r.out).toMatch(/^(?=.*\b105\b)(?=.*--upgrade).*$/m);
});

test("P1 a template already downloaded is not fetched again", async () => {
  const c = pveCtx({ have: [TEMPLATE_13] });
  succeeds(await c.proxmox());
  // The stub lists the template only for the storage that holds templates, so the script has to have asked that one.
  expect(c.calls()).toContain("pveam list local");
  expect(c.calls().some((l) => l.startsWith("pveam download"))).toBe(false);
  expect(createLine(c)).toMatch(new RegExp(`^pct create 105 local:vztmpl/${esc(TEMPLATE_13)}( |$)`));
});

test("P2 the DYNO_* settings reach pct create", async () => {
  const c = pveCtx();
  const r = await c.proxmox([], {
    DYNO_CTID: "120",
    DYNO_HOSTNAME: "speedtest",
    DYNO_STORAGE: "local-zfs",
    DYNO_BRIDGE: "vmbr1",
    DYNO_CORES: "2",
    DYNO_MEMORY: "512",
    DYNO_DISK: "8",
  });
  succeeds(r);
  const line = createLine(c);
  expect(line).toMatch(/^pct create 120 /);
  expect(opt(line, "hostname")).toBe("speedtest");
  expect(opt(line, "rootfs")).toBe("local-zfs:8");
  expect(opt(line, "cores")).toBe("2");
  expect(opt(line, "memory")).toBe("512");
  expect(opt(line, "net0")).toBe("name=eth0,bridge=vmbr1,ip=dhcp");
  expectInstallRan(c, "120");
});

test("P2 a static address and gateway reach net0", async () => {
  const c = pveCtx({ address: "192.168.1.50" });
  const r = await c.proxmox([], { DYNO_IP: "192.168.1.50/24", DYNO_GW: "192.168.1.1" });
  succeeds(r);
  expect(opt(createLine(c), "net0")).toBe("name=eth0,bridge=vmbr0,ip=192.168.1.50/24,gw=192.168.1.1");
});

test("P3 a static DYNO_IP without DYNO_GW fails before any download or pct call", async () => {
  const c = pveCtx();
  const r = await c.proxmox([], { DYNO_IP: "192.168.1.50/24" });
  fails(r);
  expect(r.out).toMatch(/DYNO_GW/);
  expect(c.calls().filter((l) => /^(curl|wget|pct)\b/.test(l))).toEqual([]);
});

test("P4 a DYNO_CTID that exists fails and points at --upgrade", async () => {
  const c = pveCtx({ containers: { "105": "running" } });
  const r = await c.proxmox([], { DYNO_CTID: "105" });
  fails(r);
  expect(r.out).toMatch(/exist|already/i);
  expect(r.out).toContain("--upgrade");
  expect(pct(c.calls(), "create")).toEqual([]);
});

test("P5 --upgrade without DYNO_CTID fails and changes nothing", async () => {
  const c = pveCtx({ containers: { "105": "running" } });
  const r = await c.proxmox(["--upgrade"]);
  fails(r);
  expect(r.stderr).toContain("DYNO_CTID"); // it stopped for that, not for something earlier
  expect(c.calls()).toEqual([]); // no call at all: hostCalls leaves out pvesh, which only a nextid lookup would make
});

test("P6 --upgrade on a stopped container says it isn't running", async () => {
  const c = pveCtx({ containers: { "106": "stopped" } });
  const r = await c.proxmox(["--upgrade"], { DYNO_CTID: "106" });
  fails(r);
  expect(r.out).toMatch(/(isn'?t|is not|not) running|stopped/i);
  expect(mutations(c.calls())).toEqual([]);
});

test("P7 --upgrade on a running container pushes, installs and skips create, start, templates and Tailscale", async () => {
  const c = pveCtx({ containers: { "106": "running" } });
  const conf = join(c.lxc, "106.conf");
  const confBefore = readText(conf);
  const r = await c.proxmox(["--upgrade"], { DYNO_CTID: "106", DYNO_TAILSCALE: "1", TS_AUTHKEY: "tskey-auth-unused" });
  succeeds(r);

  const calls = c.calls();
  expect(pct(calls, "create")).toEqual([]);
  expect(pct(calls, "start")).toEqual([]);
  expect(calls.filter((l) => /^(pveam|pvesm)\b/.test(l))).toEqual([]);
  expect(calls.filter((l) => /tailscale/i.test(l))).toEqual([]);
  expect(readText(conf)).toBe(confBefore);
  expectInstallRan(c, "106");
});

for (const bad of ["dyno-linux-amd64", "install.sh"]) {
  test(`P8 a checksum mismatch on ${bad} stops before create, push and exec`, async () => {
    const c = pveCtx({}, { badSum: [bad] });
    const r = await c.proxmox();
    fails(r);
    expect(r.out).toMatch(/checksum mismatch/i);
    const calls = c.calls();
    expect(pct(calls, "create")).toEqual([]);
    expect(pct(calls, "push")).toEqual([]);
    expect(pct(calls, "exec")).toEqual([]);
  });
}

test("P9 DYNO_BINARY pushes that file and the install.sh beside the script, with no binary download", async () => {
  const c = pveCtx({}, { missing: ["dyno-linux-amd64", "dyno-linux-arm64"] });
  const host = c.p("host");
  mkdirSync(host);
  copyFileSync(PROXMOX, join(host, "proxmox-lxc.sh"));
  put(join(host, "install.sh"), `${readText(INSTALL)}\n# beside-marker\n`);
  const local = c.p("local", "dyno-custom");
  put(local, fakeDyno("v0.3.0-local", "amd64"), 0o755);

  const r = await c.proxmox([], { DYNO_BINARY: local }, join(host, "proxmox-lxc.sh"));
  succeeds(r);
  expect(c.calls().some((l) => /^(curl|wget)\b/.test(l) && l.includes("dyno-linux"))).toBe(false);
  const { bin, inst } = pushed(c, "# beside-marker");
  expect(bin.content).toBe(readText(local));
  expect(inst.content).not.toContain("# release-marker");
  expect(bin.mode).toBe(0o755);
  expectInstallRan(c, "105", "# beside-marker");
});

test("P10 falls back to the newest Debian 12 template", async () => {
  const c = pveCtx({
    templates: [
      "debian-12-standard_12.2-1_amd64.tar.zst",
      "debian-12-standard_12.10-1_amd64.tar.zst",
      "debian-12-standard_12.7-1_amd64.tar.zst",
      "ubuntu-24.04-standard_24.04-2_amd64.tar.zst",
    ],
  });
  succeeds(await c.proxmox());
  expect(createLine(c)).toMatch(/^pct create 105 local:vztmpl\/debian-12-standard_12\.10-1_amd64\.tar\.zst( |$)/);
});

test("P10 with neither Debian 13 nor 12 it fails and names them", async () => {
  const c = pveCtx({ templates: ["ubuntu-24.04-standard_24.04-2_amd64.tar.zst", "alpine-3.22-default_20250617_amd64.tar.xz"] });
  const r = await c.proxmox();
  fails(r);
  expect(r.out).toMatch(/debian[ -]?(12|13)/i);
  expect(pct(c.calls(), "create")).toEqual([]);
});

test("P11 no active rootdir storage and no DYNO_STORAGE fails and names DYNO_STORAGE", async () => {
  const c = pveCtx({ rootdir: [["broken", "lvmthin", "disabled"], ["gone", "nfs", "unknown"]] });
  const r = await c.proxmox();
  fails(r);
  expect(r.out).toContain("DYNO_STORAGE");
  expect(pct(c.calls(), "create")).toEqual([]);
});

test("P12 DYNO_TAILSCALE=1 gives the container /dev/net/tun and installs Tailscale", async () => {
  const c = pveCtx();
  const r = await c.proxmox([], { DYNO_TAILSCALE: "1" });
  succeeds(r);
  const conf = readText(join(c.lxc, "105.conf"));
  expect(conf.match(TUN_ALLOW)).toHaveLength(1);
  expect(conf.match(TUN_MOUNT)).toHaveLength(1);
  const execs = pct(c.calls(), "exec").filter((l) => /tailscale/i.test(l));
  expect(execs.length).toBeGreaterThan(0);
  expect(execs.some((l) => /tailscale up/.test(l))).toBe(false); // without a key it only prints the command
  expect(r.out).toMatch(/tailscale up/);
  // The lines have to be in the config when the container starts, or it has no /dev/net/tun until its next restart
  // and tailscale up fails; the config after the run can't say that, the copy pct start made can.
  const atStart = readText(c.p("started", "105.conf"));
  expect(atStart.match(TUN_ALLOW)).toHaveLength(1);
  expect(atStart.match(TUN_MOUNT)).toHaveLength(1);
});

test("P12 TS_AUTHKEY joins the tailnet with the container's hostname", async () => {
  const c = pveCtx();
  const r = await c.proxmox([], { DYNO_TAILSCALE: "1", TS_AUTHKEY: "tskey-auth-abc123", DYNO_HOSTNAME: "tsbox" });
  succeeds(r);
  const up = pct(c.calls(), "exec").find((l) => /tailscale up/.test(l));
  expect(up).toBeDefined();
  expect(up).toMatch(/--auth-key[ =]["']?file:\/root\/ts-authkey/); // the key travels in that file (R17)
  expect(up).toMatch(/--hostname[ =]["']?tsbox/);
});

test("P13 a container that never gets an address fails after 30 tries", async () => {
  const c = pveCtx({ address: "" });
  const r = await c.proxmox();
  fails(r);
  expect(r.out).toContain("DYNO_BRIDGE");
  expect(r.out).toContain("DYNO_IP");
  expect(pct(c.calls(), "exec").filter((l) => l.includes("hostname -I"))).toHaveLength(30);
});

test("P14 DYNO_LISTEN and DYNO_METRICS_LISTEN go to the install inside and set the printed port", async () => {
  const c = pveCtx();
  const r = await c.proxmox([], { DYNO_LISTEN: ":8080", DYNO_METRICS_LISTEN: "127.0.0.1:3967" });
  succeeds(r);
  const { line } = expectInstallRan(c, "105");
  expect(line).toMatch(/DYNO_LISTEN=["']?:8080/);
  expect(line).toMatch(/DYNO_METRICS_LISTEN=["']?127\.0\.0\.1:3967/);
  expect(r.out).toMatch(new RegExp(`http://${esc(ADDRESS)}:8080(?!\\d)`));
  expect(r.out).not.toContain(":3966");
});

test("P15 not root fails without a pct call", async () => {
  const c = pveCtx();
  c.stub("id", idStub(1000));
  const r = await c.proxmox();
  fails(r);
  expect(r.stderr).toMatch(/run as root/);
  expect(c.calls().filter((l) => l.startsWith("pct "))).toEqual([]);
});

for (const missing of [["pct", "pveam"], ["pct"], ["pveam"]]) {
  test(`P16 without ${missing.join(" and ")} on PATH it fails and points at install.sh`, async () => {
    const c = pveCtx();
    for (const name of missing) c.unstub(name);
    const r = await c.proxmox();
    fails(r);
    expect(r.out).toContain("install.sh");
    expect(c.calls().filter((l) => l.startsWith("pct "))).toEqual([]);
    expect(c.calls()).toEqual([]); // the pct filter above can't match when pct itself is the missing one
  });
}

test("P17 an unknown option exits 1", async () => {
  const c = pveCtx();
  const r = await c.proxmox(["--bogus"]);
  expect(r.code).toBe(1);
});

test("P17 --help exits 0 and mentions --upgrade", async () => {
  const c = pveCtx();
  const r = await c.proxmox(["--help"]);
  succeeds(r);
  expect(r.out).toContain("--upgrade");
  // The usage is a line range of the script's header: it has to reach the last setting and stop before the code.
  expect(r.out).toContain("TS_AUTHKEY");
  expect(r.out).not.toContain("set -eu");
});

test("P18 an unsupported uname -m fails before any pct call", async () => {
  const c = pveCtx();
  c.stub("uname", unameStub("Linux", "riscv64"));
  const r = await c.proxmox();
  fails(r);
  expect(r.stderr).toContain("riscv64");
  expect(c.calls().filter((l) => l.startsWith("pct "))).toEqual([]);
});

// The twin of I15. Only what uname -m prints on Linux: x86_64 and aarch64, not the amd64 and arm64 install.sh also takes.
for (const [machine, arch, other] of [
  ["x86_64", "amd64", "arm64"],
  ["aarch64", "arm64", "amd64"],
]) {
  test(`P18 uname -m ${machine} downloads dyno-linux-${arch} and pushes that binary`, async () => {
    const c = pveCtx();
    c.stub("uname", unameStub("Linux", machine));
    succeeds(await c.proxmox());
    const curls = c.calls().filter((l) => l.startsWith("curl "));
    expect(curls.some((l) => l.includes(`dyno-linux-${arch}`))).toBe(true);
    expect(curls.some((l) => l.includes(`dyno-linux-${other}`))).toBe(false);
    expect(pushed(c).bin.content).toContain(`dyno-fake ${arch} `);
  });
}

// P19 and P20: the URLs proxmox-lxc.sh builds itself. Every other test here points it at a file:// folder, so a wrong
// repository or path would pass them all. This curl serves the release folder for any URL and the test reads the URLs.
const urls = (c: Ctx) =>
  c
    .calls()
    .filter((l) => l.startsWith("curl "))
    .map((l) => l.slice(l.lastIndexOf(" ") + 1)) // the URL is the last argument of every logged call
    .sort();

test("P19 downloads SHA256SUMS, the binary and install.sh from the latest release by default", async () => {
  const c = pveCtx();
  c.stub("curl", CURL_MAP);
  succeeds(await c.proxmox([], { DYNO_BASE_URL: undefined }));
  expect(urls(c)).toEqual(["SHA256SUMS", "dyno-linux-amd64", "install.sh"].map((f) => `${RELEASES}/latest/download/${f}`).sort());
});

test("P20 DYNO_VERSION picks that release's files", async () => {
  const c = pveCtx();
  c.stub("curl", CURL_MAP);
  succeeds(await c.proxmox([], { DYNO_BASE_URL: undefined, DYNO_VERSION: "v0.1.0" }));
  expect(urls(c)).toEqual(["SHA256SUMS", "dyno-linux-amd64", "install.sh"].map((f) => `${RELEASES}/download/v0.1.0/${f}`).sort());
});

test("P21 no active storage for container templates fails before any template or container work", async () => {
  const c = pveCtx({ vztmpl: [["nfs-off", "nfs", "disabled"], ["gone", "dir", "unknown"]] });
  const r = await c.proxmox();
  fails(r);
  expect(r.stderr).toBe("dyno: no active storage holds container templates\n");
  expect(c.calls().filter((l) => l.startsWith("pveam "))).toEqual([]);
  expect(mutations(c.calls())).toEqual([]);
});

test("P22 a failing pveam update uses the cached template list, says so and goes on", async () => {
  const c = pveCtx({ failUpdate: true });
  const r = await c.proxmox();
  succeeds(r);
  expect(r.stdout).toContain("dyno: couldn't refresh the template list; using the cached one");
  expect(c.calls()).toContain("pveam update");
  expect(createLine(c)).toMatch(new RegExp(`^pct create 105 local:vztmpl/${esc(TEMPLATE_13)}( |$)`));
});

// ---- docs agree with the scripts ----

// Every setting the docs name must be read (as $NAME or ${NAME...}) by the script they document it for;
// a mention in the script's header comment doesn't count.
const SETTING = /\b(?:DYNO_[A-Z0-9_]+|TS_AUTHKEY)\b/g;
for (const [doc, script] of [["debian.md", INSTALL], ["proxmox-lxc.md", PROXMOX]] as const) {
  test(`D1 every setting ${doc} names is read by ${basename(script)}`, () => {
    const named = [...new Set(readText(join(DOCS, doc)).match(SETTING) ?? [])];
    expect(named.length).toBeGreaterThan(1);
    const code = readText(script)
      .split("\n")
      .filter((l) => !/^\s*#/.test(l))
      .join("\n");
    expect(named.filter((v) => !new RegExp(`\\$\\{?${v}\\b`).test(code))).toEqual([]);
  });
}

test("D2 the defaults the Proxmox doc's table states are what P1 observes", async () => {
  const doc = readText(join(DOCS, "proxmox-lxc.md"));
  const cell = (label: string) => {
    const row = doc.split("\n").find((l) => l.startsWith(`| ${label} |`));
    if (!row) throw new Error(`proxmox-lxc.md has no "${label}" row`);
    return row.split("|")[2].trim();
  };
  const hostname = cell("Hostname").replaceAll("`", "");
  const size = /(\d+) cores?, (\d+) MiB, (\d+) GiB/.exec(cell("CPU, memory, disk"));
  const swap = /^(\d+) MiB$/.exec(cell("Swap"));
  const network = /`([^`]+)`, DHCP/.exec(cell("Network"));
  expect(size).not.toBeNull();
  expect(swap).not.toBeNull();
  expect(network).not.toBeNull();
  expect(cell("Unprivileged, nesting on, start at boot")).toBe("always");

  const c = pveCtx();
  succeeds(await c.proxmox());
  const line = createLine(c);
  expect(opt(line, "hostname")).toBe(hostname);
  expect(opt(line, "cores")).toBe(size![1]);
  expect(opt(line, "memory")).toBe(size![2]);
  expect(opt(line, "swap")).toBe(swap![1]);
  expect(opt(line, "rootfs")).toMatch(new RegExp(`:${size![3]}$`));
  expect(opt(line, "net0")).toBe(`name=eth0,bridge=${network![1]},ip=dhcp`);
  expect(opt(line, "unprivileged")).toBe("1");
  expect(opt(line, "features")).toBe("nesting=1");
  expect(opt(line, "onboot")).toBe("1");
});

// ---- regressions R1 to R9 (cases.md, "Added after black-box round 1") ----

// R1: every file proxmox-lxc.sh pushes needs a SHA256SUMS line of its own, or nothing is pushed.
const missingLines: [string, ReleaseOpts, string][] = [
  ["has no line for dyno-linux-amd64", { noSum: ["dyno-linux-amd64"] }, "dyno-linux-amd64"],
  ["has no line for install.sh", { noSum: ["install.sh"] }, "install.sh"],
];
for (const [name, rel, file] of missingLines) {
  test(`R1 a SHA256SUMS that ${name} stops before create, push and exec and names the file`, async () => {
    const c = pveCtx({}, rel);
    const r = await c.proxmox();
    fails(r);
    expect(r.stderr).toMatch(new RegExp(`SHA256SUMS.*${esc(file)}`));
    expect(mutations(c.calls())).toEqual([]);
  });
}

test("R1 an empty SHA256SUMS stops before create, push and exec", async () => {
  const c = pveCtx();
  put(join(c.release, "SHA256SUMS"), "");
  const r = await c.proxmox();
  fails(r);
  expect(r.stderr).toMatch(/SHA256SUMS/);
  expect(mutations(c.calls())).toEqual([]);
});

// R2: root's umask must not decide what is installed; the service runs as a throwaway user.
function modes(dir: string): Record<string, number> {
  const out: Record<string, number> = {};
  const walk = (d: string) => {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const f = join(d, e.name);
      out[relative(dir, f)] = lstatSync(f).mode & 0o7777;
      if (e.isDirectory()) walk(f);
    }
  };
  if (exists(dir)) walk(dir);
  return out;
}

for (const mask of ["077", "000"]) {
  test(`R2 install.sh started under umask ${mask} leaves a binary others can run and nothing others can write`, async () => {
    const c = installCtx();
    const system = ["usr", "usr/local", "usr/local/bin", "etc", "etc/systemd", "etc/systemd/system"]; // a real host has these
    for (const d of system) {
      mkdirSync(join(c.root, d), { recursive: true });
      chmodSync(join(c.root, d), 0o755);
    }
    succeeds(await c.install([], {}, mask));
    const m = modes(c.root);
    expect(m["usr/local/bin/dyno"] & 0o005).toBe(0o005);
    const loose = Object.entries(m)
      .filter(([f, mode]) => !system.includes(f) && (mode & 0o022) !== 0)
      .map(([f, mode]) => `${f} ${mode.toString(8)}`);
    expect(loose).toEqual([]);
  });

  test(`R2 proxmox-lxc.sh started under umask ${mask} runs what it starts under 022`, async () => {
    const c = pveCtx();
    c.stub("pvesh", `echo "umask $(umask)" >> '@CALLS@'\necho 105`);
    succeeds(await c.proxmox([], {}, PROXMOX, mask));
    const seen = c.calls().filter((l) => l.startsWith("umask "));
    expect(seen).toHaveLength(1);
    expect(seen[0]).toMatch(/^umask 0*22$/);
  });
}

// R3: DYNO_LISTEN and DYNO_METRICS_LISTEN end up in dyno.env, which systemd reads line by line.
const BAD_LISTEN = [
  "$(touch PWN)",
  "`touch PWN`",
  ":8080;touch PWN",
  ":8080&touch PWN",
  ":8080|touch PWN",
  ":8080\nDYNO_METRICS_LISTEN=0.0.0.0:9999",
  ":8080 touch PWN",
  "':8080'",
  "8080", // a port needs its colon
  "host",
  "host:",
];

// The values for which a script does not do what it promises with the setting `name`. Every value in `refuse` has to
// end the run with one line on stderr naming the setting, install.sh before it wrote or ran anything and proxmox-lxc.sh
// before any download or pct call. Every value in `accept` has to get the run through.
async function listenWrong(
  script: "install.sh" | "proxmox-lxc.sh",
  name: string,
  refuse: string[],
  accept: string[] = [],
): Promise<string[]> {
  const message = `dyno: ${name} must be a port or a host and port, like :8080 or 127.0.0.1:3967\n`;
  const wrong: string[] = [];
  for (const value of [...refuse, ...accept]) {
    const refused = refuse.includes(value);
    let right: boolean;
    if (script === "install.sh") {
      const c = installCtx();
      c.stub("touch", `echo "touch $*" >> '@CALLS@'`);
      const r = await c.install([], { [name]: value });
      const untouched = c.calls().length === 0 && Object.keys(snapshot(c.root, true)).length === 0; // folders too
      right = refused ? r.code !== 0 && r.stderr === message && untouched : r.code === 0;
    } else {
      const c = pveCtx();
      const r = await c.proxmox([], { [name]: value });
      right = refused ? r.code !== 0 && r.stderr === message && hostCalls(c).length === 0 : r.code === 0;
    }
    if (!right) wrong.push(`${refused ? "not refused" : "refused"}: ${JSON.stringify(value)}`);
  }
  return wrong;
}

for (const name of ["DYNO_LISTEN", "DYNO_METRICS_LISTEN"]) {
  test(`R3 install.sh refuses a bad ${name} by name before anything is touched or run`, async () => {
    expect(await listenWrong("install.sh", name, BAD_LISTEN)).toEqual([]);
  }, MANY_RUNS_MS);

  test(`R3 proxmox-lxc.sh refuses a bad ${name} by name before any download or pct call`, async () => {
    expect(await listenWrong("proxmox-lxc.sh", name, BAD_LISTEN)).toEqual([]);
  }, MANY_RUNS_MS);
}

for (const [value, port] of [
  [":8080", "8080"],
  ["0.0.0.0:3966", "3966"],
  ["[::1]:8080", "8080"],
  ["[::]:3999", "3999"],
  ["dyno.lan:80", "80"],
]) {
  test(`R3 install.sh accepts ${value}, writes it as given and prints its port`, async () => {
    const c = installCtx();
    const r = await c.install([], { DYNO_LISTEN: value, DYNO_METRICS_LISTEN: value });
    succeeds(r);
    const env = readText(join(c.root, "etc/dyno/dyno.env"));
    expect(env).toMatch(new RegExp(`^DYNO_LISTEN=["']?${esc(value)}["']?\\s*$`, "m"));
    expect(env).toMatch(new RegExp(`^DYNO_METRICS_LISTEN=["']?${esc(value)}["']?\\s*$`, "m"));
    expect(r.out).toMatch(new RegExp(`http://\\S+:${port}(?!\\d)`));
  });
}

// proxmox-lxc.sh passes both as empty strings on every create, so this is the normal Proxmox path: an empty value is
// not set, and the file gets the commented defaults, not a setting with nothing after the equals sign.
test("R3 a first install with DYNO_LISTEN and DYNO_METRICS_LISTEN empty writes both commented out and prints the default port", async () => {
  const c = installCtx();
  const r = await c.install([], { DYNO_LISTEN: "", DYNO_METRICS_LISTEN: "" });
  succeeds(r);
  const lines = readText(join(c.root, "etc/dyno/dyno.env")).split("\n");
  expect(lines).toContain("#DYNO_LISTEN=:3966");
  expect(lines).toContain("#DYNO_METRICS_LISTEN=127.0.0.1:3967");
  expect(lines.filter((l) => /^\s*DYNO_(METRICS_)?LISTEN\s*=/.test(l))).toEqual([]); // not even an empty one
  expect(r.out).toMatch(/http:\/\/\S+:3966$/m);
});

test("R3 proxmox-lxc.sh passes IPv6 listen addresses on and prints the port", async () => {
  const c = pveCtx();
  const r = await c.proxmox([], { DYNO_LISTEN: "[::]:3999", DYNO_METRICS_LISTEN: "[::1]:3967" });
  succeeds(r);
  const { line } = expectInstallRan(c, "105");
  expect(line).toContain("DYNO_LISTEN=[::]:3999");
  expect(line).toContain("DYNO_METRICS_LISTEN=[::1]:3967");
  expect(r.out).toContain(`http://${ADDRESS}:3999`);
});

test("R3 dyno.env is read as text and never run", async () => {
  const c = installCtx();
  c.stub("touch", `echo "touch $*" >> '@CALLS@'`);
  put(
    join(c.root, "etc/dyno/dyno.env"),
    '# mine\nDYNO_LISTEN=$(touch PWN)\nDYNO_METRICS_LISTEN=`touch PWN`\ntouch PWN\n; touch PWN\nDYNO_LISTEN=":7000"\n',
  );
  const r = await c.install();
  succeeds(r);
  expect(c.calls().filter((l) => l.startsWith("touch "))).toEqual([]);
  expect(r.out).toMatch(/http:\/\/\S+:7000(?=\s|$)/m);
});

const kept: [string, string][] = [
  ["DYNO_LISTEN=:7001", "7001"],
  ['DYNO_LISTEN=":7002"', "7002"],
  ["DYNO_LISTEN=':7003'", "7003"],
  ["  DYNO_LISTEN=[::1]:7004  ", "7004"],
  ["DYNO_LISTEN=:7005\nDYNO_LISTEN=:7006", "7006"], // the last one wins, as in systemd
  ["DYNO_LISTEN=:7007\n#DYNO_LISTEN=:7008", "7007"], // a comment is not a setting
  ["DYNO_LISTEN=:7009\nDYNO_LISTEN=", "3966"], // an empty value means the default
  ["DYNO_METRICS_LISTEN=127.0.0.1:7010", "3966"],
  ["# nothing set", "3966"],
];
for (const [lines, port] of kept) {
  test(`R3 the printed port follows ${JSON.stringify(lines)} in dyno.env`, async () => {
    const c = installCtx();
    put(join(c.root, "etc/dyno/dyno.env"), `${lines}\n`);
    const r = await c.install();
    succeeds(r);
    // The port must end the link: a quote left on it would still match a looser pattern.
    expect(r.out).toMatch(new RegExp(`http://\\S+:${port}(?=\\s|$)`, "m"));
  });
}

// R4: no usable container id, no container.
const badIds: [string, string][] = [
  ["fails", "echo 'cluster not ready' >&2\nexit 1"],
  ["prints nothing", "exit 0"],
  ["prints text", "echo 'no quorum'"],
  ["prints an id and then fails", "echo 106\nexit 1"],
];
for (const [name, body] of badIds) {
  test(`R4 pvesh get /cluster/nextid ${name}: nothing is created`, async () => {
    const c = pveCtx();
    c.stub("pvesh", body);
    const r = await c.proxmox();
    fails(r);
    expect(r.stderr).toContain("DYNO_CTID");
    expect(hostCalls(c)).toEqual([]);
  });
}

for (const [mode, id] of [
  ["create", "abc"],
  ["create", "10 5"],
  ["create", "105; reboot"],
  ["create", "-1"],
  ["upgrade", "abc"],
  ["upgrade", "1 2"],
]) {
  test(`R4 DYNO_CTID=${JSON.stringify(id)} is refused in ${mode} mode before any pct call`, async () => {
    const c = pveCtx({ containers: { "105": "running" } });
    const r = await c.proxmox(mode === "upgrade" ? ["--upgrade"] : [], { DYNO_CTID: id });
    fails(r);
    expect(r.stderr).toContain("DYNO_CTID");
    expect(hostCalls(c)).toEqual([]);
  });
}

// pct whose calls that match a glob (their arguments, joined into one string) run the shell that goes with it instead.
// Every other call goes to the usual stub, and a body can make that call itself with '@REAL@' "$@" (a push that lands its
// file and then fails). Call it once per test: it renames the stub it wraps.
function wrapPct(c: Ctx, arms: [glob: string, body: string][]) {
  const real = c.p("stubs", "pct-real");
  copyFileSync(c.p("stubs", "pct"), real);
  chmodSync(real, 0o755);
  const cases = arms.map(([glob, body]) => `  ${glob})\n${body.replaceAll("@REAL@", real)}\n    ;;`).join("\n");
  c.stub("pct", `case "$*" in\n${cases}\nesac\nexec '${real}' "$@"`);
}

// What such a call does when it fails, as an install that dies in the container does, or a start that Proxmox
// refuses: it is logged, says its message on stderr and exits with its status.
const failBody = (status: number, message: string) => `    echo "pct $*" >> '@CALLS@'\n    echo "${message}" >&2\n    exit ${status}`;

function failPct(c: Ctx, glob: string, status: number, message: string) {
  wrapPct(c, [[glob, failBody(status, message)]]);
}

// R5: the install inside the container fails.
for (const mode of ["create", "upgrade"]) {
  test(`R5 an install that fails in the container (${mode}) still removes the pushed files and exits with its status`, async () => {
    const id = mode === "upgrade" ? "106" : "105";
    const c = pveCtx({ containers: mode === "upgrade" ? { "106": "running" } : {} });
    failPct(c, "exec*install.sh*", 3, "boom from the install");
    const r = await c.proxmox(mode === "upgrade" ? ["--upgrade"] : [], mode === "upgrade" ? { DYNO_CTID: id } : {});
    expect(r.code).toBe(3);
    expect(r.stderr).toContain("boom from the install"); // the install's own message
    // and the script says which container it was, the status, and how to retry
    expect(r.stderr).toContain(
      `dyno: the install inside container ${id} failed (exit 3); its message is above. Fix that, then run: DYNO_CTID=${id} sh proxmox-lxc.sh --upgrade`,
    );
    const calls = c.calls();
    const ran = calls.findIndex((l) => l.startsWith(`pct exec ${id} `) && l.includes("install.sh"));
    expect(ran).toBeGreaterThanOrEqual(0);
    expect(calls.slice(ran + 1).some((l) => new RegExp(`^pct exec ${id} .*\\brm\\b.*/root/dyno-install`).test(l))).toBe(true);
  });
}

// R6: a stalled network must not hang a download for ever.
const limited = (l: string) => /--connect-timeout 20\b/.test(l) && /--max-time 600\b/.test(l);

test("R6 install.sh gives every curl download a connect and a total time limit", async () => {
  const c = installCtx();
  succeeds(await c.install());
  const curls = c.calls().filter((l) => l.startsWith("curl "));
  expect(curls.length).toBeGreaterThan(0);
  expect(curls.filter((l) => !limited(l))).toEqual([]);
});

test("R6 install.sh gives every wget download a timeout and a try limit", async () => {
  const c = installCtx();
  c.unstub("curl");
  c.stub("wget", WGET_MAP);
  succeeds(await c.install());
  const wgets = c.calls().filter((l) => l.startsWith("wget "));
  expect(wgets.length).toBeGreaterThan(0);
  expect(wgets.filter((l) => !/ -T 30\b/.test(l) || !/ -t 3\b/.test(l))).toEqual([]);
});

test("R6 proxmox-lxc.sh gives every curl download a time limit, the Tailscale installer's too", async () => {
  const c = pveCtx();
  succeeds(await c.proxmox([], { DYNO_TAILSCALE: "1" }));
  const curls = c.calls().filter((l) => l.startsWith("curl "));
  expect(curls.length).toBeGreaterThan(0);
  expect(curls.filter((l) => !limited(l))).toEqual([]);
  const tailscale = pct(c.calls(), "exec").filter((l) => l.includes("tailscale.com/install.sh"));
  expect(tailscale).toHaveLength(1);
  expect(limited(tailscale[0])).toBe(true);
});

// R7: Ctrl-C, a kill or a dropped ssh session during a download. dash skips the EXIT trap for a signal that
// kills it, so the temp directory used to stay. The stub signals the script it runs under, then exits 0 as if the
// download had worked: only the script's own `trap 'exit 1'` stops the run before its next download, so a test that sees
// exit 1 and one curl call proves the trap ran, where a stub that exits non-zero would end the run through die.
const curlSignals = (sig: string) => `echo "curl $*" >> '@CALLS@'\nkill -s ${sig} "$PPID"\nexit 0`;

for (const sig of ["INT", "TERM", "HUP", "PIPE"]) {
  for (const script of ["install.sh", "proxmox-lxc.sh"]) {
    test(`R7 ${script} stopped by SIG${sig} during a download removes its temp directory`, async () => {
      const c = script === "install.sh" ? installCtx() : pveCtx();
      c.stub("curl", curlSignals(sig));
      const r = await (script === "install.sh" ? c.install() : c.proxmox());
      expect(r.code).toBe(1); // the trap's exit, not the stub's: it exited 0
      // The download began, so the signal was sent, and it was the only one: a script that ignored the signal would
      // go on to its next download.
      expect(c.calls().filter((l) => l.startsWith("curl "))).toHaveLength(1);
      expect(readdirSync(c.tmp)).toEqual([]);
      expect(snapshot(c.root)).toEqual({});
      expect(mutations(c.calls())).toEqual([]);
    });
  }
}

// R7, SIGPIPE: an ssh session with no tty that drops sends no HUP; the script's next write takes SIGPIPE, which dash
// treats like any other killing signal. The stub sends it to the script, as the tests above do, from the calls that follow
// the temp folder: install.sh's first systemctl call and proxmox-lxc.sh's pct create (later calls: R20).
test("R7 proxmox-lxc.sh stopped by SIGPIPE during pct create removes its temp directory", async () => {
  const c = pveCtx();
  wrapPct(c, [["create*", signalBody("PIPE")]]);
  const r = await c.proxmox();
  expect(r.code).toBe(1);
  expect(c.calls()).toContain("signalled SIGPIPE"); // pct create ran and the signal was sent
  expect(readdirSync(c.tmp)).toEqual([]);
});

test("R7 install.sh stopped by SIGPIPE at its first systemctl call removes its temp directory", async () => {
  const c = installCtx();
  c.stub("systemctl", `echo "systemctl $*" >> '@CALLS@'\nkill -s PIPE "$PPID"\nexit 130`);
  const r = await c.install();
  expect(r.code).toBe(1);
  expect(c.calls().some((l) => l.startsWith("systemctl "))).toBe(true); // the install got that far, so the signal was sent
  expect(readdirSync(c.tmp)).toEqual([]);
});

// R8: the link has to work, so an IPv4 address beats an IPv6 one and a bare IPv6 address gets brackets.
const hostnameStub = (out: string) => `case "$1" in\n  -I) printf '%s\\n' '${out}';;\n  *) echo testhost;;\nesac`;

for (const [name, out, link] of [
  ["lists an IPv4 address first", "192.168.1.50 172.17.0.1 ", "http://192.168.1.50:3966"],
  ["lists an IPv6 address first", "fd00::5 192.168.1.50 ", "http://192.168.1.50:3966"],
  ["lists only IPv6 addresses", "fd00::5 fd00::6 ", "http://[fd00::5]:3966"],
  ["lists nothing", "", "http://<this-host>:3966"],
]) {
  test(`R8 install.sh prints ${link} when hostname -I ${name}`, async () => {
    const c = installCtx();
    c.stub("hostname", hostnameStub(out));
    const r = await c.install();
    succeeds(r);
    expect(r.out).toContain(link);
  });
}

test("R8 install.sh prints the placeholder when there is no hostname command", async () => {
  const c = installCtx();
  c.unstub("hostname");
  const r = await c.install();
  succeeds(r);
  expect(r.out).toContain("http://<this-host>:3966");
});

for (const [name, address, link] of [
  ["lists an IPv6 address first", "fd00::5 192.168.1.77", "http://192.168.1.77:3966"],
  ["lists only IPv6 addresses", "fd00::5 fd00::6", "http://[fd00::5]:3966"],
]) {
  test(`R8 proxmox-lxc.sh prints ${link} when the container ${name}`, async () => {
    const c = pveCtx({ address });
    const r = await c.proxmox();
    succeeds(r);
    expect(r.out).toContain(link);
  });
}

// R9: with DYNO_BINARY the binary is the owner's own and has no checksum. An install.sh that isn't beside the
// script is fetched from the release and runs as root in the container, so it has to match its SHA256SUMS line.
// The script is copied alone into a folder of its own, so nothing sits beside it unless a test puts it there.
function ownBuild(rel: ReleaseOpts = {}, beside?: string) {
  const c = pveCtx({}, rel);
  const host = c.p("host");
  mkdirSync(host);
  const script = join(host, "proxmox-lxc.sh");
  copyFileSync(PROXMOX, script);
  if (beside !== undefined) put(join(host, "install.sh"), beside);
  const local = c.p("local", "dyno-custom");
  put(local, fakeDyno("v0.3.0-local", "amd64"), 0o755);
  return { c, local, run: () => c.proxmox([], { DYNO_BINARY: local }, script) };
}

// The files curl was asked for, by name and sorted. The URL is the last argument of every logged call.
const downloads = (c: Ctx) =>
  c
    .calls()
    .filter((l) => l.startsWith("curl "))
    .map((l) => basename(l))
    .sort();

// Nothing was created, pushed or run, and no template was downloaded.
function expectNothingDone(c: Ctx) {
  const calls = c.calls();
  expect(mutations(calls)).toEqual([]);
  expect(calls.filter((l) => l.startsWith("pveam download "))).toEqual([]);
}

test("R9 DYNO_BINARY with no install.sh beside the script checks the release's install.sh, then pushes and runs it", async () => {
  const { c, local, run } = ownBuild();
  succeeds(await run());
  expect(downloads(c)).toEqual(["SHA256SUMS", "install.sh"]); // the binary is the local one, so no download
  const { bin } = expectInstallRan(c, "105"); // finds the pushed install.sh by the release's marker
  expect(bin.content).toBe(readText(local));
});

test("R9 an install.sh that no longer matches its SHA256SUMS line is refused and named", async () => {
  const { c, run } = ownBuild();
  appendFileSync(join(c.release, "install.sh"), "\n# tampered\n");
  const r = await run();
  fails(r);
  expect(r.stderr).toMatch(/checksum mismatch.*install\.sh/);
  expectNothingDone(c);
});

test("R9 a SHA256SUMS with no line for install.sh is refused and names the file", async () => {
  const { c, run } = ownBuild({ noSum: ["install.sh"] });
  const r = await run();
  fails(r);
  expect(r.stderr).toMatch(/SHA256SUMS.*install\.sh/);
  expectNothingDone(c);
});

test("R9 a release without SHA256SUMS is refused, never installed unchecked", async () => {
  const { c, run } = ownBuild({ missing: ["SHA256SUMS"] });
  const r = await run();
  fails(r);
  expect(r.stderr).toMatch(/download failed.*SHA256SUMS/);
  expectNothingDone(c);
});

test("R9 an install.sh beside the script is used as it is, with no download and no check", async () => {
  const { c, run } = ownBuild({}, `${readText(INSTALL)}\n# beside-marker\n`);
  succeeds(await run());
  expect(downloads(c)).toEqual([]);
  expectInstallRan(c, "105", "# beside-marker");
});

// ---- regressions R10 to R19 (cases.md, "Added after review round 1") ----

// R10: install.sh stages the new binary as dyno.new beside the old one and renames it over. If a step fails (a full disk,
// a read-only /usr/local) the run has to stop there, with the old binary, the unit, the settings and the history as they
// were. This tool fails on anything named *.new; cp leaves behind the partial file a full disk leaves.
function failOnNew(c: Ctx, tool: "cp" | "chmod" | "mv") {
  c.stub(
    tool,
    `for a; do
  case "$a" in
    *.new)
      ${tool === "cp" ? 'printf partial > "$a"' : ":"}
      echo "${tool}: cannot write $a: injected failure" >&2
      exit 1;;
  esac
done
exec '${join(TOOLS, tool)}' "$@"`,
  );
}

const writeSteps: [step: string, tool: "cp" | "chmod" | "mv", verb: string, suffix: string][] = [
  ["the copy", "cp", "write", ".new"],
  ["setting its permissions", "chmod", "write", ".new"],
  ["the rename onto the old binary", "mv", "replace", ""],
];
for (const [step, tool, verb, suffix] of writeSteps) {
  for (const upgrade of [false, true]) {
    test(`R10 install.sh stops when ${step} fails (${upgrade ? "upgrade" : "fresh install"}) and says nothing was changed`, async () => {
      const c = installCtx({ version: "v0.2.0-test" });
      if (upgrade) seedInstall(c.root); // v0.1.0-test with its unit, settings and history
      const before = snapshot(c.root);
      failOnNew(c, tool);
      const r = await c.install();
      fails(r);
      expect(r.stderr).toContain(`couldn't ${verb} ${join(c.root, "usr/local/bin/dyno")}${suffix}; nothing was changed`);
      expect(snapshot(c.root)).toEqual(before); // no dyno.new either
      expect(systemctlCalls(c)).toEqual([]);
      expect(r.out).not.toMatch(/\b(installed|reinstalled|upgraded) v\d/);
      expect(readdirSync(c.tmp)).toEqual([]);
    });
  }
}

// A signal while the copy runs leaves no half-written dyno.new behind either. The stub exits 0 after the signal, as
// the curl stub of R7 does, so only the script's `trap 'exit 1'` stops the run before its next step, the chmod of the
// new binary (watched by a stub that logs it).
for (const sig of ["INT", "TERM", "HUP", "PIPE"]) {
  test(`R10 install.sh stopped by SIG${sig} while it copies the binary leaves no dyno.new and no temp directory`, async () => {
    const c = installCtx({ version: "v0.2.0-test" });
    seedInstall(c.root);
    const before = snapshot(c.root);
    c.stub(
      "cp",
      `for a; do
  case "$a" in *.new) printf partial > "$a"; echo cp-signalled >> '@CALLS@'; kill -s ${sig} "$PPID"; exit 0;; esac
done
exec '${join(TOOLS, "cp")}' "$@"`,
    );
    c.stub("chmod", `echo "chmod $*" >> '@CALLS@'\nexec '${join(TOOLS, "chmod")}' "$@"`);
    const r = await c.install();
    expect(r.code).toBe(1); // the trap's exit, not the stub's
    expect(c.calls()).toContain("cp-signalled"); // the copy of the new binary began, and the signal was sent
    expect(c.calls().filter((l) => l.startsWith("chmod "))).toEqual([]); // and the run went no further
    expect(snapshot(c.root)).toEqual(before);
    expect(readdirSync(c.tmp)).toEqual([]);
  });
}

// R11: a hardened host mounts /tmp noexec, so nothing under TMPDIR runs. The new binary has to be tried from the
// install folder. This fake refuses to run from under TMPDIR.
const NOEXEC_TMP_DYNO = `#!/bin/sh
# dyno-fake noexec-tmp
case "$0" in "$TMPDIR"/*) echo "$0: Permission denied" >&2; exit 126;; esac
[ "$1" = -version ] && echo ${VERSION}
exit 0
`;

for (const via of ["release", "DYNO_BINARY"]) {
  test(`R11 install.sh tries a new binary from the install folder, so a noexec temp folder doesn't stop it (${via})`, async () => {
    const c = installCtx(via === "release" ? { binary: NOEXEC_TMP_DYNO } : {});
    const local = c.p("local", "dyno-noexec");
    put(local, NOEXEC_TMP_DYNO, 0o755);
    const r = await c.install([], via === "DYNO_BINARY" ? { DYNO_BINARY: local } : {});
    succeeds(r);
    expect(readText(join(c.root, "usr/local/bin/dyno"))).toBe(NOEXEC_TMP_DYNO);
    expect(r.out).toMatch(/\binstalled v0\.1\.0-test/);
    expect(exists(join(c.root, "usr/local/bin/dyno.new"))).toBe(false);
  });
}

// R12: the link names the address dyno listens on. Only "every address" (no host, 0.0.0.0 or [::]) means this machine's
// own address from hostname -I. A null host below is that case.
const LISTEN_LINKS: [listen: string, host: string | null][] = [
  ["127.0.0.1:3967", "127.0.0.1"],
  ["[::1]:8080", "[::1]"],
  ["localhost:8080", "localhost"],
  ["dyno.lan:3966", "dyno.lan"],
  [":8080", null],
  ["0.0.0.0:8080", null],
  ["[::]:3999", null],
];
const linkFor = (listen: string, own: string, host: string | null) =>
  `http://${host ?? own}:${listen.slice(listen.lastIndexOf(":") + 1)}`;
// The link a run printed: what follows "Open" (install.sh) or "open" (proxmox-lxc.sh) at the end of a line.
const linkOf = (out: string) => /[Oo]pen (http:\/\/\S+)$/m.exec(out)?.[1];

test("R12 install.sh links to the host DYNO_LISTEN names, on the first install and on a re-run", async () => {
  const got: [string, string | undefined][] = [];
  const want: [string, string][] = [];
  for (const [listen, host] of LISTEN_LINKS) {
    const c = installCtx();
    const first = await c.install([], { DYNO_LISTEN: listen });
    const again = await c.install(); // the re-run takes the address back out of dyno.env
    for (const r of [first, again]) {
      got.push([listen, linkOf(r.out)]);
      want.push([listen, linkFor(listen, "192.168.1.50", host)]); // what the stub hostname -I answers
    }
  }
  expect(got).toEqual(want);
}, MANY_RUNS_MS);

test("R12 proxmox-lxc.sh links to the host DYNO_LISTEN names, else to the container's address", async () => {
  const got: [string, string | undefined][] = [];
  const want: [string, string][] = [];
  for (const [listen, host] of LISTEN_LINKS) {
    const c = pveCtx();
    got.push([listen, linkOf((await c.proxmox([], { DYNO_LISTEN: listen })).out)]);
    want.push([listen, linkFor(listen, ADDRESS, host)]);
  }
  expect(got).toEqual(want);
}, MANY_RUNS_MS);

// R13: DYNO_LISTEN and DYNO_METRICS_LISTEN are for the first install. Once dyno.env exists, setting one on a re-run
// changes nothing, so the installer says so instead of letting you think it worked. proxmox-lxc.sh passes both as empty
// strings when they aren't set, and empty means not set.
const FIRST_INSTALL_ONLY: [name: string, env: Env, says: boolean][] = [
  ["DYNO_LISTEN", { DYNO_LISTEN: ":8080" }, true],
  ["DYNO_METRICS_LISTEN", { DYNO_METRICS_LISTEN: "127.0.0.1:3967" }, true],
  ["both", { DYNO_LISTEN: ":8080", DYNO_METRICS_LISTEN: "127.0.0.1:3967" }, true],
  ["neither", {}, false],
  ["both empty, as proxmox-lxc.sh passes them", { DYNO_LISTEN: "", DYNO_METRICS_LISTEN: "" }, false],
];

test("R13 a re-run says DYNO_LISTEN and DYNO_METRICS_LISTEN apply only on the first install, if either is set, and leaves dyno.env alone", async () => {
  const c = installCtx();
  const first = await c.install([], { DYNO_LISTEN: ":8080", DYNO_METRICS_LISTEN: "127.0.0.1:3967" });
  succeeds(first);
  const envPath = join(c.root, "etc/dyno/dyno.env");
  const mine = "# my edits\nDYNO_LISTEN=:7000\n";
  const wrong: string[] = [];
  if (/first install/.test(first.out)) wrong.push("the first install said it, but there they do apply");
  for (const [name, env, says] of FIRST_INSTALL_ONLY) {
    writeFileSync(envPath, mine);
    const r = await c.install([], env);
    succeeds(r);
    const said = /only on the first install/.test(r.stdout);
    if (said !== says) wrong.push(`${name}: ${said ? "said it" : "didn't say it"}`);
    if (says && !/edit \/etc\/dyno\/dyno\.env.*restart/.test(r.stdout)) wrong.push(`${name}: no way to change them`);
    if (readText(envPath) !== mine) wrong.push(`${name}: dyno.env changed`);
  }
  expect(wrong).toEqual([]);
});

// R14: systemctl edit keeps overrides in dyno.service.d, which counts as settings: --uninstall keeps the folder and
// --purge removes it, whether or not the unit file is still there. Nobody else's drop-ins are touched.
const DROP_IN = "etc/systemd/system/dyno.service.d";
const OVERRIDE = "[Service]\nAmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\n";

function seedDropIn(root: string) {
  put(join(root, DROP_IN, "override.conf"), OVERRIDE);
  put(join(root, "etc/systemd/system/other.service.d/keep.conf"), "[Service]\nNice=5\n");
}

test("R14 --purge removes the systemctl edit drop-in folder with the unit, and no other service's", async () => {
  const c = makeCtx();
  seedInstall(c.root);
  seedDropIn(c.root);
  succeeds(await c.install(["--purge"]));
  expect(exists(join(c.root, DROP_IN))).toBe(false);
  expect(readText(join(c.root, "etc/systemd/system/other.service.d/keep.conf"))).toBe("[Service]\nNice=5\n");
});

test("R14 --uninstall keeps the drop-in folder, and a --purge after it, with the unit long gone, removes it", async () => {
  const c = makeCtx();
  seedInstall(c.root);
  seedDropIn(c.root);
  succeeds(await c.install(["--uninstall"]));
  expect(readText(join(c.root, DROP_IN, "override.conf"))).toBe(OVERRIDE);
  expect(exists(join(c.root, "etc/systemd/system/dyno.service"))).toBe(false);
  succeeds(await c.install(["--purge"]));
  expect(exists(join(c.root, DROP_IN))).toBe(false);
  expect(exists(join(c.root, "etc/dyno"))).toBe(false);
  expect(exists(join(c.root, "etc/systemd/system/other.service.d/keep.conf"))).toBe(true);
});

// R15: once pct create has worked, a failure that ends the run ends by saying the container is still there and how to
// remove it, after the failure's own message. Failures before the create, successes and --upgrade runs don't say it.
const LEFT = (id: string) => `dyno: container ${id} was created and is still there; remove it with: pct stop ${id}; pct destroy ${id}`;
const errLines = (r: Result) => r.stderr.split("\n").filter(Boolean);
const TAILSCALE: Env = { DYNO_TAILSCALE: "1" };
const TAILSCALE_KEY: Env = { DYNO_TAILSCALE: "1", TS_AUTHKEY: "tskey-auth-abc123" };

// What fails, a glob over the arguments of the pct call that fails, and the environment of the run.
const afterCreate: [name: string, glob: string, env: Env][] = [
  ["pct start", "start*", {}],
  ["making the folder the files are pushed into", 'exec*"mkdir -p"*', {}],
  ["the push of the binary", "push*0755*", {}],
  ["the push of install.sh", "push*install.sh*", {}],
  ["the install inside the container", "exec*install.sh*", {}],
  ["the apt step of the Tailscale install", "exec*apt-get*", TAILSCALE],
  ["Tailscale's own installer", "exec*tailscale.com*", TAILSCALE],
  ["the push of the auth key", "push*ts-authkey*", TAILSCALE_KEY],
  ["tailscale up", 'exec*"tailscale up"*', TAILSCALE_KEY],
];
for (const [name, glob, env] of afterCreate) {
  test(`R15 when ${name} fails, the last thing said is that container 105 is still there`, async () => {
    const c = pveCtx();
    failPct(c, glob, 3, "boom");
    const r = await c.proxmox([], env);
    fails(r);
    const lines = errLines(r);
    expect(lines).toContain("boom"); // the failure's own message comes first
    expect(lines[lines.length - 1]).toBe(LEFT("105"));
    expect(lines.filter((l) => l === LEFT("105"))).toHaveLength(1);
  });
}

test("R15 a container that never gets an address is reported as left behind too", async () => {
  const c = pveCtx({ address: "" });
  const r = await c.proxmox();
  fails(r);
  const lines = errLines(r);
  expect(lines[lines.length - 2]).toMatch(/^dyno: container 105 has no address after 30 s/);
  expect(lines[lines.length - 1]).toBe(LEFT("105"));
});

for (const sig of ["INT", "TERM", "HUP", "PIPE"]) {
  test(`R15 SIG${sig} while the new container's network is awaited ends by saying it is still there`, async () => {
    const c = pveCtx({ address: "" });
    c.stub("sleep", `kill -s ${sig} "$PPID"\nexit 130`); // the script is between two tries when the signal comes
    const r = await c.proxmox();
    // The trap's exit. A script that ignored the signal would end on the stub's 130, through set -e.
    expect(r.code).toBe(1);
    const lines = errLines(r);
    expect(lines[lines.length - 1]).toBe(LEFT("105"));
    expect(readdirSync(c.tmp)).toEqual([]);
  });
}

// Whether each of these runs says a container was left, with the exit status the run has to end on and the message of
// the failure that ends it on stderr (null: none, a success or a signal). Only the first has created a container before
// it fails. A row that asserted only the absence of the still-there line would pass once its failure stopped happening.
const runs: [name: string, says: boolean, code: number, message: string | null, go: () => Promise<Result>][] = [
  ["a failed pct start", true, 3, "boom", async () => {
    const c = pveCtx();
    failPct(c, "start*", 3, "boom");
    return c.proxmox();
  }],
  ["a success", false, 0, null, () => pveCtx().proxmox()],
  ["a failed pct create", false, 3, "boom", async () => {
    const c = pveCtx();
    failPct(c, "create*", 3, "boom");
    return c.proxmox();
  }],
  ["no Debian template", false, 1, "dyno: no Debian 12 or 13 template is available", () => pveCtx({ templates: [] }).proxmox()],
  ["a checksum mismatch", false, 1, "dyno: checksum mismatch for install.sh; nothing was changed", () =>
    pveCtx({}, { badSum: ["install.sh"] }).proxmox()],
  ["a signal during the download", false, 1, null, async () => {
    const c = pveCtx();
    c.stub("curl", curlSignals("TERM"));
    return c.proxmox();
  }],
  ["an install that fails on --upgrade", false, 3, "boom", async () => {
    const c = pveCtx({ containers: { "106": "running" } });
    failPct(c, "exec*install.sh*", 3, "boom");
    return c.proxmox(["--upgrade"], { DYNO_CTID: "106" });
  }],
];
test("R15 only a failure after the create says a container was left; a success, an earlier failure and --upgrade don't", async () => {
  const wrong: string[] = [];
  for (const [name, says, code, message, go] of runs) {
    const r = await go();
    if (r.code !== code) wrong.push(`${name} exited ${r.code}, not ${code}`);
    if (message !== null && !r.stderr.includes(message)) wrong.push(`${name} didn't say ${JSON.stringify(message)}`);
    if (r.out.includes("was created and is still there") !== says) wrong.push(`${name} ${says ? "didn't say it" : "said it"}`);
  }
  expect(wrong).toEqual([]);
});

// R16: --upgrade pushes a binary into a container and runs a script there as root, so it first checks that the container
// is one this script made. The script tags what it creates dyno; tags are split on ; , and spaces.
const NOT_TAGGED = (id: string) =>
  `container ${id} isn't tagged dyno; check DYNO_CTID. If it is dyno's container, add the tag dyno to it in the Proxmox UI and run this again`;

// The tags line of the container, and whether --upgrade goes ahead with it. null: no tags line at all.
const tagCases: [tags: string | null, accepted: boolean][] = [
  ["dyno", true],
  ["dyno;web", true],
  ["web;dyno", true],
  ["web; dyno", true],
  ["web,dyno", true],
  ["web dyno", true],
  ["dynomite", false],
  ["mydyno;web", false],
  ["web;dyno2", false],
  ["dyno-old", false],
  ["", false],
  [null, false],
];

test("R16 --upgrade goes ahead only for a container tagged exactly dyno, and refuses any other before it fetches or pushes", async () => {
  const wrong: string[] = [];
  for (const [tags, accepted] of tagCases) {
    const c = pveCtx({ containers: { "106": "running" }, tags });
    const r = await c.proxmox(["--upgrade"], { DYNO_CTID: "106" });
    const label = tags === null ? "no tags line" : JSON.stringify(tags);
    if (accepted) {
      if (r.code !== 0 || pct(c.calls(), "push").length !== 2) wrong.push(`${label} was refused`);
    } else if (r.code === 0 || r.stderr !== `dyno: ${NOT_TAGGED("106")}\n` || mutations(c.calls()).length > 0 || downloads(c).length > 0) {
      wrong.push(`${label} was not refused as it should be`);
    }
  }
  expect(wrong).toEqual([]);
}, MANY_RUNS_MS);

test("R16 a container the script created passes --upgrade, until its tag is taken off", async () => {
  const c = pveCtx();
  succeeds(await c.proxmox());
  expect(opt(createLine(c), "tags")).toBe("dyno");
  c.resetCalls();
  succeeds(await c.proxmox(["--upgrade"], { DYNO_CTID: "105" }));
  expectInstallRan(c, "105");

  writeFileSync(join(c.lxc, "105.conf"), "arch: amd64\ntags: web\n"); // the tag removed in the Proxmox UI
  c.resetCalls();
  const r = await c.proxmox(["--upgrade"], { DYNO_CTID: "105" });
  fails(r);
  expect(r.stderr).toContain(NOT_TAGGED("105"));
  expect(mutations(c.calls())).toEqual([]);
});

// R17: TS_AUTHKEY must not be an argument of any pct call, since the host's process list shows those. It goes into the
// container as a file, tailscale up reads it from there, and the file is deleted whether or not tailscale up worked.
const AUTH_KEY = "tskey-auth-k8s3Cr3t-abcdef";
const KEY_FILE = "/root/ts-authkey";
const isKeyRm = (l: string) => l.startsWith("pct exec 105 -- rm ") && l.endsWith(KEY_FILE);

test("R17 TS_AUTHKEY reaches tailscale up through a file only root can read, and no pct argument holds the key", async () => {
  const c = pveCtx();
  const r = await c.proxmox([], { DYNO_TAILSCALE: "1", TS_AUTHKEY: AUTH_KEY, DYNO_HOSTNAME: "tsbox" });
  succeeds(r);
  const calls = c.calls();
  expect(calls.filter((l) => l.includes(AUTH_KEY))).toEqual([]);
  expect(r.out).not.toContain(AUTH_KEY);

  const files = c.pushes().filter((x) => x.dst === KEY_FILE);
  expect(files).toHaveLength(1);
  expect(files[0]).toMatchObject({ id: "105", perms: "0600", content: AUTH_KEY });

  const push = calls.findIndex((l) => l.startsWith("pct push 105 ") && l.endsWith(`${KEY_FILE} --perms 0600`));
  const up = calls.findIndex((l) => l.startsWith("pct exec 105 -- tailscale up "));
  const rm = calls.findIndex((l, i) => i > up && l.startsWith("pct exec 105 -- rm ") && l.endsWith(KEY_FILE));
  expect(calls[up]).toBe(`pct exec 105 -- tailscale up --auth-key=file:${KEY_FILE} --hostname tsbox`);
  expect(push).toBeGreaterThanOrEqual(0);
  expect(up).toBeGreaterThan(push);
  expect(rm).toBeGreaterThan(up);
  expect(calls.filter(isKeyRm)).toHaveLength(1); // once: the cleanup at exit (R20) doesn't delete it a second time
});

test("R17 the key file is deleted in the container even when tailscale up fails, and the run exits with its status", async () => {
  const c = pveCtx();
  failPct(c, 'exec*"tailscale up"*', 3, "tailscale up: invalid key");
  const r = await c.proxmox([], { DYNO_TAILSCALE: "1", TS_AUTHKEY: AUTH_KEY });
  expect(r.code).toBe(3);
  const calls = c.calls();
  const up = calls.findIndex((l) => l.startsWith("pct exec 105 -- tailscale up "));
  expect(up).toBeGreaterThan(0);
  expect(calls.slice(up + 1).some((l) => l.startsWith("pct exec 105 -- rm ") && l.endsWith(KEY_FILE))).toBe(true);
  expect(calls.filter(isKeyRm)).toHaveLength(1); // once: the cleanup at exit (R20) doesn't delete it a second time
  expect(calls.filter((l) => l.includes(AUTH_KEY))).toEqual([]);
});

test("R17 a key with quotes, blanks or shell syntax arrives byte for byte and is never part of a command", async () => {
  const bad: string[] = [];
  for (const key of ["k'; touch PWN; echo '", 'k"; touch PWN; echo "', "k$(touch PWN)", "k`touch PWN`", "k && touch PWN", "-k with  blanks  "]) {
    const c = pveCtx();
    const r = await c.proxmox([], { DYNO_TAILSCALE: "1", TS_AUTHKEY: key });
    const file = c.pushes().find((x) => x.dst === KEY_FILE);
    if (r.code !== 0 || file?.content !== key || c.calls().some((l) => l.includes(key))) bad.push(key);
  }
  expect(bad).toEqual([]);
});

// R18: systemd strips the blanks around the = of an EnvironmentFile line, so `DYNO_LISTEN = :7005` is a setting for the
// service, and the port in the link (and the one the health check polls) has to be that one. Lines that aren't the
// setting still aren't.
const ENV_SYNTAX: [name: string, text: string, port: string][] = [
  ["spaces around the equals sign", "DYNO_LISTEN = :7005", "7005"],
  ["tabs around the equals sign", "DYNO_LISTEN\t=\t:7005", "7005"],
  ["a space before the equals sign only", "DYNO_LISTEN =:7005", "7005"],
  ["blanks all round and a quoted value", '  DYNO_LISTEN   =   ":7005"  ', "7005"],
  ["a ; comment line after the setting", "DYNO_LISTEN=:7001\n; DYNO_LISTEN=:7002\n;DYNO_LISTEN=:7003", "7001"],
  ["only a ; comment", ";DYNO_LISTEN=:7004", "3966"],
  ["a # comment with blanks round the equals sign", "#DYNO_LISTEN = :7004", "3966"],
  ["a tab before the name", "\tDYNO_LISTEN=:7006", "7006"],
  ["a quoted value followed by spaces", 'DYNO_LISTEN=":7007"   ', "7007"],
  ["a space before a quoted value", "DYNO_LISTEN= ':7008'", "7008"],
  ["Windows line endings", "DYNO_LISTEN=:7009\r\nDYNO_METRICS_LISTEN=127.0.0.1:7010\r", "7009"],
  ["another variable that ends in DYNO_LISTEN", "MY_DYNO_LISTEN=:7011", "3966"],
  ["another variable that starts with DYNO_LISTEN", "DYNO_LISTEN_X = :7012", "3966"],
];

test("R18 the printed port follows dyno.env as systemd reads it, blanks around the equals sign included", async () => {
  const wrong: string[] = [];
  for (const [name, text, port] of ENV_SYNTAX) {
    const c = installCtx();
    put(join(c.root, "etc/dyno/dyno.env"), `${text}\n`);
    const r = await c.install();
    const link = linkOf(r.out);
    if (r.code !== 0 || !link?.endsWith(`:${port}`)) wrong.push(`${name}: ${link}`);
  }
  expect(wrong).toEqual([]);
}, MANY_RUNS_MS);

// R19: values made only of allowed characters that still aren't a port, or a host and a port, that dyno can bind. dyno
// refuses them at startup, so the service would restart in a loop. A 20-digit port has to be refused too, and not crash
// test or wrap round to a small number.
const NOT_ADDRESSES = [
  ":99999", ":65536", ":0", ":00000", ":99999999999999999999", ":18446744073709551696", // the last is 2**64 + 80
  "::1:8080", "host:80:80", ":80:80", "[::1:8080", "[::1]", "[::1]:", "[]:80", "[::g]:80", "[[::1]]:80", "[::1]]:80",
  "[::1]:80:80", "host name:80", "host_x:80", ":+80", ":80.5",
];
const ADDRESSES = [
  ":8080", "127.0.0.1:3967", "0.0.0.0:3966", "[::1]:8080", "[::]:3966", "localhost:8080", "dyno.lan:3966", ":1", ":65535",
  "my-host.lan:80", "[fe80::1]:80", "[::ffff:192.168.1.1]:80",
];

for (const name of ["DYNO_LISTEN", "DYNO_METRICS_LISTEN"]) {
  for (const script of ["install.sh", "proxmox-lxc.sh"] as const) {
    test(`R19 ${script} refuses a ${name} that is not a port or a host and port, and still accepts the real ones`, async () => {
      expect(await listenWrong(script, name, NOT_ADDRESSES, ADDRESSES)).toEqual([]);
    }, MANY_RUNS_MS);
  }
}

// ---- regressions R20 and R21 (cases.md, "Added after black-box round 3"; round 4 widened R20, round 5 added PIPE to
// R7 and R20). R20 is the signal case; R21 is about deletes that fail, whatever ended the run. ----

const KEY_ENV: Env = { DYNO_TAILSCALE: "1", TS_AUTHKEY: AUTH_KEY };
const UP = 'exec*"tailscale up"*'; // the glob of the pct call that runs tailscale up
const DIR = "/root/dyno-install";

// A pct call that signals the script (this stub's parent) and dies, as a Ctrl-C does to whatever runs when it comes.
// It logs the signal too, so a pass can't mean the call never came.
const signalBody = (sig: string) =>
  `    echo "pct $*" >> '@CALLS@'\n    echo "signalled SIG${sig}" >> '@CALLS@'\n    kill -s ${sig} "$PPID"\n    exit 130`;

// R20: tailscale up can wait on a bad or slow key, so a Ctrl-C, a kill or a dropped ssh session can come while the key
// file is in the container. The key is the one secret a run leaves anywhere, so it goes however the run ends.
for (const sig of ["INT", "TERM", "HUP", "PIPE"]) {
  test(`R20 SIG${sig} during tailscale up still deletes the auth key in the container`, async () => {
    const c = pveCtx();
    wrapPct(c, [[UP, signalBody(sig)]]);
    const r = await c.proxmox([], KEY_ENV);
    expect(r.code).toBe(1);
    const calls = c.calls();
    expect(calls).toContain(`signalled SIG${sig}`); // tailscale up ran and the signal was sent
    expect(c.pushes().some((x) => x.dst === KEY_FILE)).toBe(true);
    const up = calls.findIndex((l) => l.startsWith("pct exec 105 -- tailscale up "));
    expect(calls.slice(up + 1).filter(isKeyRm)).toHaveLength(1); // deleted at exit, once
    const lines = errLines(r);
    expect(lines[lines.length - 1]).toBe(LEFT("105")); // R15: the last thing said is that the container is there
    expect(readdirSync(c.tmp)).toEqual([]);
  });
}

// R20, widened by black-box round 4 (findings D5 and D3): two more ways to leave the key in the container, and the same
// promise for each. A push can write the file and still fail, or a signal can come while it runs, so the script
// remembers the key from just before the push. And a second signal, a double Ctrl-C, can come while the exit cleanup
// runs: it must not cut the cleanup short, and at a terminal it would kill the delete as well.
//
// The usual pct stub only logs a delete, so that the files a run pushed can be read after it. These tests model the
// container's rm instead: a delete of the key that works removes the file from the container's folder, and one that
// fails leaves it there.
const PUSH_KEY = "push*ts-authkey*"; // the glob of the pct call that pushes the key file
const KEY_RM = `pct exec 105 -- rm -f ${KEY_FILE}`; // the call that deletes it at exit
const RM_OK = `    rm -f '@CTR@${KEY_FILE}'\n    exit 0`; // the container's rm, working
const RM_FAILS = `    echo "rm: the container is gone" >&2\n    exit 1`; // and failing: the file stays
const keyIn = (c: Ctx) => c.p("ctr", "root", "ts-authkey");
const keyPushes = (c: Ctx) => readText(c.p("pushes.tsv")).split("\n").filter((l) => l.split("\t")[2] === KEY_FILE).length;

// A signal at a call as a terminal sends it, to the whole foreground group: the script gets it, and so does the call,
// which dies of it before it does or says anything, unless the script has set that signal to be ignored for what it
// starts.
const groupSignal = (sig: string) =>
  `    echo "signalled SIG${sig}" >> '@CALLS@'\n    kill -s ${sig} "$PPID"\n    kill -s ${sig} $$`;

// The arm for the delete of the key file: it logs the call, takes the signal named (if any), then ends as `end` says.
const keyRm = (end: string, hit?: string): [string, string] => [
  `exec*"-- rm "*${KEY_FILE}`,
  [`    echo "pct $*" >> '@CALLS@'`, hit ? groupSignal(hit) : "", end].filter(Boolean).join("\n"),
];

// The key push as a dropped link leaves it: the file lands in the container (the usual stub makes the push), then the
// call reports a failure. For a signal the push finishes and exits 0, so that here only the signal, not a failed push,
// ends the run.
const pushLandsThenFails = `    '@REAL@' "$@"\n    echo "pct push: connection lost" >&2\n    exit 7`;
const pushThenSignal = (sig: string) =>
  `    '@REAL@' "$@"\n    echo "signalled SIG${sig}" >> '@CALLS@'\n    kill -s ${sig} "$PPID"\n    exit 0`;

type KeyEnd = {
  code: number; // the exit status
  signals: string[]; // the signals the stubs sent, in order
  up: boolean; // whether tailscale up was called
  deletes?: number; // calls of the key's delete; once, at exit, unless said
  stays?: boolean; // that delete fails: the key stays, and the run says so once
  warns?: number | null; // warnings that the key couldn't be removed, if not as `stays` says; null: not checked
};

// What a run owes at its end, whatever ended it, when the key may have been in the container (R20): the key deleted
// once, at exit, and gone from the container (when that delete fails, still there and said so once, right before the
// still-there line), R15's still-there line last and once, the temp folder gone. It also checks that the run went as
// the test meant: the key file pushed once, the signals sent, tailscale up called or not. Returns what is wrong, each
// as what was wanted and what was seen, so a failing test lists all of it.
function keyEnd(c: Ctx, r: Result, want: KeyEnd): string[] {
  const calls = c.calls();
  const lines = errLines(r);
  const bad: string[] = [];
  const check = (what: string, wanted: unknown, saw: unknown) => {
    if (JSON.stringify(wanted) !== JSON.stringify(saw)) bad.push(`${what}: wanted ${JSON.stringify(wanted)}, saw ${JSON.stringify(saw)}`);
  };
  check("pushes of the key file", 1, keyPushes(c));
  check("signals the stubs sent", want.signals.map((s) => `signalled SIG${s}`), calls.filter((l) => l.startsWith("signalled ")));
  check("calls of tailscale up", want.up ? 1 : 0, calls.filter((l) => l.startsWith("pct exec 105 -- tailscale up ")).length);
  check("exit status", want.code, r.code);
  check(`calls of "${KEY_RM}"`, want.deletes ?? 1, times(calls, KEY_RM));
  check("the key file in the container", want.stays ? "still there" : "gone", exists(keyIn(c)) ? "still there" : "gone");
  if (want.warns !== null) {
    check("warnings that the key couldn't be removed", want.warns ?? (want.stays ? 1 : 0), times(lines, CANT(KEY_FILE, "-f")));
  }
  check("stderr lines saying the container is still there", 1, times(lines, LEFT("105")));
  check("the last stderr line", LEFT("105"), lines[lines.length - 1]);
  if (want.stays) check("the last two stderr lines", [CANT(KEY_FILE, "-f"), LEFT("105")], lines.slice(-2));
  check("what is left in the temp folder", [], readdirSync(c.tmp));
  return bad;
}

test("R20 a key push that lands the file and then fails still deletes the key in the container", async () => {
  const c = pveCtx();
  wrapPct(c, [[PUSH_KEY, pushLandsThenFails], keyRm(RM_OK)]);
  const r = await c.proxmox([], KEY_ENV);
  expect(keyEnd(c, r, { code: 7, signals: [], up: false })).toEqual([]);
});

for (const sig of ["INT", "TERM", "HUP", "PIPE"]) {
  test(`R20 SIG${sig} while the auth key is pushed still deletes it in the container`, async () => {
    const c = pveCtx();
    wrapPct(c, [[PUSH_KEY, pushThenSignal(sig)], keyRm(RM_OK)]);
    const r = await c.proxmox([], KEY_ENV);
    expect(keyEnd(c, r, { code: 1, signals: [sig], up: false })).toEqual([]);
  });
}

// A signal ends the run during tailscale up, and a second one comes while the exit cleanup deletes the key. A double
// Ctrl-C is INT and INT; the other pairs make each of the three signals the second one.
const DOUBLE: [first: string, second: string][] = [["TERM", "HUP"], ["INT", "INT"], ["HUP", "TERM"], ["TERM", "PIPE"]];
for (const [first, second] of DOUBLE) {
  test(`R20 SIG${second} during the exit cleanup after SIG${first} doesn't stop the auth key being deleted`, async () => {
    const c = pveCtx();
    wrapPct(c, [[UP, signalBody(first)], keyRm(RM_OK, second)]);
    const r = await c.proxmox([], KEY_ENV);
    expect(keyEnd(c, r, { code: 1, signals: [first, second], up: true })).toEqual([]);
  });

  test(`R20 SIG${second} during the exit cleanup after SIG${first}, with the key delete failing, warns once before the still-there line`, async () => {
    const c = pveCtx();
    wrapPct(c, [[UP, signalBody(first)], keyRm(RM_FAILS, second)]);
    const r = await c.proxmox([], KEY_ENV);
    expect(keyEnd(c, r, { code: 1, signals: [first, second], up: true, stays: true })).toEqual([]);
  });
}

// The one place the key is deleted twice, on purpose: a signal that lands while the normal path's own delete runs. The
// script can't tell whether that delete finished (a Ctrl-C kills the call too), so the exit cleanup deletes again, and a
// second rm -f does no harm. Here the first delete is killed before it removes anything: only the second one empties the
// container. Whether the killed call's failure is warned about is not what this checks.
test("R20 a signal landing while the key is deleted after tailscale up makes the exit cleanup delete it again", async () => {
  const c = pveCtx();
  const firstOneKilled = [
    `    if [ ! -e '@CALLS@.seen' ]; then`,
    `      : > '@CALLS@.seen'`,
    groupSignal("INT"),
    `      exit 130`,
    `    fi`,
    RM_OK,
  ].join("\n");
  wrapPct(c, [keyRm(firstOneKilled)]);
  const r = await c.proxmox([], KEY_ENV);
  expect(keyEnd(c, r, { code: 1, signals: ["INT"], up: true, deletes: 2, warns: null })).toEqual([]);
});

// R21: deleting what the run pushed is tidying. When it fails (the container stopped, pct exec refusing) the run says so
// and goes on, so its exit status, the retry line (R5) and its last line (R15) are still those of the install, of
// tailscale up or of the signal.
const CANT = (path: string, flags: string, id = "105") =>
  `dyno: couldn't remove ${path} in container ${id}; remove it with: pct exec ${id} -- rm ${flags} ${path}`;
const RETRY = (id: string, status: number) =>
  `dyno: the install inside container ${id} failed (exit ${status}); its message is above. Fix that, then run: DYNO_CTID=${id} sh proxmox-lxc.sh --upgrade`;
const installFails: [string, string] = ["exec*install.sh*", failBody(3, "boom from the install")];
const upFails: [string, string] = [UP, failBody(3, "tailscale up: invalid key")];
// The delete of one path fails. The glob names the path, so the `rm` inside the Tailscale installer's sh -c isn't hit.
const rmFails = (path: string): [string, string] => [`exec*"-- rm "*${path}`, failBody(1, "rm: the container is gone")];
const times = (lines: string[], line: string) => lines.filter((l) => l === line).length;

test("R21 an install that fails, with deleting its folder failing too, still exits with its status and prints the retry line", async () => {
  const c = pveCtx();
  wrapPct(c, [installFails, rmFails(DIR)]);
  const r = await c.proxmox();
  expect(r.code).toBe(3);
  const lines = errLines(r);
  expect(times(lines, CANT(DIR, "-rf"))).toBe(1);
  expect(lines).toContain(RETRY("105", 3));
  expect(lines[lines.length - 1]).toBe(LEFT("105"));
});

test("R21 the same with --upgrade ends on the retry line, and says nothing was created", async () => {
  const c = pveCtx({ containers: { "106": "running" } });
  wrapPct(c, [installFails, rmFails(DIR)]);
  const r = await c.proxmox(["--upgrade"], { DYNO_CTID: "106" });
  expect(r.code).toBe(3);
  const lines = errLines(r);
  expect(times(lines, CANT(DIR, "-rf", "106"))).toBe(1);
  expect(lines[lines.length - 1]).toBe(RETRY("106", 3));
  expect(r.out).not.toContain("was created and is still there");
});

test("R21 tailscale up that fails, with deleting the key file failing too, still exits with its status", async () => {
  const c = pveCtx();
  wrapPct(c, [upFails, rmFails(KEY_FILE)]);
  const r = await c.proxmox([], KEY_ENV);
  expect(r.code).toBe(3);
  const lines = errLines(r);
  expect(times(lines, CANT(KEY_FILE, "-f"))).toBe(1); // once: the cleanup at exit doesn't try and warn again
  expect(c.calls().filter(isKeyRm)).toHaveLength(1);
  expect(lines[lines.length - 1]).toBe(LEFT("105"));
});

test("R21 a signal during tailscale up, with deleting the key file at exit failing, still exits 1 and says it", async () => {
  const c = pveCtx();
  wrapPct(c, [[UP, signalBody("INT")], rmFails(KEY_FILE)]);
  const r = await c.proxmox([], KEY_ENV);
  expect(r.code).toBe(1);
  const lines = errLines(r);
  expect(times(lines, CANT(KEY_FILE, "-f"))).toBe(1); // a key left in the container is reported, not silent
  expect(c.calls().filter(isKeyRm)).toHaveLength(1);
  expect(lines[lines.length - 1]).toBe(LEFT("105"));
});

test("R21 an install that worked, with deleting its folder failing, warns and still ends well", async () => {
  const c = pveCtx();
  wrapPct(c, [rmFails(DIR)]);
  const r = await c.proxmox();
  succeeds(r);
  expect(times(errLines(r), CANT(DIR, "-rf"))).toBe(1);
  expect(r.stdout).toContain(`dyno: done. dyno is container 105; open http://${ADDRESS}:3966`);
  expect(r.out).not.toContain("was created and is still there");
});

test("R21 tailscale up that worked, with deleting the folder and the key file failing, warns about each once", async () => {
  const c = pveCtx();
  wrapPct(c, [rmFails(DIR), rmFails(KEY_FILE)]);
  const r = await c.proxmox([], KEY_ENV);
  succeeds(r);
  const lines = errLines(r);
  expect(times(lines, CANT(DIR, "-rf"))).toBe(1);
  expect(times(lines, CANT(KEY_FILE, "-f"))).toBe(1);
  expect(c.calls().filter(isKeyRm)).toHaveLength(1);
});
