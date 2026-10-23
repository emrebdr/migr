// Publishes the seven packages build.mjs wrote (ADR 0100): the six platform packages first and
// @migr/cli last, so the wrapper never names a package the registry does not hold yet; a version
// already there is skipped, so a failed release can be run again. Before anything, the binary for this
// machine must report the packages' version. Run as `node npm/cli/publish.mjs <out> [--dry-run]`.

import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const goos = { darwin: "darwin", linux: "linux", win32: "windows" };
const goarch = { x64: "amd64", arm64: "arm64" };

// publishOrder lists the package directories under out, every platform package before the wrapper.
export function publishOrder(dirs) {
  const platforms = dirs.filter((d) => d.startsWith("cli-")).sort();
  if (!dirs.includes("cli")) {
    throw new Error("no cli directory: run build.mjs first");
  }

  return [...platforms, "cli"];
}

// distTag is the npm tag a version is published under: a pre-release never becomes latest.
export function distTag(version) {
  return version.includes("-") ? "next" : "latest";
}

// readManifest reads the package.json a built package carries.
function readManifest(dir) {
  return JSON.parse(readFileSync(path.join(dir, "package.json"), "utf8"));
}

// checkBinary runs this machine's binary and refuses one that reports another version than the packages.
function checkBinary(out, version) {
  const target = `${goos[process.platform]}-${goarch[process.arch]}`;
  const binary = path.join(out, `cli-${target}`, "bin", process.platform === "win32" ? "migr.exe" : "migr");
  const reported = JSON.parse(execFileSync(binary, ["version", "--json"], { encoding: "utf8" })).version;

  if (reported !== `v${version}`) {
    throw new Error(`${binary} reports ${reported}, but the packages are ${version}`);
  }

  process.stdout.write(`${binary} reports ${reported}\n`);
}

// published reports whether the registry already holds this name at this version.
function published(name, version) {
  try {
    return execFileSync("npm", ["view", `${name}@${version}`, "version"], { encoding: "utf8" }).trim() === version;
  } catch {
    return false;
  }
}

// publish sends every package in order, or rehearses it under --dry-run.
function publish(out, dryRun) {
  const order = publishOrder(readdirSync(out));
  const { version } = readManifest(path.join(out, "cli"));

  checkBinary(out, version);

  for (const dir of order) {
    const { name, version: own } = readManifest(path.join(out, dir));
    if (own !== version) {
      throw new Error(`${name} is ${own} but @migr/cli is ${version}`);
    }

    if (!dryRun && published(name, version)) {
      process.stdout.write(`${name}@${version} is already published, skipped\n`);
      continue;
    }

    const args = ["publish", path.join(out, dir), "--access", "public", "--tag", distTag(version)];
    execFileSync("npm", dryRun ? [...args, "--dry-run"] : [...args, "--provenance"], { stdio: "inherit" });
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [out, ...flags] = process.argv.slice(2);
  if (out === undefined || flags.some((f) => f !== "--dry-run")) {
    process.stderr.write("usage: node npm/cli/publish.mjs <out> [--dry-run]\n");
    process.exit(2);
  }

  try {
    publish(out, flags.includes("--dry-run"));
  } catch (err) {
    process.stderr.write(`publish.mjs: ${err.message}\n`);
    process.exit(1);
  }
}
