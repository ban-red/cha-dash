package apidoc

import (
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/security"
)

// Mirrors of what the settings handlers read and write: Security, Sign-in
// (internal/security) and History (internal/server/history.go).
type (
	securityUpdate struct {
		Posture *string `json:"posture"`
		StepUp  *string `json:"step_up"`
	}
	// dozzleSettings mirrors internal/server/dozzle.go's DozzleSettings: the
	// server package imports this one.
	dozzleSettings struct {
		URL string `json:"url"`
	}
	dozzleUpdate struct {
		URL *string `json:"url"`
	}
	agentsUpdate struct {
		Enabled *bool `json:"enabled"`
	}
	signInUpdate struct {
		auth.SignInSettings
		// ClientSecret: null keeps the stored one, "" removes it.
		ClientSecret *string `json:"client_secret"`
	}
	historySettings struct {
		Level       history.Level           `json:"level"`
		Locked      bool                    `json:"locked"`
		Demo        bool                    `json:"demo"`
		MaxBytes    int64                   `json:"max_bytes"`
		TrimmedAt   *int64                  `json:"trimmed_at"`
		Series      int                     `json:"series"`
		Resources   int                     `json:"resources"`
		FileBytes   int64                   `json:"file_bytes"`
		Tiers       []historyTier           `json:"tiers"`
		Instances   []historyInstance       `json:"instances"`
		Excluded    []string                `json:"excluded"`
		ExcludeKeys []historyPattern        `json:"exclude_keys"`
		Estimates   map[history.Level]int64 `json:"estimates"`
	}
	historyPattern struct {
		Pattern string `json:"pattern"`
		Series  int    `json:"series"`
	}
	historyInstance struct {
		ID     string `json:"id"`
		Series int    `json:"series"`
		Bytes  int64  `json:"bytes"`
	}
	historyTier struct {
		Tier          string  `json:"tier"`
		StepMs        int64   `json:"step_ms"`
		KeepMs        int64   `json:"keep_ms"`
		DefaultKeepMs int64   `json:"default_keep_ms"`
		Custom        bool    `json:"custom"`
		ChoicesMs     []int64 `json:"choices_ms"`
		Chunks        int     `json:"chunks"`
		Bytes         int64   `json:"bytes"`
		Oldest        *int64  `json:"oldest"`
	}
	historyUpdate struct {
		Level       *string            `json:"level"`
		MaxBytes    *int64             `json:"max_bytes"`
		Exclude     *[]string          `json:"exclude"`
		ExcludeKeys *[]string          `json:"exclude_keys"`
		Keep        *map[string]*int64 `json:"keep"`
	}
)

func init() {
	add("settings", map[string]Op{
		"GET /api/settings/security":       {Summary: "The security posture, the step-up rule and their options.", Resp: security.View{}},
		"PUT /api/settings/security":       {Summary: "Change the posture or the step-up rule; the rule needs a step-up.", Body: securityUpdate{}, Resp: security.View{}, Errors: []string{"step_up_required"}},
		"GET /api/settings/agents":         {Summary: "The Agents switch: whether agent tokens can be made.", Resp: security.AgentSettings{}},
		"PUT /api/settings/agents":         {Summary: "Turn Agents on or off.", Body: agentsUpdate{}, Resp: security.AgentSettings{}, Errors: []string{"invalid"}},
		"GET /api/settings/dozzle":         {Summary: "Dozzle's address, where a container's sheet links its logs; \"\" when unset.", Resp: dozzleSettings{}},
		"PUT /api/settings/dozzle":         {Summary: "Set Dozzle's address, or \"\" to turn the links off.", Body: dozzleUpdate{}, Resp: dozzleSettings{}, Errors: []string{"invalid"}},
		"GET /api/settings/sign-in":        {Summary: "OIDC, forward-auth and the group mappings.", Resp: security.SignInView{}},
		"PUT /api/settings/sign-in":        {Summary: "Change OIDC, forward-auth or the group mappings. Needs a step-up.", Body: signInUpdate{}, Resp: security.SignInView{}, Errors: []string{"step_up_required"}},
		"POST /api/settings/sign-in/test":  {Summary: "Check the OIDC provider's discovery and offer a test sign-in.", Resp: security.TestView{}},
		"GET /api/settings/history":        {Summary: "The history level, what history.db holds and the retention choices.", Resp: historySettings{}},
		"PUT /api/settings/history":        {Summary: "Change the history level, size limit, exclusions or retention.", Body: historyUpdate{}, Resp: historySettings{}, Errors: []string{"history_locked"}},
		"POST /api/settings/history/clear": {Summary: "Clear the history, or one integration's.", Query: []Param{{Name: "instance", Desc: "Only this integration instance's series."}}, Resp: historySettings{}, Errors: []string{"history_locked"}},
	})
}
