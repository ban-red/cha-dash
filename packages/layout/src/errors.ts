/** Machine-readable reasons an operation or a document was refused. */
export type LayoutErrorCode =
  | 'INVALID_BOARD'
  | 'INVALID_DOCUMENT'
  | 'UNSUPPORTED_VERSION'
  | 'INVALID_ARGUMENT'
  | 'DUPLICATE_ID'
  | 'UNKNOWN_ITEM'
  | 'UNKNOWN_SECTION'
  | 'UNSUPPORTED_SIZE'
  | 'NO_FIT'
  | 'NO_SECTION'
  | 'CROSS_SECTION_AT_BREAKPOINT';

/**
 * Thrown for programmer errors (unknown ids, unsupported sizes, malformed
 * documents). Out-of-range drop targets are not errors: they are clamped.
 */
export class LayoutError extends Error {
  readonly code: LayoutErrorCode;

  constructor(code: LayoutErrorCode, message: string) {
    super(message);
    this.name = 'LayoutError';
    this.code = code;
  }
}
