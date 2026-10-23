import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const launcher = path.join(here, "..", "bin", "migr.js");
const { platformPackage, shipped } = createRequire(import.meta.url)(launcher);

const unix = process.platform !== "win32";

const fakeBinary = `#!/usr/bin/env node
const [what, value] = process.argv.slice(2);
if (what === "exit") process.exit(Number(value));
if (what === "signal") { process.kill(process.pid, value); setTimeout(() => {}, 5000); }
if (what === "args") process.stdout.write(JSON.stringify(process.argv.slice(3)));
if (what === "interrupt") { process.on("SIGINT", () => process.exit(7)); process.stdout.write("ready\\n"); setInterval(() => {}, 1000); }
`;

// installed lays out a project's node_modules: @migr/cli at one version, and this machine's platform package at another unless omitted.
function installed(t, { wrapper = "1.2.3", platform = wrapper } = {}) {
  const project = mkdtempSync(path.join(tmpdir(), "migr-cli-"));
  t.after(() => rmSync(project, { recursive: true, force: true }));

  const cli = path.join(project, "node_modules", "@migr", "cli");
  mkdirSync(path.join(cli, "bin"), { recursive: true });
  copyFileSync(launcher, path.join(cli, "bin", "migr.js"));
  writeFileSync(path.join(cli, "package.json"), JSON.stringify({ name: "@migr/cli", version: wrapper }));

  if (platform !== null) {
    const pkg = path.join(project, "node_modules", ...platformPackage(process.platform, process.arch).split("/"));
    mkdirSync(path.join(pkg, "bin"), { recursive: true });
    writeFileSync(path.join(pkg, "package.json"), JSON.stringify({ name: "x", version: platform }));
    writeFileSync(path.join(pkg, "bin", "migr"), fakeBinary);
    chmodSync(path.join(pkg, "bin", "migr"), 0o755);
  }

  return path.join(cli, "bin", "migr.js");
}

// migr runs the installed launcher with arguments, as npx would.
function migr(bin, ...args) {
  return spawnSync(process.execPath, [bin, ...args], { encoding: "utf8", timeout: 10_000 });
}

test("every Node platform migr ships for names the package goreleaser built it into", () => {
  assert.equal(platformPackage("darwin", "arm64"), "@migr/cli-darwin-arm64");
  assert.equal(platformPackage("darwin", "x64"), "@migr/cli-darwin-amd64");
  assert.equal(platformPackage("linux", "x64"), "@migr/cli-linux-amd64");
  assert.equal(platformPackage("linux", "arm64"), "@migr/cli-linux-arm64");
  assert.equal(platformPackage("win32", "x64"), "@migr/cli-windows-amd64");
  assert.equal(platformPackage("win32", "arm64"), "@migr/cli-windows-arm64");
  assert.equal(shipped.length, 6);
});

test("a platform migr does not ship for is refused, naming the ones it does", () => {
  assert.throws(() => platformPackage("freebsd", "x64"), /no binary for freebsd\/x64.*darwin-arm64/);
  assert.throws(() => platformPackage("linux", "ia32"), /no binary for linux\/ia32/);
});

test("the binary's exit status is the wrapper's", { skip: !unix }, (t) => {
  const bin = installed(t);

  for (const code of [0, 3, 6]) {
    assert.equal(migr(bin, "exit", String(code)).status, code, `exit ${code}`);
  }
});

test("the arguments reach the binary unchanged", { skip: !unix }, (t) => {
  const result = migr(installed(t), "args", "up", "--to", "a b", "");

  assert.deepEqual(JSON.parse(result.stdout), ["up", "--to", "a b", ""]);
});

test("a binary ended by a signal ends the wrapper by the same signal", { skip: !unix }, (t) => {
  const result = migr(installed(t), "signal", "SIGTERM");

  assert.equal(result.signal, "SIGTERM");
});

test("Ctrl-C reaches the binary, which ends as it chooses, and the wrapper outlives it", { skip: !unix, timeout: 10_000 }, async (t) => {
  const wrapper = spawn(process.execPath, [installed(t), "interrupt"], { detached: true, stdio: ["ignore", "pipe", "inherit"] });
  t.after(() => wrapper.kill("SIGKILL"));

  const ended = new Promise((resolve) => wrapper.once("exit", (...how) => resolve(how)));
  const started = await Promise.race([
    new Promise((resolve) => wrapper.stdout.once("data", () => resolve(true))),
    ended.then(() => false),
  ]);
  assert.ok(started, "the binary ended before it was interrupted");

  process.kill(-wrapper.pid, "SIGINT");

  assert.deepEqual(await ended, [7, null]);
});

test("a missing platform package is refused, naming it and why it would be missing", (t) => {
  const result = migr(installed(t, { platform: null }), "version");

  assert.equal(result.status, 1);
  assert.match(result.stderr, new RegExp(`${platformPackage(process.platform, process.arch)} is not installed`));
  assert.match(result.stderr, /--omit=optional/);
});

test("a platform package of another version is refused before anything runs", { skip: !unix }, (t) => {
  const result = migr(installed(t, { wrapper: "1.2.3", platform: "1.2.2" }), "exit", "0");

  assert.equal(result.status, 1);
  assert.match(result.stderr, /@migr\/cli is 1\.2\.3 but .* is 1\.2\.2/);
});
