// The eighteen logical types, each of which is its own canonical spelling.
//
// docs/ir.md §5.1 encodes a type as a string rather than as an object, and §5.3 requires that
// printing always emit the tight form. So a Type here IS that string, branded so a bare string
// cannot be passed where one is wanted. It needs no toJSON and no parser: what can only be
// constructed by these functions can only be spelled one way.
//
// A bad type throws where it was written. The Go SDK defers -- migr.String(0) is a value carrying
// the reason it has no spelling, surfaced at Apply -- because Go could not have a chain
// return an error at each link. A type constructor is not a chain link, so this one need not.
//
// The compile-time half is an addition and never a replacement: a computed argument is invisible
// to the type system, so every rule below is checked at runtime too.

import { OperationError } from "./error.ts";

/** The brand that separates a canonical logical type from any other string. */
declare const LOGICAL: unique symbol;

/** Type is one logical type, always canonically spelled, which each driver maps to a native one. */
export type Type = string & { readonly [LOGICAL]: "logical type" };

/** Refused is what an argument the type system rejects is asked to be, so the message reads. */
type Refused<M extends string> = { readonly __migr_refused: M };

/** Whole rejects a literal that is not an integer, leaving a computed argument to the runtime. */
type Whole<N extends number, M extends string> = number extends N
  ? unknown
  : `${N}` extends `${string}.${string}` | `${string}e${string}`
    ? Refused<M>
    : unknown;

/** Positive additionally rejects a literal that is zero or negative. */
type Positive<N extends number, M extends string> = number extends N
  ? unknown
  : `${N}` extends `-${string}` | "0"
    ? Refused<M>
    : Whole<N, M>;

/** NotNegative additionally rejects a literal below zero. */
type NotNegative<N extends number, M extends string> = number extends N
  ? unknown
  : `${N}` extends `-${string}`
    ? Refused<M>
    : Whole<N, M>;

/** Distinct rejects a label list that names one label twice. */
type Distinct<T extends readonly string[]> = T extends readonly [
  infer Head extends string,
  ...infer Rest extends string[],
]
  ? Head extends Rest[number]
    ? readonly Refused<"enum labels must be distinct">[]
    : Distinct<Rest>
  : readonly unknown[];

/** TYPE_KINDS names every logical type, in the order docs/ir.md §5.2 declares them. */
export const TYPE_KINDS = [
  "text",
  "bool",
  "int16",
  "int32",
  "int64",
  "float32",
  "float64",
  "uuid",
  "json",
  "timestamp",
  "timestamptz",
  "date",
  "time",
  "binary",
  "string",
  "decimal",
  "enum",
  "array",
] as const;

/** MAX_ARRAY_DEPTH bounds array nesting, which §5.4 sets so corrupt input errors rather than recurses. */
const MAX_ARRAY_DEPTH = 32;

/** The characters an enum label is made of, per docs/ir.md §5.2. */
const LABEL = /^[A-Za-z0-9_-]+$/;

/** spelled brands a canonical spelling as the logical type it is. */
function spelled(spelling: string): Type {
  return spelling as Type;
}

/** text is variable-length text with no declared maximum. */
function text(): Type {
  return spelled("text");
}

/** bool is a truth value. */
function bool(): Type {
  return spelled("bool");
}

/** int16 is a 16-bit signed integer. */
function int16(): Type {
  return spelled("int16");
}

/** int32 is a 32-bit signed integer. */
function int32(): Type {
  return spelled("int32");
}

/** int64 is a 64-bit signed integer. */
function int64(): Type {
  return spelled("int64");
}

/** float32 is a single-precision floating point number. */
function float32(): Type {
  return spelled("float32");
}

/** float64 is a double-precision floating point number. */
function float64(): Type {
  return spelled("float64");
}

/** uuid is a universally unique identifier. */
function uuid(): Type {
  return spelled("uuid");
}

/** json is a JSON document the engine stores as one. */
function json(): Type {
  return spelled("json");
}

