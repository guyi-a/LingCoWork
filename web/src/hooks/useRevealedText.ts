import { useEffect, useRef, useState } from "react";

// useRevealedText paces how much of an already-arrived string is actually shown.
//
// The authoritative message content (segment.content in the chat store) is kept
// in full — this hook only controls the *display* window. Network jitter makes
// the SSE frames arrive in bursts; a steady reveal rate hides that so the text
// always types out at a consistent speed instead of jumping with each packet.
//
// Behaviour (all tunable via the constants below):
//   - Steady base rate while the backlog is small (the "typewriter" feel).
//   - Moderate backlog: speed up so the display keeps pace with the model.
//   - Large backlog: snap to the model's current position so it never lags far.
//   - Inactive (segment no longer live), done, or prefers-reduced-motion:
//     reveal everything immediately and stop the loop.
//
// On mount we start the reveal at 0 so a freshly created segment types from
// the top, including the first chunk. (A segment is created and given its
// first text in the same store update, so initialising to text.length would
// skip that chunk.) The trade-off is a resumed run re-types the text that was
// already on screen before a page reload — rare, and arguably a fair replay.
const BASE_CPS = 60; // chars/second steady typewriter rate
const CATCH_UP_THRESHOLD = 1500; // chars of backlog that switches to catch-up rate
const FAST_CPS = BASE_CPS * 3; // catch-up rate while backlog is moderate
const SNAP_THRESHOLD = 8000; // chars of backlog that snaps to the model now

function reducedMotion(): boolean {
  if (typeof window === "undefined" || !window.matchMedia) return false;
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

export function useRevealedText(
  text: string,
  active: boolean,
  onGrow?: () => void,
): string {
  const [revealed, setRevealed] = useState(0);
  const revealedRef = useRef(revealed);
  const acc = useRef(0); // fractional chars owed, so the rate is real (not 1/frame)
  const textRef = useRef(text);
  textRef.current = text;
  const onGrowRef = useRef(onGrow);
  onGrowRef.current = onGrow;

  useEffect(() => {
    if (!active || reducedMotion()) {
      revealedRef.current = text.length;
      setRevealed(text.length);
      return;
    }

    let raf = 0;
    let last = performance.now();
    acc.current = 0;

    const tick = (now: number) => {
      if (reducedMotion()) {
        revealedRef.current = textRef.current.length;
        setRevealed(textRef.current.length);
        return;
      }
      const target = textRef.current.length;
      const cur = revealedRef.current;
      const backlog = target - cur;
      if (backlog > 0) {
        if (backlog >= SNAP_THRESHOLD) {
          // Too far behind — align to where the model actually is.
          revealedRef.current = target;
          setRevealed(target);
          onGrowRef.current?.();
        } else {
          let rate = BASE_CPS;
          if (backlog >= CATCH_UP_THRESHOLD) rate = FAST_CPS;
          acc.current += ((now - last) / 1000) * rate;
          const whole = Math.floor(acc.current);
          if (whole > 0) {
            const k = Math.min(whole, backlog);
            acc.current -= k;
            if (k > 0) {
              revealedRef.current = cur + k;
              setRevealed(cur + k);
              onGrowRef.current?.();
            }
          }
        }
      }
      last = now;
      raf = requestAnimationFrame(tick);
    };

    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [active]);

  return text.slice(0, revealed);
}
