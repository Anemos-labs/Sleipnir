/* 00-namespace.js: the one global of the UI. The modules are separate classic scripts, so the object they share is a window
 * property, created here before any module runs. Every module keeps its own function scope and reads the namespace as the free
 * variable `SL`. */
window.SL = {};