/** timestamp is a date and time carrying no zone. */
function timestamp(): Type {
  return spelled("timestamp");
}

/** timestamptz is a date and time carrying a zone. */
function timestamptz(): Type {
  return spelled("timestamptz");
}

/** date is a calendar date with no time. */
function date(): Type {
  return spelled("date");
}

/** time is a time of day with no date. */
function time(): Type {
  return spelled("time");
}

/** binary is a string of bytes. */
function binary(): Type {
  return spelled("binary");
}

/** stringType is variable-length text with a maximum length of at least one. */
function stringType<N extends number>(
  length: N & Positive<N, "a string length must be a whole number of at least 1">,
): Type {
  requireWhole(length, "string length");

  if (length < 1) {
    throw new OperationError(`string length must be at least 1, got ${length}`);
  }

  return spelled(`string(${length})`);
}

/** decimal is an exact numeric of the given precision and scale. */
function decimal<P extends number, S extends number>(
  precision: P & Positive<P, "a decimal precision must be a whole number of at least 1">,
  scale: S & NotNegative<S, "a decimal scale must be a whole number of at least 0">,
): Type {
  requireWhole(precision, "decimal precision");
  requireWhole(scale, "decimal scale");

  if (precision < 1) {
    throw new OperationError(`decimal precision must be at least 1, got ${precision}`);
  }

  if (scale < 0) {
    throw new OperationError(`decimal scale must not be negative, got ${scale}`);
  }

  if (scale > precision) {
    throw new OperationError(`decimal scale ${scale} exceeds precision ${precision}`);
  }

  return spelled(`decimal(${precision},${scale})`);
}

/** enumType is one of an ordered list of labels, whose order is part of the type. */
function enumType<const T extends readonly [string, ...string[]]>(
  ...values: T & Distinct<T>
): Type {
  if (values.length === 0) {
    throw new OperationError("enum must have at least one label");
  }

  for (const value of values) {
    if (!LABEL.test(value)) {
      throw new OperationError(
        `enum label ${JSON.stringify(value)} carries something other than letters, digits, _ and -`,
      );
    }
  }

  requireDistinct(values);

  return spelled(`enum(${values.join(",")})`);
}

/** arrayType is a list whose element is itself a full type. */
function arrayType(of: Type): Type {
  if (typeof of !== "string" || of === "") {
    throw new OperationError("array must name the type of its elements");
  }

  if (depthOf(of) + 1 > MAX_ARRAY_DEPTH) {
    throw new OperationError(`array nesting exceeds the limit of ${MAX_ARRAY_DEPTH}`);
  }

  return spelled(`array(${of})`);
}

/** requireWhole reports a parameter that is not a whole number, which no type takes. */
function requireWhole(value: number, subject: string): void {
  if (!Number.isInteger(value)) {
    throw new OperationError(`${subject} must be a whole number, got ${value}`);
  }
}

/** requireDistinct reports an enum naming one label twice, which is two labels or a typo. */
function requireDistinct(values: readonly string[]): void {
  const seen = new Set<string>();

  for (const value of values) {
    if (seen.has(value)) {
      throw new OperationError(`enum lists ${JSON.stringify(value)} more than once`);
    }

    seen.add(value);
  }
}

/** depthOf counts how deeply a type is already nested in arrays, which §5.4 bounds. */
function depthOf(of: string): number {
  let depth = 0;
  let inner = of;

  while (inner.startsWith("array(") && inner.endsWith(")")) {
    depth += 1;
    inner = inner.slice("array(".length, -1);
  }

  return depth;
}

/** t holds the eighteen logical types, so `enum` keeps the spelling docs/ir.md gives it. */
export const t = {
  text,
  bool,
  int16,
  int32,
  int64,
  float32,
  float64,
  uuid,
  json,
  timestamp,
  timestamptz,
  date,
  time,
  binary,
  string: stringType,
  decimal,
  enum: enumType,
  array: arrayType,
} as const;
