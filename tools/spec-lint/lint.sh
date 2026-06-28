#!/bin/bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

resolve_path() {
    case "$1" in
        /*) echo "$1" ;;
        *) echo "$(cd "$(dirname "$1")" 2>/dev/null && pwd)/$(basename "$1")" ;;
    esac
}

if [ $# -gt 0 ]; then
    MD_FILES=()
    for arg in "$@"; do
        p="$(resolve_path "$arg")"
        [ -f "$p" ] && MD_FILES+=("$p")
    done
else
    cd "$REPO_ROOT"
    MD_FILES=()
    while IFS= read -r; do
        MD_FILES+=("$REPLY")
    done < <(
        find . -name "*.md" \
            -not -path "./legacy/*" \
            -not -path "./tools/spec-lint/*" \
            -not -path "*/node_modules/*" \
            -not -name "PLAN.md" \
            -not -name "PLAN_PARTA.md" \
            -not -name "DEPLOYMENT_PLAN.md" \
            2>/dev/null | sort
    )
fi

if [ ${#MD_FILES[@]} -eq 0 ]; then
    echo "No markdown files found."
    exit 0
fi

echo "Found ${#MD_FILES[@]} markdown file(s)."
echo ""

# --- mermaid validation via temp npm install ---
SCRIPTS=$(mktemp -d)
trap "rm -rf $SCRIPTS" EXIT

cat > "$SCRIPTS/package.json" << 'EOF'
{"private":true,"dependencies":{"mermaid":"^11.0.0","jsdom":"^25.0.0","dompurify":"^3.0.0"}}
EOF

cat > "$SCRIPTS/validate.mjs" << 'NODEEOF'
import fs from "node:fs";
import path from "node:path";
import { JSDOM } from "jsdom";
import DOMPurify from "dompurify";
import mermaid from "mermaid";

const jsdom = new JSDOM("...", { pretendToBeVisual: true });
global.document = jsdom;
global.window = jsdom.window;
global.Option = jsdom.window.Option;

DOMPurify.addHook = () => {};
DOMPurify.sanitize = (x) => x;

mermaid.initialize({
  startOnLoad: false,
  theme: "neutral",
  flowchart: { useMaxWidth: true, htmlLabels: true },
  securityLevel: "strict",
});

const mdFiles = process.argv.slice(2);
let errors = 0;
let chartsChecked = 0;
const re = /```mermaid\n([\s\S]*?)```/g;

for (const filePath of mdFiles) {
  let data;
  try { data = fs.readFileSync(filePath, "utf8"); } catch { continue; }
  const matches = [...data.matchAll(re)];
  if (matches.length === 0) continue;

  const relPath = path.relative(process.cwd(), filePath);
  console.log("  " + relPath + ": " + matches.length + " chart(s)");

  for (const match of matches) {
    chartsChecked++;
    try {
      await mermaid.parse(match[1]);
    } catch (err) {
      const line = data.slice(0, match.index).split("\n").length;
      console.log("  " + relPath + ":" + line + ": " + (err.message || err));
      errors++;
    }
  }
}

console.log("");
console.log(chartsChecked + " mermaid chart(s) checked.");
if (errors > 0) {
  console.log(errors + " mermaid error(s) found.");
  process.exit(1);
}
console.log("No mermaid errors.");
NODEEOF

echo "Validating mermaid diagrams..."
set +e
npm install --no-audit --no-fund --loglevel=error --prefix "$SCRIPTS" 2>&1
INSTALL_EXIT=$?
set -e

MERMAID_EXIT=0
if [ "$INSTALL_EXIT" -ne 0 ]; then
    echo "Warning: mermaid install failed, skipping mermaid validation." >&2
else
    node "$SCRIPTS/validate.mjs" "${MD_FILES[@]}" || MERMAID_EXIT=$?
fi

# --- markdownlint pass ---
echo ""
echo "Running markdownlint..."
CONFIG="$(dirname "$0")/.markdownlint.json"
MDLINT_EXIT=0
npx --yes markdownlint-cli2 --config "$CONFIG" "${MD_FILES[@]}" || MDLINT_EXIT=$?

if [ "$MERMAID_EXIT" -ne 0 ] || [ "$MDLINT_EXIT" -ne 0 ]; then
    echo ""
    echo "Total error(s) found."
    exit 1
fi

echo ""
echo "All clean."
