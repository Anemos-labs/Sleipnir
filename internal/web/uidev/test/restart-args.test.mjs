// restart-args.test.mjs: /restart splits its flags the way the terminal does (cmd/sleipnir splitArgs), so a quoted value such as a
// --verify command with spaces reaches the server as one argument.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

test('splitArgs keeps quoted words together and has no other syntax', async () => {
  const L = await boot(), split = L.SL.u.splitArgs;
  const cases = [['', []], ['  ', []], ['--swarm 8', ['--swarm', '8']], ['--verify "go test ./..."', ['--verify', 'go test ./...']],
    ["--verify='a && b'", ['--verify=a && b']], ['--verify ""', ['--verify', '']], ['a\tb', ['a', 'b']], ['say "it\'s"', ['say', "it's"]],
    ['$HOME \\x', ['$HOME', '\\x']]];
  for (const [line, want] of cases) assert.deepEqual([...split(line)], want, 'splitArgs(' + JSON.stringify(line) + ')');
});

test('/restart sends a quoted --verify command as one flag value', async () => {
  const L = await boot(), SL = L.SL;
  let sent = null;
  SL.ui.confirm = o => o.run();
  SL.act.restart = flags => { sent = [...flags]; return { ok: true }; };
  SL.palette.runLine('/restart --verify "cargo fmt --check && cargo test" --swarm 4');
  assert.deepEqual(sent, ['--verify', 'cargo fmt --check && cargo test', '--swarm', '4']);
});
