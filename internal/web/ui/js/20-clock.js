/* 20-clock.js: SL.time, the page's wall clock and the rate of the view clock.
 *
 * Two clocks exist per session: the WORLD clock (`S.wt`, events happen on it; 1 s per wall second, moved forward by the server's
 * time) and the VIEW clock (`S.vt`, what the screen shows). Live, the view clock is the world clock: each frame moves it to the world
 * clock (50-sessions.js, follow), so an event is drawn in the first frame after it arrived, wherever the pointer or the focus is,
 * and a tab that was hidden draws everything it missed in its first frame back. In a replay the view runs at the replay's speed,
 * and the RATE eases toward that speed (0 while the replay is paused; 1 live):
 *
 *   dtView = dtWall * rate        tau 150 ms when slowing, 250 ms when speeding up
 *
 * What moves (leg cycles, rings, gantt scroll, arcs, token tickers, streaming text, CSS keyframes) is driven by dtView, so a paused
 * replay stands still and a fast one runs fast. Wall time is used for what is about the person: the quiet period before an approval
 * takes keys, the window of a chord, toasts. */
(function (SL) {
  'use strict';
  const C = SL.TIME_CONST = { TAU_DOWN: 0.15, TAU_UP: 0.25 };

  const T = {
    wall: 0,            // virtual wall clock in ms: advanced only by SL.loop.step (so a test can warp it)
    rate: 1, target: 1,
    replay: null,       // {playing, speed} while the view is a replay
    quietSince: 0,      // wall ms of the last key press: the approvals' quiet period reads it
  };

  function noteKey() { T.quietSince = T.wall; }

  /** Advance the rate by dtWall seconds toward 1 (live) or the replay's speed (0 while it is paused); returns dtView. */
  function tick(dtWall, replay) {
    T.replay = replay || null;
    const target = replay ? (replay.playing ? replay.speed : 0) : 1;
    T.target = target;
    T.rate += (target - T.rate) * (1 - Math.exp(-dtWall / (target < T.rate ? C.TAU_DOWN : C.TAU_UP)));
    if (target === 0 && T.rate < 0.01) T.rate = 0;
    return dtWall * T.rate;
  }

  /** Reset the rate (new page state, tests). */
  function reset() { T.rate = 1; T.target = 1; T.replay = null; }

  SL.time = {
    T, C, tick, noteKey, reset,
    get rate() { return T.rate; }, get wall() { return T.wall; },
    /** Wall seconds since the last key press (the approvals' quiet period). */
    quiet() { return (T.wall - T.quietSince) / 1000; },
  };
})(SL);
