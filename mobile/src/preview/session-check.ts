/** A full, wide desktop screen; its final row must remain visible on a narrow phone. */
export function sessionCheckFrame(update: number): string {
  return Array.from({ length: 55 }, (_, row) => `CAPTURE ROW ${row + 1}: ` + 'Wide desktop text wraps into multiple phone lines. '.repeat(4))
    .join('\n') + `\nLATEST OUTPUT CHECK · update ${update}`;
}
