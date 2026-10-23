import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";

import { binaries, build } from "../build.mjs";

const targets = ["darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64", "windows-amd64", "windows-arm64"];

// release writes a goreleaser dist holding a fake binary for each target given, at one version.
function release(t, version, built = targets) {
  const dist = mkdtempSync(path.join(tmpdir(), "migr-dist-"));
  t.after(() => rmSync(dist, { recursive: true, force: true }));

  const artifacts = built.map((target) => {
    const [goos, goarch] = target.split("-");
    const file = path.join(dist, `migr_${target}`, goos === "windows" ? "migr.exe" : "migr");
    mkdirSync(path.dirname(file), { recursive: true });
    writeFileSync(file, `binary for ${target}`);

    return { name: path.basename(file), path: file, goos, goarch, type: "Binary" };
  });

  artifacts.push({ name: "checksums.txt", path: path.join(dist, "checksums.txt"), type: "Checksum" });
  writeFileSync(path.join(dist, "artifacts.json"), JSON.stringify(artifacts));
  writeFileSync(path.join(dist, "metadata.json"), JSON.stringify({ version }));

  return dist;
}

// manifest reads one built package's package.json.
function manifest(out, dir) {
  return JSON.parse(readFileSync(path.join(out, dir, "package.json"), "utf8"));
}

test("a release becomes seven packages at its version, the wrapper pinning each exactly", (t) => {
  const out = path.join(release(t, "1.2.3"), "npm");
  const { packages } = build(path.dirname(out), out);

  assert.equal(packages.length, 7);

  const wrapper = manifest(out, "cli");
  assert.equal(wrapper.name, "@migr/cli");
  assert.equal(wrapper.version, "1.2.3");
  assert.deepEqual(wrapper.bin, { migr: "bin/migr.js" });
  assert.equal(wrapper.scripts, undefined);
  assert.deepEqual(
    wrapper.optionalDependencies,
    Object.fromEntries(targets.map((target) => [`@migr/cli-${target}`, "1.2.3"])),
  );
});

test("each platform package is gated to its platform in Node's names and holds an executable binary", (t) => {
  const out = path.join(release(t, "1.2.3-rc.1"), "npm");
  build(path.dirname(out), out);

  const want = {
    "darwin-amd64": ["darwin", "x64", "migr"],
    "darwin-arm64": ["darwin", "arm64", "migr"],
    "linux-amd64": ["linux", "x64", "migr"],
    "linux-arm64": ["linux", "arm64", "migr"],
    "windows-amd64": ["win32", "x64", "migr.exe"],
    "windows-arm64": ["win32", "arm64", "migr.exe"],
  };

  for (const [target, [os, cpu, binary]] of Object.entries(want)) {
    const pkg = manifest(out, `cli-${target}`);
    assert.equal(pkg.name, `@migr/cli-${target}`);
    assert.equal(pkg.version, "1.2.3-rc.1");
    assert.deepEqual([pkg.os, pkg.cpu], [[os], [cpu]], target);

    const file = path.join(out, `cli-${target}`, "bin", binary);
    assert.equal(readFileSync(file, "utf8"), `binary for ${target}`);
    if (process.platform !== "win32") {
      assert.equal(statSync(file).mode & 0o111, 0o111, `${target} is not executable`);
    }
  }
});

test("a release missing a target is refused rather than published without it", (t) => {
  const dist = release(t, "1.2.3", targets.slice(1));

  assert.throws(() => build(dist, path.join(dist, "npm")), /no binary for darwin-amd64/);
});

test("a target no package is defined for is refused", () => {
  const artifacts = targets.map((target) => {
    const [goos, goarch] = target.split("-");
    return { type: "Binary", goos, goarch, path: target };
  });
  artifacts.push({ type: "Binary", goos: "freebsd", goarch: "amd64", path: "x" });

  assert.throws(() => binaries(artifacts), /freebsd-amd64, which no npm package is defined for/);
});

test("a release with no version is refused", (t) => {
  const dist = release(t, "");

  assert.throws(() => build(dist, path.join(dist, "npm")), /names no version/);
});
