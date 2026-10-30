package session

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/lessons"
	"github.com/anemos-labs/sleipnir/internal/lore"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// EmbedderRole and RerankerRole are the names under which models.roles and --role-model give the models of the lesson memory
// (internal/lessons): an optional embeddings model, which finds lessons by meaning as well as by keyword, and an optional
// reranker that orders what was found. Like the compactor they are not agents. A session started with --no-memory uses neither.
const (
	EmbedderRole = "embedder"
	RerankerRole = "reranker"
)

// Bounds of the lesson memory's work for one goal.
const (
	lessonsShown      = 5                // lessons a goal's first message carries at most
	lessonsCandidates = 12               // what the embeddings propose for the reranker to order
	lessonsTimeout    = 30 * time.Second // retrieval at a goal's start
	distillTimeout    = 90 * time.Second // recording and distilling at its end
	distillMaxTokens  = 1500
	lessonsEarlier    = 8 // earlier lessons the distiller is shown, which a new one may replace
)

// lessonMemory is a session's lesson memory: the store under ~/.sleipnir and the endpoints that embed and rerank.
type lessonMemory struct {
	store *lessons.Store
	emb   *lessons.HTTPEmbedder // nil without an embedder: lessons are found by keyword alone
	rr    *lessons.HTTPReranker // nil without a reranker
}

// embedder is m's embedder as the store takes it: a nil interface, not a nil pointer, without one.
func (m *lessonMemory) embedder() lessons.Embedder {
	if m.emb == nil {
		return nil
	}
	return m.emb
}

// roleRef is the model a non-agent role is named with: --role-model first, then models.roles.
func (s *Session) roleRef(role string) string {
	if ref := s.opts.RoleModels[role]; ref != "" {
		return ref
	}
	return s.cfg.Models.Roles[role]
}

// buildLessons turns the lesson memory on when the session has the global memory and the lessons plugin is on. Lessons are
// found by keyword, and by meaning too when the embedder role names a model.
func (s *Session) buildLessons() error {
	if s.opts.NoMemory || !s.pluginOn(plugin.Lessons) {
		return nil
	}
	m := &lessonMemory{store: lessons.NewStore(filepath.Join(stateRoot(s.opts.Home), "lessons.json"))}
	if ref := s.roleRef(EmbedderRole); ref != "" {
		base, key, headers, model, err := s.rawEndpoint(ref)
		if err != nil {
			return fmt.Errorf("embedder model %s: %w", ref, err)
		}
		m.emb = lessons.NewHTTPEmbedder(base, key, model, headers, nil)
	}
	if rref := s.roleRef(RerankerRole); rref != "" {
		base, key, headers, model, err := s.rawEndpoint(rref)
		if err != nil {
			return fmt.Errorf("reranker model %s: %w", rref, err)
		}
		m.rr = lessons.NewHTTPReranker(base, key, model, headers, nil)
	}
	s.lessons = m
	return nil
}

// rawEndpoint is where requests that are not chat (embeddings, rerank) for ref go: the provider's checked base URL, its key and
// headers, and the model id. The checks are the chat client's: a key never follows a base URL its provider is not known to use.
func (s *Session) rawEndpoint(ref string) (base, key string, headers map[string]string, model string, err error) {
	mr, err := ResolveModel(s.cfg, ref)
	if err != nil {
		return "", "", nil, "", err
	}
	p, ok := lookupProvider(s.cfg, mr.Provider)
	if !ok {
		return "", "", nil, "", fmt.Errorf("unknown provider %q", mr.Provider)
	}
	if p.Auth == config.AuthChatGPTPlan {
		return "", "", nil, "", fmt.Errorf("provider %q is a ChatGPT plan, which serves chat only", mr.Provider)
	}
	key = p.APIKey()
	if p.APIKeyEnv != "" && key == "" {
		return "", "", nil, "", fmt.Errorf("provider %q needs %s to be set", mr.Provider, p.APIKeyEnv)
	}
	if base, err = endpointOf(mr.Provider, p, key); err != nil {
		return "", "", nil, "", err
	}
	return base, key, p.Headers, mr.Model, nil
}

// LessonsOn reports whether the session has a lesson memory.
func (s *Session) LessonsOn() bool { return s.lessons != nil }

// GoalStart is the first message of goal g (goal.Start) with, when the session has a lesson memory, the lessons of earlier sessions
// that are closest to its objective; their ids are kept in g to be credited or debited at its end. A memory that cannot be asked
// costs the goal nothing but a notice.
func (s *Session) GoalStart(ctx context.Context, g *goal.State) string {
	msg := goal.Start(g)
	if s.lessons == nil || g == nil {
		return msg
	}
	ctx, cancel := context.WithTimeout(ctx, lessonsTimeout)
	defer cancel()
	m := s.lessons
	k := lessonsShown
	if m.rr != nil {
		k = lessonsCandidates
	}
	found, err := m.store.RetrieveIn(ctx, m.embedder(), g.Objective, lore.Key(s.opts.Root), k)
	if err == nil && m.rr != nil {
		found, err = lessons.Rerank(ctx, m.rr, g.Objective, found, lessonsShown)
	}
	s.chargeLessons()
	if err != nil {
		s.notice("", "lesson memory: "+short(provider.SanitizeText(err.Error(), 200), 200)+"; the goal starts without lessons")
		return msg
	}
	g.Lessons = g.Lessons[:0]
	for _, l := range found {
		g.Lessons = append(g.Lessons, l.ID)
	}
	s.Log.Emit("", "lessons.retrieve", map[string]any{"ids": g.Lessons, "reranked": m.rr != nil})
	if block := lessons.Render(found); block != "" {
		msg += "\n\n" + block
	}
	return msg
}

