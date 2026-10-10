// plainpage.mjs: a page in a headless Chromium on real time (scripts/web-parity.mjs's Page runs a virtual clock), for the live page against a
// server, and for timing. Same Browser as web-parity.mjs.
export async function openPlain(browser, url, { width = 1440, height = 900, mobile = false, before } = {}) {
  const { browserContextId } = await browser.send('Target.createBrowserContext');
  const { targetId } = await browser.send('Target.createTarget', { url: 'about:blank', browserContextId });
  const { sessionId } = await browser.send('Target.attachToTarget', { targetId, flatten: true });
  const P = { problems: [], b: browser, sessionId, targetId, ctx: browserContextId };
  const send = (m, a) => browser.send(m, a, sessionId);
  let loaded; const lp = new Promise(r => { loaded = r; });
  browser.handlers.set(sessionId, (m, a) => {
    if (m === 'Page.loadEventFired') loaded();
    else if (m === 'Runtime.exceptionThrown') { const d = a.exceptionDetails; P.problems.push('exception: ' + String((d.exception && d.exception.description) || d.text).split('\n')[0]); }
    else if (m === 'Runtime.consoleAPICalled' && (a.type === 'error' || a.type === 'assert')) P.problems.push('console.' + a.type + ': ' + a.args.map(x => x.value ?? x.description).join(' ').slice(0, 300));
    else if (m === 'Log.entryAdded' && a.entry.level === 'error') P.problems.push('log: ' + a.entry.text.slice(0, 200) + (a.entry.url ? ' ' + a.entry.url.slice(0, 100) : ''));
  });
  await Promise.all([send('Page.enable'), send('Runtime.enable'), send('Log.enable')]);
  await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile });
  await send('Emulation.setTimezoneOverride', { timezoneId: 'UTC' });
  if (before) await send('Page.addScriptToEvaluateOnNewDocument', { source: before });
  await send('Page.navigate', { url }); await Promise.race([lp, new Promise(r => setTimeout(r, 30000))]);
  P.eval = async expr => { const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('eval failed: ' + String((r.exceptionDetails.exception && r.exceptionDetails.exception.description) || r.exceptionDetails.text).split('\n')[0] + ' in ' + expr.slice(0, 120)); return r.result ? r.result.value : undefined; };
  P.shot = async () => Buffer.from((await send('Page.captureScreenshot', { format: 'png', fromSurface: true })).data, 'base64');
  P.metrics = async () => Object.fromEntries((await send('Performance.getMetrics')).metrics.map(m => [m.name, m.value]));
  P.enableMetrics = () => send('Performance.enable');
  P.key = async k => { const named = { ArrowDown: 40, ArrowUp: 38, Enter: 13, Escape: 27 }[k]; const base = { key: k, code: k, windowsVirtualKeyCode: named || k.toUpperCase().charCodeAt(0), nativeVirtualKeyCode: named || k.toUpperCase().charCodeAt(0) }; await send('Input.dispatchKeyEvent', { type: 'keyDown', ...base }); await send('Input.dispatchKeyEvent', { type: 'keyUp', ...base }); };
  P.waitFor = async (expr, ms = 8000) => { const t0 = Date.now(); for (;;) { if (await P.eval('!!(' + expr + ')').catch(() => false)) return true; if (Date.now() - t0 > ms) throw new Error('timed out waiting for ' + expr); await new Promise(r => setTimeout(r, 40)); } };
  P.close = async () => { browser.handlers.delete(sessionId); try { await browser.send('Target.closeTarget', { targetId }); } catch { /* gone */ } try { await browser.send('Target.disposeBrowserContext', { browserContextId }); } catch { /* gone */ } };
  return P;
}
