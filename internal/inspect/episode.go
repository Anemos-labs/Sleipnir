package inspect

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxEpisodeBytes = 4 << 20

// episodeFile mirrors the parts of rl.Episode (docs/TRAINING-DATA.md section 4)
// the inspector shows. It is declared here rather than imported so the inspector
// depends on the JSON contract, not on the RL packages.
type episodeFile struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
	Group  string `json:"group"`
	Sample int    `json:"sample"`
	Policy struct {
		Model string `json:"model"`
	} `json:"policy"`
	Agents  []json.RawMessage `json:"agents"`
	Outcome struct {
		Verifier *struct {
			Pass  bool    `json:"pass"`
			Score float64 `json:"score"`
		} `json:"verifier"`
		Claimed string `json:"claimed"`
	} `json:"outcome"`
	Reward struct {
		Total      float64            `json:"total"`
		Components map[string]float64 `json:"components"`
	} `json:"reward"`
	Flags []string `json:"flags"`
	Cost  struct {
		USD      float64 `json:"usd"`
		ITE      float64 `json:"ite"`
		Requests int     `json:"requests"`
	} `json:"cost"`
}

// readEpisode parses episode.json next to the log (rollout directories have
// one). It returns nil when there is none or it is unreadable; a missing
// episode is the normal case for an ordinary session.
func (s *Session) readEpisode() *episodeRead {
	if s.dir == "" {
		return nil
	}
	p := filepath.Join(s.dir, "episode.json")
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxEpisodeBytes {
		return &episodeRead{}
	}
	key := fmt.Sprintf("%d/%d", fi.Size(), fi.ModTime().UnixNano())
	s.mu.RLock()
	same := key == s.episodeKey
	s.mu.RUnlock()
	if same {
		return nil // unchanged: keep what we have
	}
	f, err := openRegular(p)
	if err != nil {
		return &episodeRead{}
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxEpisodeBytes))
	if err != nil {
		return &episodeRead{}
	}
	var ef episodeFile
	if json.Unmarshal(data, &ef) != nil {
		return &episodeRead{key: key}
	}
	info := &EpisodeInfo{
		ID: oneLine(ef.ID, 120), TaskID: oneLine(ef.TaskID, 120), Group: oneLine(ef.Group, 120), Sample: ef.Sample,
		Policy: oneLine(ef.Policy.Model, 120), Claimed: oneLine(ef.Outcome.Claimed, 40),
		Reward: ef.Reward.Total, CostUSD: ef.Cost.USD, CostITE: ef.Cost.ITE, Requests: ef.Cost.Requests, Agents: len(ef.Agents),
	}
	if v := ef.Outcome.Verifier; v != nil {
		pass, score := v.Pass, v.Score
		info.Pass, info.Score = &pass, &score
	}
	if len(ef.Reward.Components) > 0 {
		info.Components = map[string]float64{}
		for k, v := range ef.Reward.Components {
			if len(info.Components) >= 24 {
				break
			}
			info.Components[oneLine(k, 40)] = v
		}
	}
	info.Flags = capStrings(ef.Flags, 16)
	return &episodeRead{info: info, key: key}
}

// episodeRead is the result of readEpisode: nil means "unchanged".
type episodeRead struct {
	info *EpisodeInfo
	key  string
}

// setEpisodeLocked installs nonnil episode metadata and its cache key; the caller must hold the
// session lock.
func (s *Session) setEpisodeLocked(e *episodeRead) {
	if e == nil {
		return
	}
	s.episode, s.episodeKey = e.info, e.key
}

// rlSummaryLocked gathers the RL fields present in the log, or nil when none are.
func (s *Session) rlSummaryLocked() *RLSummary {
	r := &s.rl
	if s.episode == nil && len(r.outcomes) == 0 && r.wireHashes == 0 && r.tokenTraces == 0 {
		return nil
	}
	kinds := make(map[string]int, len(r.kinds))
	for k, v := range r.kinds {
		kinds[k] = v
	}
	return &RLSummary{
		Episode: s.episode, Outcomes: append([]Outcome(nil), r.outcomes...), Kinds: kinds,
		WireHashes: r.wireHashes, TokenTraces: r.tokenTraces, Renderers: append([]string(nil), r.renderers...),
	}
}
