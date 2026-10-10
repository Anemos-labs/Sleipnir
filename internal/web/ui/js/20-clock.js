/* 20-clock.js: SL.time, the session clocks and the time governor.
 *
 * Two clocks exist per session: the WORLD clock (`S.wt`, always 1 s per wall second, events happen on it) and the VIEW clock
 * (`S.vt`, what the screen shows). The governor owns the ratio between view time and wall time, the RATE:
 *
 *   dtView = dtWall * rate        rate eases toward a target; it is never set directly
 *   target 0      the pointer is over the chat transcript, or keyboard focus is inside it, or the hold is pinned   ("hold")
 *   target 0.3    the pointer is over a cross-highlighting source: stall, leg, gantt row, mail row, task card ...   ("slow")
 *   target 1..6   otherwise 1; above 1 only while the view is catching up with the world (gap = S.wt - S.vt > 0)
 *
 * Easing is exponential: tau 150 ms when slowing (rate < 5% after ~0.5 s), 250 ms when resuming, 100 ms when ramping to catch-up.
 * Everything that moves (events, leg cycles, rings, gantt scroll, arcs, token tickers, streaming text, chat scroll) is driven by dtView,
 * so one number slows, freezes and accelerates the whole screen. Wall time is used only for things that are about the person: the
 * quiet period before an approval takes keys, toasts, the hold chip's own animation.
 *
 * Catch-up on release is bounded in sessions.js (collapse older than CATCHUP_WINDOW seconds, replay the rest at up to MAX_RATE). */
(function (SL) {
  'use strict';
  const { clamp } = SL.u;
  const C = SL.TIME_CONST = { WINDOW: 18, MAX_RATE: 6, SLOW: 0.3, TAU_DOWN: 0.15, TAU_UP: 0.25, TAU_CATCH: 0.1, GAP_ON: 0.12, GAP_SNAP: 0.12, GAP_RAMP: 0.7 };

  const T = {
    wall: 0,            // virtual wall clock in ms: advanced only by SL.loop.step (so a test can warp it)
    rate: 1, target: 1,
    hover: null,        // null | 'chat' | 'linked'
    focus: false,       // keyboard focus inside the chat
    pinned: false,      // the hold is pinned (click the chip, or Space over the chat)
    suppress: false,    // Esc released a hover/focus hold: stays released until the pointer or focus leaves and returns
    mode: 'both',       // setting `Hover behaviour`: 'both' (hold on chat + slow on linked) | 'chat' | 'off'
    gap: 0, catching: false, snap: false,
    held: false,        // the view is frozen (hold wanted and rate ~0)
    replay: null,       // {playing, speed} while the view is a replay (the catch-up logic is off)
    quietSince: 0,      // wall ms of the last key press: the approvals' quiet period reads it
  };

  const holdWanted = () => T.pinned || (T.mode !== 'off' && !T.suppress && (T.hover === 'chat' || T.focus));
  const slowWanted = () => !holdWanted() && T.mode === 'both' && T.hover === 'linked';
  const emitHold = () => SL.bus.emit('hold', { held: T.held, pinned: T.pinned, wanted: holdWanted() });

  /** Pointer position class: 'chat' (over a transcript), 'linked' (over a highlighting source) or null. */
  function setHover(kind) { if (kind !== 'chat' && T.suppress && T.hover === 'chat') T.suppress = false; if (T.hover !== kind) { T.hover = kind; emitHold(); } }
  function setFocus(on) { if (!on && T.suppress && T.focus) T.suppress = false; if (T.focus !== !!on) { T.focus = !!on; emitHold(); } }
  function pin(on) { on = !!on; if (T.pinned !== on) { T.pinned = on; if (on) T.suppress = false; emitHold(); } }
  /** Esc: release every hold. Returns true when something was held (so Esc is consumed). */
  function release() { const was = holdWanted(); if (!was) return false; T.pinned = false; T.suppress = true; emitHold(); return true; }
  function setMode(m) { T.mode = m; emitHold(); }
  function noteKey() { T.quietSince = T.wall; }

  /** Advance the governor by dtWall seconds; returns dtView. `gap` = world minus view seconds of the active session. */
  function tick(dtWall, gap, replay) {
    T.gap = gap; T.replay = replay || null; T.snap = false;
    const hold = holdWanted(), slow = slowWanted();
    let target;
    if (replay) target = replay.playing ? replay.speed : 0;
    else target = 1;
    if (hold) target = 0; else if (slow) target = Math.min(target, C.SLOW);
    if (!replay && !hold && !slow) {
      if (T.catching) {
        if (gap <= C.GAP_SNAP) { T.catching = false; T.snap = gap > 0; }
        else target = 1 + (C.MAX_RATE - 1) * clamp((gap - C.GAP_SNAP) / C.GAP_RAMP, 0, 1);
      } else if (gap > C.GAP_ON) T.catching = true, target = C.MAX_RATE;
    } else T.catching = false;
    T.target = target;
    const tau = target < T.rate ? C.TAU_DOWN : (T.catching ? C.TAU_CATCH : C.TAU_UP);
    T.rate += (target - T.rate) * (1 - Math.exp(-dtWall / tau));
    if (target === 0 && T.rate < 0.01) T.rate = 0;
    const held = hold && T.rate < 0.02;
    if (held !== T.held) { T.held = held; emitHold(); }
    return dtWall * T.rate;
  }

  /** Reset the governor (new page state, tests). Does not touch pins unless asked. */
  function reset() { T.rate = 1; T.target = 1; T.gap = 0; T.catching = false; T.snap = false; T.held = false; T.suppress = false; T.hover = null; T.focus = false; T.pinned = false; }

  SL.time = {
    T, C, tick, setHover, setFocus, pin, release, setMode, noteKey, reset,
    holdWanted, slowWanted,
    get rate() { return T.rate; }, get held() { return T.held; }, get pinned() { return T.pinned; }, get wall() { return T.wall; },
    get catching() { return T.catching; }, get gap() { return T.gap; },
    /** Wall seconds since the last key press (the approvals' quiet period). */
    quiet() { return (T.wall - T.quietSince) / 1000; },
  };
})(SL);
