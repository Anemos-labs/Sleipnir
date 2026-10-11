// fsfake.mjs: the server's side of a folder field (GET /api/fs/dirs) over a small tree of folders, for the tests of the New session
// dialog and of the Schedule form: what internal/web/dirs answers for a path, written the way the page reads the answer.
export const HOME = '/home/me', BASE = '/p/a';
/** The folders: by path (file: a file; git: a repository; empty; verify: what its files suggest). */
const TREE = {
  '/': {}, '/home': {}, [HOME]: {}, [HOME + '/code']: {}, [HOME + '/code/api-server']: { git: true }, [HOME + '/code/shop']: { verify: [{ cmd: 'go test {dirs}', why: 'go.mod' }] },
  [HOME + '/code/notes']: {}, [HOME + '/code/Sandbox']: { empty: true }, [HOME + '/code/readme.txt']: { file: true }, '/p': {}, [BASE]: {},
};
const join = (a, b) => (a === '/' ? '' : a) + '/' + b, dirname = p => p.slice(0, p.lastIndexOf('/')) || '/';
/** What GET /api/fs/dirs?path= answers for a path, as internal/web/dirs does. */
export function answer(text) {
  const t = text.trim(); let p = t === '~' || t.startsWith('~/') ? HOME + t.slice(1) : t;
  if (!p.startsWith('/')) p = BASE + '/' + p;
  const whole = t === '~' || /\/$/.test(t) || /\/\.\.$/.test(t), abs = p.replace(/\/+$/, '') || '/', i = p.lastIndexOf('/');
  const parent = whole ? abs : p.slice(0, i) || '/', prefix = whole ? '' : p.slice(i + 1);
  const entries = Object.keys(TREE).filter(k => k !== '/' && dirname(k) === parent && !TREE[k].file).map(k => k.slice(k.lastIndexOf('/') + 1))
    .filter(n => !n.startsWith('.') && n.toLowerCase().startsWith(prefix.toLowerCase())).sort((a, b) => (+b.startsWith(prefix) - +a.startsWith(prefix)) || a.toLowerCase().localeCompare(b.toLowerCase()))
    .map(n => ({ name: n, path: join(parent, n), repo: !!TREE[join(parent, n)].git || undefined }));
  const node = TREE[abs], up = TREE[dirname(abs)];
  return { dir: parent, prefix, entries, home: HOME, sep: '/', state: { path: abs, exists: !!node, dir: !!node && !node.file, readable: !!node && !node.file, parentOk: !!up && !up.file, known: abs === BASE,
    empty: !!(node && node.empty), git: node && node.git ? 'repo' : 'none', verify: (node && node.verify) || [] } };
}
