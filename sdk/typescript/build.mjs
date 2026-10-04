// Builds @migr/sdk to both module formats, each beside the declarations that describe it, and the
// executable it installs as migr-runtime-node.
//
// esbuild emits the bundles and tsc emits the types, which is the split the package already pays
// for: it ships esbuild as a runtime dependency to transpile a user's own .ts migrations, so nothing
// here is a dependency the package would not carry anyway (ADR 0041). That dependency is external to
// both bundles -- it is loaded dynamically, from the hook that needs it, and bundling a transpiler
// into a library is not what "bundle" is for.

import { execFileSync } from "node:child_process";
import {
  chmodSync,
  cpSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { build } from "esbuild";

const root = dirname(fileURLToPath(import.meta.url));
const dist = join(root, "dist");
const entry = join(root, "src", "index.ts");

// The test helpers, bundled apart from the library and importing it rather than copying it.
//
// Its one non-builtin import is "../index.ts", marked external so the library stays where it is: the
// emitted file is a few hundred bytes. Node's self-reference ("@migr/sdk" from inside the package)
// cannot do this job -- each format directory carries a package.json declaring its type, and that
// manifest shadows the real one, so Node resolves no such package. A relative specifier does work in
// both formats, and finishing the .ts -> .js rewrite for it is the same rewrite the declarations
// already get. It emits into a directory of its own so that "../index.js" means dist/<format>/index.js.
const helpers = {
  entry: join(root, "src", "testing", "index.ts"),
  out: join("testing", "index.js"),
  external: ["../index.ts"],
};

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

// The executable this package installs as migr-runtime-node, which is a shebang and three lines.
//
// It is generated rather than committed for the same reason each format's package.json is: dist/ is
// the answer to what ships. A second bundled entry point would put a whole second copy of the
// library in the package, and there is nothing here worth typechecking -- it names one export and
// exits with what that returns. process.test.ts drives this file itself.
const runtime = {
  name: "migr-runtime-node.js",
  dir: targets[0].dir,
  source: `#!/usr/bin/env node
import { main } from "./index.js";

process.exit(await main());
`,
};

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

// A relative module specifier in a declaration file, which tsc leaves spelled the way it was written.
//
// The sources import each other with .ts because that is the only thing Node resolves when it runs
// them, and running them is how this package is tested with no transpiler in the loop.
// rewriteRelativeImportExtensions turns those into .js in emitted JavaScript -- and does not reach
// declaration output, where a .ts specifier is one a consumer cannot resolve at all. So the build
// finishes the rewrite tsc started, and dist.test.ts holds every shipped declaration to it.
const DECLARED_SPECIFIER = /(from\s*|import\s*\(\s*)(['"])(\.\.?\/[^'"]*)\.ts\2/g;

// The same specifier in emitted JavaScript, which also reaches a require() the CJS bundle writes.
const EMITTED_SPECIFIER = /(from\s*|require\s*\(\s*|import\s*\(\s*)(['"])(\.\.?\/[^'"]*)\.ts\2/g;

// rewriteDeclarations points every declaration at the JavaScript beside it rather than at a source.
function rewriteDeclarations() {
  for (const path of declarationsUnder(declarationsIn)) {
    writeFileSync(path, readFileSync(path, "utf8").replace(DECLARED_SPECIFIER, "$1$2$3.js$2"));
  }
}

// declarationsUnder walks a directory tree, since one source directory makes one output directory.
function declarationsUnder(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);

    if (entry.isDirectory()) {
      return declarationsUnder(path);
    }

    return entry.name.endsWith(".d.ts") ? [path] : [];
  });
}

// bundle emits one module format from the single entry point.
function bundle(target) {
  return build({
    entryPoints: [entry],
    outfile: join(target.dir, "index.js"),
    format: target.format,
    bundle: true,
    external: ["esbuild"],
    platform: "node",
    target: "node24",
    sourcemap: true,
    logLevel: "warning",
  });
}

// bundleHelpers emits the testing subpath for one format, with the library left external.
async function bundleHelpers(target) {
  await build({
    entryPoints: [helpers.entry],
    outfile: join(target.dir, helpers.out),
    format: target.format,
    bundle: true,
    external: [...helpers.external, "esbuild"],
    platform: "node",
    target: "node24",
    sourcemap: true,
    logLevel: "warning",
  });

  rewriteSpecifiers(join(target.dir, helpers.out));
}

// rewriteSpecifiers points an emitted file's external .ts import at the JavaScript beside it.
function rewriteSpecifiers(path) {
  writeFileSync(
    path,
    readFileSync(path, "utf8").replace(EMITTED_SPECIFIER, (_, lead, quote, specifier) => {
      return `${lead}${quote}${specifier}.js${quote}`;
    }),
  );
}

// writeRuntime puts the executable beside the ESM bundle it loads, and marks it executable.
function writeRuntime() {
  const path = join(runtime.dir, runtime.name);

  writeFileSync(path, runtime.source);
  chmodSync(path, 0o755);
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
rewriteDeclarations();
await Promise.all(targets.map(bundle));
await Promise.all(targets.map(bundleHelpers));
shareDeclarations();
writeRuntime();

for (const target of targets) {
  markFormat(target);
}
