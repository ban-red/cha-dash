// happy-dom's Web Animations rejects `finished` when motion cancels an
// animation, which surfaces as unhandled AbortErrors. Without element.animate,
// motion falls back to its own frame loop, which is all the tests need.
delete (Element.prototype as { animate?: unknown }).animate;
