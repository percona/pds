/**
 * Puts text on the clipboard that is not known yet when the click happens.
 *
 * Reading a password from an environment can take several seconds. Safari and
 * Firefox only allow a clipboard write inside the click gesture, so awaiting
 * the read first and writing after can be refused. Handing the browser the
 * pending value keeps the write attached to the click. Where that API is
 * missing the value is awaited and written the plain way.
 */
export async function copyResolved(read: () => Promise<string>): Promise<void> {
  if (typeof ClipboardItem !== "undefined" && typeof navigator.clipboard?.write === "function") {
    const blob = read().then((text) => new Blob([text], { type: "text/plain" }));
    await navigator.clipboard.write([new ClipboardItem({ "text/plain": blob })]);

    return;
  }

  await navigator.clipboard.writeText(await read());
}
