/* 00-namespace.js: the one global of the UI. The mock concatenated its modules inside a single function that declared `SL`;
 * as separate classic scripts the same object is a window property, created here before any module runs. Every module keeps its
 * own function scope and reads the namespace as the free variable `SL`. */
window.SL = {};
