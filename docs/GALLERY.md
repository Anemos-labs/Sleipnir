# Terminal recordings

Recordings illustrate specific captured sessions. They are not performance
benchmarks or evidence that the current interface has been visually checked.

| Files | Source |
| --- | --- |
| `media/real-*.svg` | Captured provider sessions; waits may be shortened |
| `media/chat.svg`, `media/swarm.svg`, `media/cache.svg`, `media/fold.svg` | Scripted fixtures using a mock model and arranged timing or cache events |

Create provider recordings with `scripts/record-real.sh` and mock demonstrations
with `scripts/record-demo.sh`. Inspect script options before running; provider
recordings make inference requests. Fixture numbers describe the fixture only.

For interface development, capture the current binary in a real terminal:

```sh
scripts/look.sh /tmp/login.png --key Down -- sleipnir login
node scripts/svg2png.mjs docs/media/chat.svg /tmp/chat.png --at 5
```

See [UX requirements](UX.md) and [manual acceptance checks](DOGFOOD.md).

## Scripted fixtures

These recordings use a mock model. Cache events, timing, and displayed costs are
fixture data.

<img src="media/chat.svg" alt="Scripted chat fixture" width="760">
<img src="media/swarm.svg" alt="Scripted team fixture" width="760">
<img src="media/cache.svg" alt="Scripted cache fixture" width="760">
<img src="media/fold.svg" alt="Scripted compaction fixture" width="760">
