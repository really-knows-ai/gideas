package components

import (
	"strings"
	"testing"

	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

func TestArtefactLoadingState(t *testing.T) {
	m := NewArtefactTree()
	v := m.View()
	if !strings.Contains(v, "Loading artefacts") {
		t.Error("expected 'Loading artefacts' in view, got:", v)
	}
}

func TestArtefactLoadedState(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{ArtefactID: "haiku", GovernedBy: "haiku"},
	}
	v := m.View()
	if !strings.Contains(v, "haiku") {
		t.Error("expected artefact ID in view, got:", v)
	}
}

func TestArtefactExpandedShowsContent(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true, Content: "old pond"},
	}
	v := m.View()
	if !strings.Contains(v, "old pond") {
		t.Error("expected content text in view, got:", v)
	}
}

func TestArtefactCollapsedHidesContent(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{ArtefactID: "haiku", GovernedBy: "haiku", Expanded: false, Content: "hidden content"},
	}
	v := m.View()
	if strings.Contains(v, "hidden content") {
		t.Error("expected hidden content, but it's visible in view:", v)
	}
}

func TestArtefactFeedbackRendered(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "FEEDBACK_STATE_NEW", SourceNode: "reviewer", Message: "needs work", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "reviewer") || !strings.Contains(v, "needs work") {
		t.Error("expected feedback source and message in view, got:", v)
	}
}

func TestArtefactFeedbackStateResolvedGreen(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "FEEDBACK_STATE_RESOLVED", SourceNode: "sort", Message: "looks good", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "RESOLVED") {
		t.Error("expected RESOLVED state in view, got:", v)
	}
}

func TestArtefactFeedbackStateDeadlockedMagenta(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "FEEDBACK_STATE_DEADLOCKED", SourceNode: "arbiter", Message: "needs ruling", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "DEADLOCKED") {
		t.Error("expected DEADLOCKED state in view, got:", v)
	}
}

func TestArtefactFeedbackStateNewCyan(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "FEEDBACK_STATE_NEW", SourceNode: "reviewer", Message: "new feedback", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "[NEW]") {
		t.Error("expected [NEW] state in view, got:", v)
	}
}

func TestArtefactFeedbackStateRejectedRed(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "FEEDBACK_STATE_REJECTED", SourceNode: "reviewer", Message: "not good", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "REJECTED") {
		t.Error("expected REJECTED state in view, got:", v)
	}
}

func TestArtefactFeedbackStateActionedYellow(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "FEEDBACK_STATE_ACTIONED", SourceNode: "node", Message: "action taken", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "ACTIONED") {
		t.Error("expected ACTIONED state in view, got:", v)
	}
}

func TestArtefactFeedbackStateWontFixGray(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "FEEDBACK_STATE_WONT_FIX", SourceNode: "node", Message: "wont fix", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "WONT") {
		t.Error("expected WONT_FIX state in view, got:", v)
	}
}

func TestArtefactFeedbackStateUnspecified(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "FEEDBACK_STATE_UNSPECIFIED", SourceNode: "node", Message: "unspecified", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "UNSPECIFIED") {
		t.Error("expected UNSPECIFIED in view, got:", v)
	}
}

func TestArtefactFeedbackStateUnknownEnum(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "FEEDBACK_STATE_CUSTOM", SourceNode: "node", Message: "custom", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "CUSTOM") {
		t.Error("expected CUSTOM state in view, got:", v)
	}
}

func TestArtefactFeedbackPrefixStripped(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "FEEDBACK_STATE_NEW", SourceNode: "reviewer", Message: "test", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "[NEW]") {
		t.Error("expected stripped state [NEW] in view, got:", v)
	}
	if strings.Contains(v, "FEEDBACK_STATE_") {
		t.Error("expected FEEDBACK_STATE_ prefix to be stripped, got:", v)
	}
}

func TestArtefactResolvedFeedbackDimmed(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "RESOLVED", SourceNode: "sort", Message: "done", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "RESOLVED") {
		t.Error("expected RESOLVED state in view, got:", v)
	}
}

func TestArtefactDeadlockedHighlighted(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-1", State: "DEADLOCKED", SourceNode: "arbiter", Message: "deadlock", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	if !strings.Contains(v, "DEADLOCKED") {
		t.Error("expected DEADLOCKED state in view, got:", v)
	}
}

func TestArtefactSortedLexicographically(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{ArtefactID: "zeta", GovernedBy: "z"},
		{ArtefactID: "alpha", GovernedBy: "a"},
		{ArtefactID: "beta", GovernedBy: "b"},
	}
	v := m.View()
	alphaIdx := strings.Index(v, "alpha")
	betaIdx := strings.Index(v, "beta")
	zetaIdx := strings.Index(v, "zeta")
	if alphaIdx < 0 || betaIdx < 0 || zetaIdx < 0 {
		t.Fatal("expected all artefact IDs in view")
	}
	if !(alphaIdx < betaIdx && betaIdx < zetaIdx) {
		t.Error("expected lexicographic order: alpha < beta < zeta, got:", v)
	}
}

