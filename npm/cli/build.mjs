// Turns a goreleaser dist/ into the seven npm packages of one release: @migr/cli, and one
// @migr/cli-<os>-<arch> per binary, every one at the release's version and the wrapper pinning each
// exactly. Run as `node npm/cli/build.mjs <dist> <out>`.

import { chmodSync, copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.join(here, "..", "..");

const nodeOS = { darwin: "darwin", linux: "linux", windows: "win32" };
const nodeCPU = { amd64: "x64", arm64: "arm64" };

const targets = Object.keys(nodeOS).flatMap((os) => Object.keys(nodeCPU).map((arch) => `${os}-${arch}`));

// readJSON parses one file, naming it when it cannot be read.
function readJSON(file) {
  try {
    return JSON.parse(readFileSync(file, "utf8"));
  } catch (err) {
    throw new Error(`reading ${file}: ${err.message}`, { cause: err });
  }
}

// binaries returns the release's binaries keyed by target, refusing any set but exactly the six.
export function binaries(artifacts) {
  const found = new Map();

  for (const a of artifacts.filter((a) => a.type === "Binary")) {
    const target = `${a.goos}-${a.goarch}`;
    if (!targets.includes(target)) {
      throw new Error(`the release built ${target}, which no npm package is defined for`);
    }

    if (found.has(target)) {
      throw new Error(`the release built ${target} twice`);
    }

    found.set(target, a.path);
  }

  const missing = targets.filter((t) => !found.has(t));
  if (missing.length > 0) {
    throw new Error(`the release built no binary for ${missing.join(", ")}, so @migr/cli would install nothing there`);
  }

  return found;
}

// platformManifest is the package.json of the package holding one target's binary.
export function platformManifest(wrapper, target, version) {
  const [os, arch] = target.split("-");

  return {
    name: `@migr/cli-${target}`,
    version,
    description: `The migr binary for ${os}/${arch}, installed by @migr/cli.`,
    license: wrapper.license,
    repository: wrapper.repository,
    homepage: wrapper.homepage,
    os: [nodeOS[os]],
    cpu: [nodeCPU[arch]],
    files: ["bin", "LICENSE"],
  };
}

// wrapperManifest is @migr/cli's package.json at the release's version, pinning every platform package exactly.
export function wrapperManifest(wrapper, version) {
  const { scripts: _scripts, ...published } = wrapper;

  return {
    ...published,
    version,
    optionalDependencies: Object.fromEntries(targets.map((t) => [`@migr/cli-${t}`, version])),
  };
}

// build writes the seven package directories under out from the goreleaser output in dist.
export function build(dist, out) {
  const version = readJSON(path.join(dist, "metadata.json")).version;
  if (typeof version !== "string" || version === "") {
    throw new Error(`${path.join(dist, "metadata.json")} names no version`);
  }

  const found = binaries(readJSON(path.join(dist, "artifacts.json")));
  const wrapper = readJSON(path.join(here, "package.json"));

  rmSync(out, { recursive: true, force: true });

  for (const [target, binary] of found) {
    const dir = path.join(out, `cli-${target}`);
    const name = target.startsWith("windows-") ? "migr.exe" : "migr";

    mkdirSync(path.join(dir, "bin"), { recursive: true });
    copyFileSync(path.resolve(binary), path.join(dir, "bin", name));
    chmodSync(path.join(dir, "bin", name), 0o755);
    copyFileSync(path.join(root, "LICENSE"), path.join(dir, "LICENSE"));
    writeFileSync(path.join(dir, "package.json"), JSON.stringify(platformManifest(wrapper, target, version), null, 2) + "\n");
    writeFileSync(path.join(dir, "README.md"), `The migr binary for ${target}. Install [@migr/cli](https://www.npmjs.com/package/@migr/cli) instead.\n`);
  }

  const dir = path.join(out, "cli");
  mkdirSync(path.join(dir, "bin"), { recursive: true });
  copyFileSync(path.join(here, "bin", "migr.js"), path.join(dir, "bin", "migr.js"));
  chmodSync(path.join(dir, "bin", "migr.js"), 0o755);
  copyFileSync(path.join(here, "README.md"), path.join(dir, "README.md"));
  copyFileSync(path.join(root, "LICENSE"), path.join(dir, "LICENSE"));
  writeFileSync(path.join(dir, "package.json"), JSON.stringify(wrapperManifest(wrapper, version), null, 2) + "\n");

  return { version, packages: [...[...found.keys()].map((t) => `cli-${t}`), "cli"] };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [dist, out] = process.argv.slice(2);
  if (dist === undefined || out === undefined) {
    process.stderr.write("usage: node npm/cli/build.mjs <goreleaser dist> <out>\n");
    process.exit(2);
  }

  try {
    const { version, packages } = build(dist, out);
    process.stdout.write(`${packages.length} packages at ${version} in ${out}:\n${packages.map((p) => `  ${p}\n`).join("")}`);
  } catch (err) {
    process.stderr.write(`build.mjs: ${err.message}\n`);
    process.exit(1);
  }
}
