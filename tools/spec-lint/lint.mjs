#!/usr/bin/env node

// Lint markdown files: mermaid syntax validation + markdownlint rules.
// Usage:
//   node lint.mjs                        # lint all spec .md files
//   node lint.mjs ../01-concepts/*.md    # lint specific files

import fs from "node:fs";
import path from "node:path";
import { glob } from "glob";
import mermaid from "mermaid";
import { lint as markdownlint } from "markdownlint/sync";
import DOMPurify from "dompurify";
import { JSDOM } from "jsdom";

// Set up a minimal DOM environment for mermaid
const jsdom = new JSDOM("...", {
  pretendToBeVisual: true,
});
global.document = jsdom;
global.window = jsdom.window;
global.Option = window.Option;

// Stub DOMPurify — mermaid expects it but we don't need sanitization for linting.
// See https://github.com/mermaid-js/mermaid/issues/5204
DOMPurify.addHook = () => {};
DOMPurify.sanitize = (x) => x;

const mermaidMatch = /```mermaid(.*?)```/gms;
const repoRoot = path.resolve(import.meta.dirname, "../..");

// Collect files: either from CLI args or default glob
const argv = process.argv.slice(2);
let mdFiles;

if (argv.length > 0) {
  mdFiles = argv.flatMap((arg) => {
    const resolved = path.resolve(arg);
    if (fs.existsSync(resolved) && fs.statSync(resolved).isFile()) {
      return [resolved];
    }
    return glob.sync(arg).map((f) => path.resolve(f));
  });
} else {
  mdFiles = await glob("**/*.md", {
    cwd: repoRoot,
    ignore: ["legacy/**", "**/node_modules/**", "tools/spec-lint/**", "PLAN.md", "PLAN_PARTA.md", "DEPLOYMENT_PLAN.md"],
    absolute: true,
  });
}

if (mdFiles.length === 0) {
  console.log("No markdown files found.");
  process.exit(0);
}

console.log(`Checking ${mdFiles.length} markdown file(s)...\n`);

mermaid.initialize({
  startOnLoad: false,
  theme: "neutral",
  flowchart: {
    useMaxWidth: true,
    htmlLabels: true,
  },
  securityLevel: "strict",
});

let errors = 0;
let chartsChecked = 0;

await Promise.all(
  mdFiles.map((filePath) => {
    const data = fs.readFileSync(filePath, "utf8");
    const matched = [...data.matchAll(mermaidMatch)];

    if (matched.length === 0) return Promise.resolve();

    const relPath = path.relative(repoRoot, filePath);

    return Promise.all(
      matched.map((match) => {
        const matchIndex = match.index;
        const mermaidText = match[1];
        chartsChecked++;

        return mermaid.parse(mermaidText).catch((error) => {
          const lineNumber = data.slice(0, matchIndex).split("\n").length;
          const message =
            error.message || error.toString().split("\n").slice(0, 3).join("\n");
          console.log(`  ${relPath}:${lineNumber}: ${message}\n`);
          errors++;
        });
      }),
    );
  }),
);

console.log(`${chartsChecked} mermaid chart(s) checked across ${mdFiles.length} file(s).`);

if (errors > 0) {
  console.log(`${errors} mermaid error(s) found.\n`);
} else {
  console.log("No mermaid errors.\n");
}

// --- markdownlint pass ---

const configPath = path.resolve(import.meta.dirname, ".markdownlint.json");
const config = JSON.parse(fs.readFileSync(configPath, "utf8"));

const mdlintResults = markdownlint({
  files: mdFiles,
  config,
});

let mdlintErrors = 0;

for (const filePath of mdFiles) {
  const fileResults = mdlintResults[filePath];
  if (!fileResults || fileResults.length === 0) continue;

  const relPath = path.relative(repoRoot, filePath);
  for (const result of fileResults) {
    console.log(
      `  ${relPath}:${result.lineNumber}: ${result.ruleNames.join("/")} ${result.ruleDescription}`,
    );
    mdlintErrors++;
  }
}

console.log(`\n${mdlintErrors} markdownlint issue(s) across ${mdFiles.length} file(s).`);

const totalErrors = errors + mdlintErrors;
if (totalErrors > 0) {
  console.log(`\nTotal: ${totalErrors} error(s).`);
  process.exit(1);
} else {
  console.log("\nAll clean.");
}
