package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cli.eigenflux.ai/internal/controlcontext"
	"cli.eigenflux.ai/internal/output"
)

func TestHydrateFeedV2ControlContextFromAppliedCache(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	t.Setenv("EIGENFLUX_SKILLS_DIR", t.TempDir())
	if err := controlcontext.Save("test", controlcontext.Snapshot{
		OwnerAgentID: "agent-1",
		Revision:     7,
		Context:      json.RawMessage(`{"context_revision":7,"network_goal":{"text":"Find collaborators"},"intent_actions":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	payload, revision, err := hydrateFeedV2ControlContext("test", "agent-1", json.RawMessage(`{
		"schema_version":"feed.v2",
		"control_context_snapshot":null,
		"personalization":{"mode":"intent_aligned","context_revision":7,"context_delivery":"unchanged"},
		"items":[]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if revision != 7 {
		t.Fatalf("revision=%d want=7", revision)
	}
	var got struct {
		Source  string `json:"control_context_source"`
		Context struct {
			NetworkGoal struct {
				Text string `json:"text"`
			} `json:"network_goal"`
		} `json:"control_context_snapshot"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "local_applied_cache" || got.Context.NetworkGoal.Text != "Find collaborators" {
		t.Fatalf("unexpected hydrated payload: %s", payload)
	}
}

func TestHydrateFeedV2ControlContextRejectsRevisionMismatch(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	t.Setenv("EIGENFLUX_SKILLS_DIR", t.TempDir())
	if err := controlcontext.Save("test", controlcontext.Snapshot{
		OwnerAgentID: "agent-1", Revision: 6, Context: json.RawMessage(`{"context_revision":6,"intent_actions":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hydrateFeedV2ControlContext("test", "agent-1", json.RawMessage(`{
		"schema_version":"feed.v2","control_context_snapshot":null,
		"personalization":{"mode":"intent_aligned","context_revision":7},"items":[]
	}`)); err == nil {
		t.Fatal("expected revision mismatch to fail closed")
	}
}

func TestHydrateFeedV2ControlContextSavesNewFullRevision(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	t.Setenv("EIGENFLUX_SKILLS_DIR", t.TempDir())
	payload, revision, err := hydrateFeedV2ControlContext("test", "agent-1", json.RawMessage(`{
		"schema_version":"feed.v2",
		"control_context_snapshot":{"context_revision":8,"network_goal":{"text":"New goal"}},
		"personalization":{"mode":"intent_aligned","context_revision":8,"context_delivery":"full"},
		"items":[]
	}`))
	if err != nil || revision != 8 || !json.Valid(payload) {
		t.Fatalf("revision=%d payload=%s err=%v", revision, payload, err)
	}
	cached, err := controlcontext.Load("test", "agent-1")
	if err != nil || cached.Revision != 8 {
		t.Fatalf("cached=%#v err=%v", cached, err)
	}
}

func TestHydrateFeedV2PreservesContractSelection(t *testing.T) {
	for _, mode := range []string{"baseline", "intent_aligned"} {
		for _, contractKind := range []string{"absent", "empty", "supplied"} {
			t.Run(mode+"/"+contractKind, func(t *testing.T) {
				t.Setenv("EIGENFLUX_HOME", t.TempDir())
				skillsDir := t.TempDir()
				t.Setenv("EIGENFLUX_SKILLS_DIR", skillsDir)
				references := filepath.Join(skillsDir, "ef-broadcast", "references")
				if err := os.MkdirAll(references, 0700); err != nil {
					t.Fatal(err)
				}
				for name, text := range map[string]string{
					"contract.md": "CURRENT COMPLETED RULES", "baseline-contract.md": "CURRENT BASELINE RULES",
				} {
					if err := os.WriteFile(filepath.Join(references, name), []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
				}
				personalization := map[string]interface{}{"mode": mode, "context_revision": 0}
				envelope := map[string]interface{}{
					"schema_version": "feed.v2", "personalization": personalization,
					"control_context_snapshot": nil, "items": []interface{}{},
				}
				want := "CURRENT BASELINE RULES"
				if mode == "intent_aligned" {
					personalization["context_revision"] = 1
					envelope["control_context_snapshot"] = map[string]interface{}{"context_revision": 1}
					want = "CURRENT COMPLETED RULES"
				}
				switch contractKind {
				case "empty":
					envelope["output_contract"] = ""
					want = ""
				case "supplied":
					envelope["output_contract"] = "SERVER RULES"
					want = "SERVER RULES"
				}
				raw, err := json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				hydrated, _, err := hydrateFeedV2ControlContext("test", "agent-1", raw)
				if err != nil {
					t.Fatal(err)
				}
				var result map[string]json.RawMessage
				if err := json.Unmarshal(hydrated, &result); err != nil {
					t.Fatal(err)
				}
				if _, present := result["output_contract"]; present != (contractKind != "absent") {
					t.Fatalf("hydration changed server contract presence: %s", hydrated)
				}
				got, err := output.ResolveFeedContract(hydrated)
				if err != nil || got != want {
					t.Fatalf("resolved contract=%q want=%q err=%v", got, want, err)
				}
			})
		}
	}
}
