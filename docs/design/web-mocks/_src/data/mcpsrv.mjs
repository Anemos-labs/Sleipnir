// A throw-away MCP server (stdio, newline-delimited JSON-RPC) used ONLY to capture real `sleipnir mcp test` output.
// usage: node mcpsrv.mjs docs|tracker
import readline from 'node:readline';
const SPECS = {
  docs: { name: 'docs-mcp', version: '2.4.1', tools: [
    ['search_docs', 'Search the documentation by keywords'], ['get_page', 'Return one documentation page by path'],
    ['list_spaces', 'List documentation spaces'], ['list_pages', 'List pages of a space'], ['get_page_history', 'Return the revisions of a page'],
    ['get_attachment', 'Return an attachment of a page'], ['list_labels', 'List labels'], ['pages_by_label', 'Pages carrying a label'],
    ['recent_changes', 'Pages changed lately'], ['get_comments', 'Comments on a page'], ['find_owner', 'Who owns a page'],
    ['get_outline', 'Headings of a page'], ['resolve_link', 'Resolve an internal link'], ['export_page', 'Export a page as markdown']] },
  tracker: { name: 'tracker-mcp', version: '0.9.3', tools: [
    ['list_issues', 'List issues of a project'], ['get_issue', 'Return one issue'], ['search_issues', 'Search issues'] ] },
};
const spec = SPECS[process.argv[2]];
const rl = readline.createInterface({ input: process.stdin });
const send = (o) => process.stdout.write(JSON.stringify(o) + '\n');
rl.on('line', (line) => {
  let m; try { m = JSON.parse(line); } catch { return; }
  if (m.method === 'initialize') send({ jsonrpc: '2.0', id: m.id, result: { protocolVersion: m.params.protocolVersion, capabilities: { tools: { listChanged: false } }, serverInfo: { name: spec.name, version: spec.version } } });
  else if (m.method === 'tools/list') send({ jsonrpc: '2.0', id: m.id, result: { tools: spec.tools.map(([name, description]) => ({ name, description, inputSchema: { type: 'object', properties: { query: { type: 'string' } } }, annotations: { readOnlyHint: true } })) } });
  else if (m.method === 'ping') send({ jsonrpc: '2.0', id: m.id, result: {} });
  else if (m.id !== undefined && m.method) send({ jsonrpc: '2.0', id: m.id, error: { code: -32601, message: 'method not found' } });
});
rl.on('close', () => process.exit(0));
