// Package node is the runtime adapter for migrations written in JavaScript and
// TypeScript.
//
// It discovers a Node installation, resolves node_modules from the project
// root, and spawns the migr-runtime-node binary shipped with @migr/sdk.
// TypeScript is transpiled through esbuild/tsx so users need no build step of
// their own.
//
// Status: scaffolding. Implemented in M7-T6 (see TASKS.md).
package node
