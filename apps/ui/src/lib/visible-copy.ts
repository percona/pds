import ts from "typescript";

/**
 * Customer-visible copy, read from source with the TypeScript compiler so
 * comments, identifiers, imports and route strings are left out.
 *
 * A later check on the landing page can import this module. It lives in the
 * console package because that is where the compiler API is already installed.
 */
const textProps = new Set([
  "title",
  "description",
  "label",
  "aria-label",
  "placeholder",
  "alt",
  "children",
  "help",
  "detail",
  "note",
  "problem",
  "empty",
  "consequence",
  "message",
  "kind",
]);

const bannedNames = /\bPMM\b|\bFlux\b|\b[Tt]he (hub|adapter)\b|\b[Ss]pokes?\b/g;
const clusterWord = /\bclusters?\b/gi;

export function visibleCopyTexts(source: string, fileName = "fixture.tsx"): string[] {
  const kind = fileName.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const sourceFile = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, kind);
  const texts: string[] = [];

  const visit = (node: ts.Node): void => {
    if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) return;

    if (ts.isJsxText(node)) {
      push(texts, node.text);
      return;
    }

    if (ts.isJsxAttribute(node)) {
      // A command offered for copying keeps flags such as --cluster. It is not copy.
      if (isCommandAttribute(node)) return;
      const name = node.name.getText(sourceFile);
      if (textProps.has(name) && node.initializer) {
        if (ts.isStringLiteral(node.initializer)) {
          push(texts, node.initializer.text);
        } else if (ts.isJsxExpression(node.initializer) && node.initializer.expression) {
          collectLiteral(node.initializer.expression, texts);
        }
      }
      return;
    }

    if (ts.isJsxExpression(node) && node.expression && isJsxChild(node)) {
      collectLiteral(node.expression, texts);
    }

    if (ts.isPropertyAssignment(node)) {
      const name = propertyName(node.name);
      if (name && textProps.has(name)) collectLiteral(node.initializer, texts);
    }

    if (ts.isVariableDeclaration(node) && node.initializer) {
      collectProse(node.initializer, texts);
    }

    if (ts.isReturnStatement(node) && node.expression) {
      collectProse(node.expression, texts);
    }

    if (ts.isArrowFunction(node) && node.body && !ts.isBlock(node.body)) {
      collectProse(node.body, texts);
    }

    if (ts.isCallExpression(node) && isToastCall(node)) {
      const first = node.arguments[0];
      if (first) collectLiteral(first, texts);
    }

    if (
      (ts.isNewExpression(node) || ts.isCallExpression(node)) &&
      isErrorConstruct(node.expression)
    ) {
      const first = node.arguments?.[0];
      if (first) collectLiteral(first, texts);
    }

    if (
      ts.isBinaryExpression(node) &&
      node.operatorToken.kind === ts.SyntaxKind.EqualsToken &&
      isDocumentTitle(node.left)
    ) {
      collectLiteral(node.right, texts);
    }

    ts.forEachChild(node, visit);
  };

  visit(sourceFile);
  return texts;
}

/** `<title>` in an HTML document. The console sets no document.title in script. */
export function htmlDocumentTitles(source: string): string[] {
  return [...source.matchAll(/<title>([^<]*)<\/title>/gi)]
    .map((match) => match[1]?.trim() ?? "")
    .filter((title) => title !== "");
}

export function bannedNameHits(texts: string[]): string[] {
  return texts.flatMap((text) => text.match(new RegExp(bannedNames.source, "g")) ?? []);
}

/**
 * Bare cluster or clusters, in any case. Kubernetes cluster(s) and EKS
 * cluster(s) name infrastructure. A hyphen, dot or slash before the word is an
 * identifier such as create-cluster, pds-cluster or kubernetes.io/cluster.
 */
export function bareClusterHits(texts: string[]): string[] {
  const hits: string[] = [];
  for (const text of texts) {
    for (const match of text.matchAll(new RegExp(clusterWord.source, "gi"))) {
      const index = match.index ?? 0;
      if (/(?:kubernetes|eks)\s+$/i.test(text.slice(0, index))) continue;
      const previous = text[index - 1];
      if (previous && /[-./]/.test(previous)) continue;
      hits.push(match[0]);
    }
  }
  return hits;
}

