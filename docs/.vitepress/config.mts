// The documentation site (M14-T5): VitePress over docs/ as it stands, so the specification
// tests and the generated lock tables keep reading the files the site publishes.
import path from "node:path";
import { defineConfig } from "vitepress";

const repository = "https://github.com/emrebdr/migr";

// outbound points a link that leaves docs/ at the repository on GitHub, where the file it names lives.
function outbound(href: string, page: string): string | undefined {
  if (/^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith("#") || href.startsWith("/")) {
    return undefined;
  }

  const [target, hash = ""] = href.split("#");
  const resolved = path.posix.normalize(path.posix.join("docs", path.posix.dirname(page), target));
  if (resolved.startsWith("docs/")) {
    return undefined;
  }

  return `${repository}/blob/main/${resolved}${hash ? `#${hash}` : ""}`;
}

// literal reports HTML these pages mean as text: a placeholder such as <version> in prose, which Vue
// would otherwise read as an element never closed. Comments, the lock tables' markers, stay HTML.
function literal(html: string): boolean {
  return !/^\s*(<!--|<br\s*\/?>)/i.test(html);
}

export default defineConfig({
  title: "migr",
  description: "Polyglot, database-agnostic migrations, safe on a live database.",
  base: "/migr/",
  cleanUrls: true,
  lastUpdated: false,
  srcExclude: ["**/node_modules/**"],
  markdown: {
    config(md) {
      const code = md.renderer.rules.code_inline!;
      md.renderer.rules.code_inline = (tokens, i, options, env, self) =>
        code(tokens, i, options, env, self).replace(/^<code/, "<code v-pre");

      const text = md.renderer.rules.text!;
      md.renderer.rules.text = (tokens, i, options, env, self) =>
        text(tokens, i, options, env, self).replaceAll("{{", "&#123;&#123;");

      md.core.ruler.push("migr-outbound-links", (state) => {
        const page: string = state.env.relativePath ?? "";
        for (const token of state.tokens) {
          if (token.type === "html_block" && literal(token.content)) {
            token.type = "text";
          }

          for (const child of token.children ?? []) {
            if (child.type === "html_inline" && literal(child.content)) {
              child.type = "text";
            }

            if (child.type !== "link_open") {
              continue;
            }

            const rewritten = outbound(child.attrGet("href") ?? "", page);
            if (rewritten !== undefined) {
              child.attrSet("href", rewritten);
            }
          }
        }
      });
    },
  },
  themeConfig: {
    nav: [
      { text: "Guide", link: "/getting-started" },
      { text: "Drivers", link: "/drivers/postgres" },
      { text: "Zero-downtime", link: "/zero-downtime" },
      { text: "Reference", link: "/ir" },
    ],
    sidebar: [
      {
        text: "Guide",
        items: [
          { text: "Getting started", link: "/getting-started" },
          { text: "Go", link: "/guides/go" },
          { text: "TypeScript", link: "/guides/typescript" },
          { text: "SQL", link: "/guides/sql" },
        ],
      },
      {
        text: "Drivers",
        items: [
          { text: "PostgreSQL", link: "/drivers/postgres" },
          { text: "MySQL and MariaDB", link: "/drivers/mysql" },
          { text: "SQLite", link: "/drivers/sqlite" },
        ],
      },
      { text: "Zero-downtime", items: [{ text: "The zero-downtime guide", link: "/zero-downtime" }] },
      {
        text: "Reference",
        items: [
          { text: "Operation IR", link: "/ir" },
          { text: "Runtime protocol", link: "/protocol" },
        ],
      },
      { text: "Maintainers", items: [{ text: "Releasing", link: "/releasing" }] },
    ],
    search: { provider: "local" },
    socialLinks: [{ icon: "github", link: repository }],
    editLink: { pattern: `${repository}/edit/main/docs/:path`, text: "Edit this page on GitHub" },
    outline: { level: [2, 3] },
  },
});
