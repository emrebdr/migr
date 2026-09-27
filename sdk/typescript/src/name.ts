// This SDK's copy of the migration filename grammar.
//
// It is a copy on purpose: importing the engine's would make this package require the CLI module,
// which is what a separate package exists to prevent. The grammar is frozen by every migration
// anybody has written, so the duplication is the cheap kind (ADR 0037, B).
//
// Where the Go SDK recovers a filename with runtime.Caller, this one is handed it: a module is a
// file, so the loader knows the path before it knows the migration. Nothing here can disagree with
// what the engine discovered, and there is no version for an author to mistype.

import { MigrationError } from "./error.ts";

/** The reference shape of the timestamp a migration filename carries, for a message a human reads. */
const VERSION_SHAPE = "YYYYMMDDThhmmss";

/** Divides a migration's version from its name, and its name's own words from each other. */
const SEPARATOR = "_";

/** EXTENSIONS are the files this SDK loads, which are the ones migr-runtime-node can execute. */
export const EXTENSIONS = [".ts", ".js", ".mjs"] as const;

/** The shape of every migration filename, as a diagnostic states it. */
const GRAMMAR = `<${VERSION_SHAPE}>${SEPARATOR}<snake_case name>(${EXTENSIONS.join("|")})`;

/** The digits a version is made of, before it is checked against the calendar. */
const STAMP = /^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})$/;

/** Named is what a filename says about the migration it holds. */
export interface Named {
  /** The UTC timestamp the filename carries, which every message and history row uses. */
  readonly version: string;
  /** The snake_case name, for a message a human reads. */
  readonly name: string;
  /** The filename itself, which migration.discover reports. */
  readonly file: string;
}

/** parseName reads a migration filename, or reports what about it is not one. */
export function parseName(file: string): Named {
  const extension = extensionOf(file);

  if (!(EXTENSIONS as readonly string[]).includes(extension)) {
    throw malformed(file, `it ends in ${describeExtension(extension)}`);
  }

  const stem = file.slice(0, file.length - extension.length);
  const cut = stem.indexOf(SEPARATOR);

  if (cut < 0) {
    throw malformed(file, `it has no ${SEPARATOR} separating a version from a name`);
  }

  const version = stem.slice(0, cut);
  const name = stem.slice(cut + SEPARATOR.length);

  checkVersion(file, version);
  checkName(file, name);

  return { version, name, file };
}

/** foreign reports a well-formed migration name whose extension belongs to another runtime. */
export function foreign(file: string): boolean {
  const extension = extensionOf(file);

  if (extension === "" || (EXTENSIONS as readonly string[]).includes(extension)) {
    return false;
  }

  try {
    parseName(file.slice(0, file.length - extension.length) + EXTENSIONS[0]);
  } catch {
    return false;
  }

  return true;
}

/** extensionOf returns what a filename ends in, which is empty when it ends in nothing. */
function extensionOf(file: string): string {
  const dot = file.lastIndexOf(".");

  return dot <= 0 ? "" : file.slice(dot);
}

/** describeExtension renders what a filename ended in, saying so plainly when it ended in nothing. */
function describeExtension(extension: string): string {
  return extension === "" ? "no extension at all" : quote(extension);
}

/** checkVersion reports a version that is not the UTC timestamp the grammar requires. */
function checkVersion(file: string, version: string): void {
  if (version === "") {
    throw malformed(file, `it has no version before its first ${SEPARATOR}`);
  }

  const stamp = stampOf(version);

  if (stamp === undefined) {
    throw malformed(file, `${quote(version)} is not a ${VERSION_SHAPE} UTC timestamp`);
  }

  if (!isInstant(stamp)) {
    throw malformed(
      file,
      `${quote(version)} is shaped like ${VERSION_SHAPE} but is not a UTC instant`,
    );
  }
}

/** Stamp is one version read as the six numbers it is made of. */
interface Stamp {
  readonly year: number;
  readonly month: number;
  readonly day: number;
  readonly hour: number;
  readonly minute: number;
  readonly second: number;
}

/** stampOf reads a version's six fields, or reports that it is not shaped like one. */
function stampOf(version: string): Stamp | undefined {
  const parts = STAMP.exec(version);

  if (parts === null) {
    return undefined;
  }

  return {
    year: Number(parts[1]),
    month: Number(parts[2]),
    day: Number(parts[3]),
    hour: Number(parts[4]),
    minute: Number(parts[5]),
    second: Number(parts[6]),
  };
}

/** isInstant reports whether six fields name a moment the calendar has, so 20260230 is refused. */
function isInstant(stamp: Stamp): boolean {
  const at = new Date(0);

  at.setUTCFullYear(stamp.year, stamp.month - 1, stamp.day);
  at.setUTCHours(stamp.hour, stamp.minute, stamp.second, 0);

  return (
    at.getUTCFullYear() === stamp.year &&
    at.getUTCMonth() === stamp.month - 1 &&
    at.getUTCDate() === stamp.day &&
    at.getUTCHours() === stamp.hour &&
    at.getUTCMinutes() === stamp.minute &&
    at.getUTCSeconds() === stamp.second
  );
}

/** checkName reports a name that is not snake_case: lowercase words joined by single separators. */
function checkName(file: string, name: string): void {
  if (name === "") {
    throw malformed(file, "it has no name after its version");
  }

  for (const word of name.split(SEPARATOR)) {
    if (word === "") {
      throw malformed(
        file,
        `its name ${quote(name)} has a leading, trailing, or doubled ${SEPARATOR}`,
      );
    }

    const bad = firstNotLowerAlnum(word);

    if (bad !== undefined) {
      throw malformed(
        file,
        `its name ${quote(name)} carries ${quote(bad)}, and a name is lowercase letters and digits`,
      );
    }
  }
}

/** firstNotLowerAlnum returns the first character a snake_case word may not carry. */
function firstNotLowerAlnum(word: string): string | undefined {
  for (const character of word) {
    if (!/^[a-z0-9]$/.test(character)) {
      return character;
    }
  }

  return undefined;
}

/** malformed reports a filename the grammar refuses, naming the grammar it was held to. */
function malformed(file: string, why: string): MigrationError {
  return new MigrationError("malformed_name", file, `${file} is not ${GRAMMAR}, because ${why}`);
}

/** quote renders a value inside a message without letting it be mistaken for the message. */
function quote(value: string): string {
  return JSON.stringify(value);
}
