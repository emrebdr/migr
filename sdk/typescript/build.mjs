// Builds @migr/sdk to both module formats, each beside the declarations that describe it.
//
// esbuild emits the bundles and tsc emits the types, which is the split the package already pays
// for: M7-T5 ships esbuild as a runtime dependency to transpile a user's own .ts migrations, so
// nothing here is a dependency the package would not carry anyway (ADR 0041).

import { execFileSync } from "node:child_process";
import { cpSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { build } from "esbuild";

const root = dirname(fileURLToPath(import.meta.url));
const dist = join(root, "dist");
const entry = join(root, "src", "index.ts");

// One directory per module format, each declaring its own flavour in a package.json of its own.
//
// That marker is what makes a plain .d.ts unambiguous. Copying one .d.ts into a package whose type
// is "module" and calling it CJS types is the masquerading case -- a require() consumer would be
// handed types describing a module it is not loading. A directory that says what it holds cannot
// have that problem, and it needs no second declaration build to say it.
const targets = [
  { dir: join(dist, "esm"), format: "esm", type: "module" },
  { dir: join(dist, "cjs"), format: "cjs", type: "commonjs" },
];

// declarationsIn is the directory tsc emits into, which every other target is copied from.
const declarationsIn = targets[0].dir;

// emitDeclarations runs tsc over the build config, which excludes the tests from what ships.
function emitDeclarations() {
  execFileSync(
    process.execPath,
    [
      join(root, "node_modules", "typescript", "bin", "tsc"),
      "--project",
      join(root, "tsconfig.build.json"),
      "--outDir",
      declarationsIn,
    ],
    { stdio: "inherit" },
  );
}

// bundle emits one module format from the single entry point.
function bundle(target) {
  return build({
    entryPoints: [entry],
    outfile: join(target.dir, "index.js"),
    format: target.format,
    bundle: true,
    platform: "node",
    target: "node24",
    sourcemap: true,
    logLevel: "warning",
  });
}

// markFormat writes the package.json that fixes how Node and TypeScript read one directory.
function markFormat(target) {
  writeFileSync(
    join(target.dir, "package.json"),
    JSON.stringify({ type: target.type }, null, 2) + "\n",
  );
}

// shareDeclarations gives every other format the declarations tsc emitted once.
function shareDeclarations() {
  for (const target of targets) {
    if (target.dir !== declarationsIn) {
      cpSync(declarationsIn, target.dir, { recursive: true, filter: isDeclaration });
    }
  }
}

// isDeclaration keeps the copy to the type files, since each format brings its own bundle.
function isDeclaration(source) {
  return !source.endsWith(".js") && !source.endsWith(".js.map");
}

rmSync(dist, { recursive: true, force: true });

for (const target of targets) {
  mkdirSync(target.dir, { recursive: true });
}

emitDeclarations();
await Promise.all(targets.map(bundle));
shareDeclarations();

for (const target of targets) {
  markFormat(target);
}
