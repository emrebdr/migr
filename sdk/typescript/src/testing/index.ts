// @migr/sdk/testing -- the three helpers for testing a migration written against this SDK.
//
// Its own subpath rather than part of ".", so the library a migration imports does not carry test
// helpers, and neither does what migr-runtime-node loads. The one thing it imports from the package
// is the package itself, which the build leaves external: the shipped bundle is a few hundred bytes
// beside the library rather than a second copy of it.

export {
  answer,
  capture,
  captureDown,
  documents,
  PLACEHOLDER,
  reversal,
  UnstagedError,
} from "./capture.ts";
export type { Capture, Document, Line, Reversed, Stage, Statement } from "./capture.ts";

export { ambient, BINARY_NAME, BINARY_VAR, found, NoBinaryError } from "./binary.ts";
export type { Environment } from "./binary.ts";

export { CACHE_DIR, project, render, table } from "./project.ts";
export type {
  Applied,
  Counts,
  Entry,
  Failure,
  Finding,
  Options,
  Project,
  Report,
  Run,
  Skipped,
  Status,
  Unchecked,
} from "./project.ts";
