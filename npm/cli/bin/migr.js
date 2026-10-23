#!/usr/bin/env node
"use strict";

// The migr binary, found in the one @migr/cli-<os>-<arch> package npm installed for this machine and
// run with this process's arguments, its exit status and its ending signal passed back (ADR 0100).

const { spawn } = require("node:child_process");
const path = require("node:path");

const goos = { darwin: "darwin", linux: "linux", win32: "windows" };
const goarch = { x64: "amd64", arm64: "arm64" };

const shipped = Object.values(goos).flatMap((os) => Object.values(goarch).map((arch) => `${os}-${arch}`));

// platformPackage names the package holding the binary for a Node platform and architecture.
function platformPackage(platform, arch) {
  const os = goos[platform];
  const cpu = goarch[arch];
  if (os === undefined || cpu === undefined) {
    throw new Error(
      `migr ships no binary for ${platform}/${arch}; it ships for ${shipped.join(", ")}. ` +
        "Build it from source instead: https://github.com/emrebdr/migr#building-from-source",
    );
  }

  return `@migr/cli-${os}-${cpu}`;
}

// locate resolves the installed binary, refusing a missing package or one of another version.
function locate(platform, arch, from) {
  const name = platformPackage(platform, arch);
  const own = require(path.join(from, "..", "package.json"));

  let manifest;
  try {
    manifest = require.resolve(`${name}/package.json`, { paths: [from] });
  } catch {
    throw new Error(
      `${name} is not installed, and it holds the migr binary for ${platform}/${arch}. ` +
        "npm installs it as an optional dependency of @migr/cli, so it is missing where optional " +
        "dependencies were left out (--omit=optional, --no-optional) or where the lockfile was " +
        "written on another platform. Reinstall @migr/cli without either.",
    );
  }

  const installed = require(manifest).version;
  if (installed !== own.version) {
    throw new Error(
      `@migr/cli is ${own.version} but ${name} is ${installed}; the two are released together and ` +
        "must be the same version. Reinstall @migr/cli.",
    );
  }

  return path.join(path.dirname(manifest), "bin", platform === "win32" ? "migr.exe" : "migr");
}

// run starts the binary and ends this process the way the binary ended.
function run(binary, args) {
  const child = spawn(binary, args, { stdio: "inherit" });

  process.on("SIGINT", () => {});
  for (const signal of ["SIGTERM", "SIGHUP"]) {
    process.on(signal, () => child.kill(signal));
  }

  child.on("error", (err) => {
    process.stderr.write(`migr: starting ${binary}: ${err.message}\n`);
    process.exit(1);
  });

  child.on("exit", (code, signal) => {
    if (signal === null) {
      process.exit(code);
    }

    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
  });
}

if (require.main === module) {
  let binary;
  try {
    binary = locate(process.platform, process.arch, __dirname);
  } catch (err) {
    process.stderr.write(`migr: ${err.message}\n`);
    process.exit(1);
  }

  run(binary, process.argv.slice(2));
}

module.exports = { platformPackage, locate, shipped };
