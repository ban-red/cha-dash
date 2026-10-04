/** The slice of `@chadash/tokens` (plain JS with JSDoc) the tests use. */
declare module '@chadash/tokens' {
  export const MOTION: Readonly<Record<'press' | 'move' | 'lift' | 'sheet', { readonly duration: number; readonly bounce: number }>>;
}