function push(texts: string[], value: string): void {
  const text = value.replace(/\s+/g, " ").trim();
  if (text !== "") texts.push(text);
}

function collectLiteral(node: ts.Node, texts: string[]): void {
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
    push(texts, node.text);
    return;
  }
  if (ts.isTemplateExpression(node)) {
    const parts = [node.head.text, ...node.templateSpans.map((span) => span.literal.text)];
    push(texts, parts.join(""));
    return;
  }
  if (
    ts.isParenthesizedExpression(node) ||
    ts.isAsExpression(node) ||
    ts.isSatisfiesExpression(node) ||
    ts.isTypeAssertionExpression(node) ||
    ts.isNonNullExpression(node)
  ) {
    collectLiteral(node.expression, texts);
    return;
  }
  if (ts.isConditionalExpression(node)) {
    collectLiteral(node.whenTrue, texts);
    collectLiteral(node.whenFalse, texts);
    return;
  }
  if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.PlusToken) {
    collectLiteral(node.left, texts);
    collectLiteral(node.right, texts);
    return;
  }
  if (ts.isArrayLiteralExpression(node)) {
    for (const element of node.elements) {
      if (ts.isSpreadElement(element)) continue;
      collectLiteral(element, texts);
    }
  }
}

/**
 * Prose in a binding or a return. Paths and one-word tokens are not sentences.
 * A title-case "Cluster" is still a label a customer reads, so it is kept.
 * A lowercase "cluster" is an API value and stays out.
 */
function collectProse(node: ts.Node, texts: string[]): void {
  const found: string[] = [];
  collectLiteral(node, found);
  collectClusterLabels(node, found);
  for (const text of found) {
    if (looksLikeRoute(text)) continue;
    if (!/\s/.test(text) && !/^(?:Cluster|Clusters)$/.test(text)) continue;
    texts.push(text);
  }
}

function collectClusterLabels(node: ts.Node, texts: string[]): void {
  const visit = (current: ts.Node): void => {
    if (ts.isObjectLiteralExpression(current)) {
      for (const property of current.properties) {
        if (!ts.isPropertyAssignment(property)) continue;
        const value = property.initializer;
        if (
          (ts.isStringLiteral(value) || ts.isNoSubstitutionTemplateLiteral(value)) &&
          /^(?:Cluster|Clusters)$/.test(value.text)
        ) {
          texts.push(value.text);
        }
      }
    }
    ts.forEachChild(current, visit);
  };
  visit(node);
}

function looksLikeRoute(text: string): boolean {
  const trimmed = text.trim();
  return trimmed.startsWith("/") || trimmed.startsWith("http://") || trimmed.startsWith("https://");
}

function isJsxChild(node: ts.JsxExpression): boolean {
  const parent = node.parent;
  return ts.isJsxElement(parent) || ts.isJsxFragment(parent);
}

function isCommandAttribute(node: ts.JsxAttribute): boolean {
  if (!ts.isIdentifier(node.name) || node.name.text !== "command") return false;
  const element = node.parent.parent;
  if (!ts.isJsxOpeningElement(element) && !ts.isJsxSelfClosingElement(element)) return false;
  const tag = element.tagName;
  return ts.isIdentifier(tag) && tag.text === "CommandBlock";
}

function propertyName(name: ts.PropertyName): string | undefined {
  if (ts.isIdentifier(name) || ts.isStringLiteral(name) || ts.isNumericLiteral(name)) {
    return name.text;
  }
  return undefined;
}

function isToastCall(node: ts.CallExpression): boolean {
  const expr = node.expression;
  if (ts.isIdentifier(expr)) return expr.text === "toast";
  return (
    ts.isPropertyAccessExpression(expr) &&
    ts.isIdentifier(expr.expression) &&
    expr.expression.text === "toast"
  );
}

function isErrorConstruct(expr: ts.Expression): boolean {
  return ts.isIdentifier(expr) && expr.text === "Error";
}

function isDocumentTitle(expr: ts.Expression): boolean {
  return (
    ts.isPropertyAccessExpression(expr) &&
    expr.name.text === "title" &&
    ts.isIdentifier(expr.expression) &&
    expr.expression.text === "document"
  );
}