func TestArtefactFeedbackSortedByTimestamp(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-3", State: "NEW", SourceNode: "n2", Message: "second", Timestamp: "2024-01-01T00:00:02Z"},
				{ID: "fb-1", State: "NEW", SourceNode: "n1", Message: "first", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	firstIdx := strings.Index(v, "first")
	secondIdx := strings.Index(v, "second")
	if firstIdx < 0 || secondIdx < 0 {
		t.Fatal("expected both feedback messages in view")
	}
	if !(firstIdx < secondIdx) {
		t.Error("expected 'first' before 'second' (timestamp ascending), got:", v)
	}
}

func TestArtefactFeedbackSecondarySortByID(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true,
			Feedback: []types.FeedbackItem{
				{ID: "fb-b", State: "NEW", SourceNode: "n2", Message: "second", Timestamp: "2024-01-01T00:00:01Z"},
				{ID: "fb-a", State: "NEW", SourceNode: "n1", Message: "first", Timestamp: "2024-01-01T00:00:01Z"},
			},
		},
	}
	v := m.View()
	firstIdx := strings.Index(v, "first")
	secondIdx := strings.Index(v, "second")
	if firstIdx < 0 || secondIdx < 0 {
		t.Fatal("expected both feedback messages in view")
	}
	if !(firstIdx < secondIdx) {
		t.Error("expected 'first' (fb-a) before 'second' (fb-b) when timestamps equal, got:", v)
	}
}

func TestArtefactBinaryHexPreview(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "data", GovernedBy: "data", Expanded: true,
			IsBinary: true, BinarySize: 50,
			Content: string([]byte{0x48, 0x65, 0x6c, 0x6c, 0x6f}),
		},
	}
	v := m.View()
	if !strings.Contains(v, "binary") || !strings.Contains(v, "50 bytes") {
		t.Error("expected binary label with size in view, got:", v)
	}
	if !strings.Contains(v, "00000000") {
		t.Error("expected hex offset in view, got:", v)
	}
}

func TestArtefactBinaryTruncated256Bytes(t *testing.T) {
	content := make([]byte, 300)
	for i := range content {
		content[i] = byte(i)
	}
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "data", GovernedBy: "data", Expanded: true,
			IsBinary: true, BinarySize: 300,
			Content: string(content),
		},
	}
	v := m.View()
	// Should show 256 bytes = 16 rows of 16 bytes
	if !strings.Contains(v, "00000000") {
		t.Error("expected start offset in view")
	}
	// 256 bytes would end at offset 0xF0 (240) - 16 rows of 16 = row 16
	if !strings.Contains(v, "000000f0") {
		t.Error("expected offset near 240 in truncated view, got:", v)
	}
	// Should NOT contain offset 0x110 (272) which is past 256
	if strings.Contains(v, "00000110") {
		t.Error("expected no content past 256 bytes, got:", v)
	}
}

func TestArtefactBinarySmallContent(t *testing.T) {
	content := []byte{0x48, 0x65, 0x6c, 0x6c, 0x6f}
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "data", GovernedBy: "data", Expanded: true,
			IsBinary: true, BinarySize: 5,
			Content: string(content),
		},
	}
	v := m.View()
	if !strings.Contains(v, "5 bytes") {
		t.Error("expected byte count in view, got:", v)
	}
	// All 5 bytes should be visible
	for _, b := range content {
		if !strings.Contains(v, formatHexByte(b)) {
			t.Errorf("expected hex byte %02x in view", b)
		}
	}
}

func formatHexByte(b byte) string {
	return strings.ToLower(string([]byte{
		hexChar(b >> 4),
		hexChar(b & 0x0f),
	}))
}

func hexChar(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'a' + n - 10
}

func TestArtefactErrorState(t *testing.T) {
	m := NewArtefactTree()
	m.Loading = false
	m.Error = "archivist unreachable"
	v := m.View()
	if !strings.Contains(v, "archivist unreachable") {
		t.Error("expected error text in view, got:", v)
	}
}

func TestArtefactBinaryPartialUTF8Valid(t *testing.T) {
	// Content that is partially valid UTF-8 then invalid (mid-stream invalid byte)
	content := string([]byte{
		'H', 'e', 'l', 'l', 'o', ' ', 0xff, 0xfe, // valid ASCII then invalid bytes
	})
	m := NewArtefactTree()
	m.Loading = false
	m.Artefacts = []types.ArtefactNode{
		{
			ArtefactID: "data", GovernedBy: "data", Expanded: true,
			IsBinary: true, BinarySize: 9,
			Content: content,
		},
	}
	v := m.View()
	if !strings.Contains(v, "binary") {
		t.Error("expected binary view for non-UTF-8 content, got:", v)
	}
}
