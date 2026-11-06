/**
 * Leaves the console for an address outside it. It is its own function so a
 * test can see where a screen sent the browser: jsdom cannot navigate.
 */
export function visit(url: string): void {
  window.location.assign(url);
}