// goalEnded is the lesson memory's part of a goal that ended: met (won) or given up on by the harness. The lessons it was shown
// are credited or debited, and new ones are distilled from its verdicts on the judge's model and saved. It is bounded in time; a
// failure is a notice, never an error of the goal.
func (s *Session) goalEnded(ctx context.Context, g *goal.State, won bool, outcome string) {
	if s.lessons == nil || g == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), distillTimeout)
	defer cancel()
	m := s.lessons
	shown := g.Lessons
	err := m.store.Record(shown, won)
	g.Lessons = nil
	var saved lessons.Saved
	if err == nil {
		earlier := m.related(ctx, g.Objective, lore.Key(s.opts.Root), shown)
		var drafts []lessons.Draft
		if drafts, err = s.distill(ctx, g, outcome, earlier); err == nil && len(drafts) > 0 {
			for i := range drafts {
				drafts[i].Project = lore.Key(s.opts.Root)
				// Lessons reach every later goal in every project: a credential the model copied into one does not.
				drafts[i].Text = config.ScrubText(drafts[i].Text)
				drafts[i].Task = config.ScrubText(drafts[i].Task)
				// The model retires only a lesson it was shown, never one it guessed or was told the id of.
				if !slices.ContainsFunc(earlier, func(l lessons.Lesson) bool { return l.ID == drafts[i].Replaces }) {
					drafts[i].Replaces = ""
				}
			}
			saved, err = m.store.Save(ctx, m.embedder(), drafts)
		}
	}
	s.chargeLessons()
	if err != nil {
		s.notice("", "lesson memory: "+short(provider.SanitizeText(err.Error(), 200), 200))
		return
	}
	s.Log.Emit("", "lessons.learn", map[string]any{"won": won, "recorded": len(shown), "added": saved.Added, "merged": saved.Merged, "replaced": saved.Replaced})
}

// related is what the distiller is shown of the lessons saved earlier, which a new lesson may replace: those the goal was shown
// (that are still kept), then the others closest to its objective, at most lessonsEarlier. A memory that cannot be read gives
// none: the goal's lessons are still distilled, only none can be replaced.
func (m *lessonMemory) related(ctx context.Context, objective, project string, shown []string) []lessons.Lesson {
	all, err := m.store.Load()
	if err != nil || len(all) == 0 {
		return nil
	}
	var out []lessons.Lesson
	for _, l := range all {
		if slices.Contains(shown, l.ID) {
			out = append(out, l)
		}
	}
	near, _ := m.store.RetrieveIn(ctx, m.embedder(), objective, project, lessonsEarlier)
	for _, l := range near {
		if len(out) < lessonsEarlier && !slices.ContainsFunc(out, func(o lessons.Lesson) bool { return o.ID == l.ID }) {
			out = append(out, l)
		}
	}
	return out[:min(len(out), lessonsEarlier)]
}

// distill asks the judge's model (else the session's) for the lessons of goal g, showing it the earlier lessons it may replace.
func (s *Session) distill(ctx context.Context, g *goal.State, outcome string, earlier []lessons.Lesson) ([]lessons.Draft, error) {
	p, m := s.Provider, s.Model
	if s.judgeP != nil {
		p, m = s.judgeP, s.judgeModel
	}
	prompt := &core.Prompt{
		Model:    m.ID,
		System:   []core.Block{core.Text(lessons.DistillSystem)},
		Params:   core.Params{MaxTokens: distillMaxTokens},
		Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text(lessons.DistillPrompt(g.Objective, outcome, g.Verdicts, earlier))}}},
	}
	resp, err := p.Do(ctx, &provider.Request{Prompt: prompt, Label: "lessons:distill", NoStream: true}, nil)
	if err != nil {
		return nil, err
	}
	usd := m.Price.USD(resp.Usage)
	if resp.CostUSD != nil {
		usd = *resp.CostUSD
	}
	s.mu.Lock()
	s.judgeUSD += usd
	s.judgeListUSD += m.ListUSD(resp.Usage)
	s.judgePlan = s.judgePlan || m.Plan
	s.mu.Unlock()
	drafts := lessons.ParseDrafts(resp.Turn.PlainText())
	task := lessons.Clean(g.Objective, lessons.MaxTaskRunes)
	for i := range drafts {
		drafts[i].Task = task
	}
	return drafts, nil
}

// chargeLessons adds what the embeddings and rerank endpoints reported since the last call to the session's spend.
func (s *Session) chargeLessons() {
	if s.lessons == nil {
		return
	}
	var usd float64
	if s.lessons.emb != nil {
		usd = s.lessons.emb.TakeUsage().Cost
	}
	if s.lessons.rr != nil {
		usd += s.lessons.rr.TakeUsage().Cost
	}
	if usd > 0 {
		s.mu.Lock()
		s.judgeUSD += usd
		s.mu.Unlock()
	}
}

// goalOutcome says how a goal the harness stopped ended for the lesson memory: won when it was met, lost when the harness gave up
// on it (no progress, its continuations used up), and nothing (ok false) when it stopped for another reason: a person paused or
// interrupted it, it is blocked on them, or the judge could not be asked.
func goalOutcome(g *goal.State, a goal.Action) (won bool, outcome string, ok bool) {
	switch {
	case a == goal.Stop:
		return true, "the goal was met: " + g.Reason, true
	case a == goal.Pause && (strings.HasPrefix(g.Paused, "no progress") || strings.HasSuffix(g.Paused, "continuations used")):
		return false, "the harness gave up on the goal (" + g.Paused + "); the judge's last word: " + g.Reason, true
	}
	return false, "", false
}
