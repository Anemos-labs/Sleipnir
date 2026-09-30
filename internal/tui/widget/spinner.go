package widget

// The spinner and its verbs. Both are pure functions of a frame number: the caller counts frames (at most ~15 a second, see
// docs/UX.md) and nothing here reads a clock.

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var spinnerASCII = [...]string{"|", "/", "-", "\\"}

// Spinner is the braille spinner's glyph (one cell) for a frame: ten frames, repeating. Any frame number is fine; a negative one
// counts backwards.
func Spinner(frame int) string { return spinnerFrames[spinIndex(frame, len(spinnerFrames))] }

// SpinnerASCII is the spinner for terminals that cannot be trusted with braille: | / - \ (one cell), four frames, repeating.
func SpinnerASCII(frame int) string { return spinnerASCII[spinIndex(frame, len(spinnerASCII))] }

// spinIndex maps any frame number onto 0..n-1, negative ones included.
func spinIndex(frame, n int) int {
	i := frame % n
	if i < 0 {
		i += n
	}
	return i
}

// VerbFrames is how many frames a verb stays before the next one replaces it: 45, about three seconds at the UI's 15 frames
// a second. The verb is a slow signal that something is happening, not an animation.
const VerbFrames = 45

// verbList is the source of truth; Verbs is a copy for callers that want to show or test the list.
var verbList = [...]string{
	"Thinking", "Reading", "Weighing", "Tracing", "Checking", "Considering", "Mapping", "Sifting",
	"Reasoning", "Planning", "Sketching", "Scanning", "Comparing", "Inspecting", "Untangling", "Connecting",
}

// Verbs lists the calm present-participle verbs Verb chooses from. It is a copy made at start-up: changing it changes nothing
// Verb does, and it must be treated as read-only.
var Verbs = append([]string(nil), verbList[:]...)

// Verb is the verb to show next to the spinner for a frame, deterministic in seed: the same (seed, frame) is always the same
// verb, it changes every VerbFrames frames, and a different seed starts at a different verb and (for most seeds) steps through
// the list in a different order, so two agents working at once do not flicker in step. A full tour visits every verb before
// repeating and never shows the same verb twice in a row. Any seed and any frame number are fine; a negative frame counts
// backwards.
func Verb(seed, frame int) string {
	n := len(verbList)
	slot := frame / VerbFrames
	if frame%VerbFrames < 0 {
		slot--
	}
	h := verbHash(uint64(seed))
	start := int(h % uint64(n))
	// A stride coprime with n makes the walk a permutation of the list: step through the coprime numbers in 1..n-1.
	var strides [len(verbList)]int
	k := 0
	for s := 1; s < n; s++ {
		if gcdInt(s, n) == 1 {
			strides[k] = s
			k++
		}
	}
	stride := strides[int((h>>32)%uint64(k))]
	pos := (start + spinIndex(slot, n)*stride) % n
	return verbList[pos]
}

// verbHash is the splitmix64 finaliser: a cheap, well-spread hash of the seed.
func verbHash(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func gcdInt(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
