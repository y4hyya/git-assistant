package ui

import (
	"strings"
	"testing"
)

// syncModel parks a model on the sync dialog with both offers live.
func syncModel(t *testing.T) Model {
	t.Helper()
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepSync)
	m.branch = "feat"
	m.syncMainBranchName = "main"
	m.syncPullCurrent = true
	m.syncSyncMain = true
	m.syncIncomingCurr = []string{"upstream a"}
	m.syncIncomingMain = []string{"main a", "main b"}
	m.syncCurrTotal = 1
	m.syncMainTotal = 2
	return m
}

// Both keys are gated on the offer actually being on screen. Pressing `p` when
// the dialog is only offering a main-sync would start a pull the screen never
// mentioned — and against an upstream the branch may not even have.
func TestSyncKeysAreGatedOnTheOfferShown(t *testing.T) {
	t.Run("pull-offered", func(t *testing.T) {
		m := syncModel(t)
		m, cmd := key(t, m, "p")
		if !m.pulling || m.pullingKind != pullKindCurrent {
			t.Fatalf("pulling=%v kind=%v", m.pulling, m.pullingKind)
		}
		if cmd == nil {
			t.Fatal("no command — nothing was actually pulled")
		}
	})

	t.Run("pull-not-offered", func(t *testing.T) {
		m := syncModel(t)
		m.syncPullCurrent = false
		m, cmd := key(t, m, "p")
		if m.pulling || cmd != nil {
			t.Fatalf("p started a pull that was never offered: pulling=%v cmd=%v", m.pulling, cmd)
		}
		if m.step != stepSync {
			t.Fatalf("step = %d, want to stay on the dialog", m.step)
		}
	})

	t.Run("sync-offered", func(t *testing.T) {
		m := syncModel(t)
		m, cmd := key(t, m, "s")
		if !m.pulling || m.pullingKind != pullKindMain {
			t.Fatalf("pulling=%v kind=%v", m.pulling, m.pullingKind)
		}
		if cmd == nil {
			t.Fatal("no command — nothing was actually synced")
		}
	})

	t.Run("sync-not-offered", func(t *testing.T) {
		m := syncModel(t)
		m.syncSyncMain = false
		m, cmd := key(t, m, "s")
		if m.pulling || cmd != nil {
			t.Fatalf("s started a sync that was never offered: pulling=%v cmd=%v", m.pulling, cmd)
		}
	})
}

// Skipping has to clear the dialog's contents, or the next time it opens it
// renders a commit list from the previous repository state.
func TestSyncSkipClearsTheDialogAndReturnsToTheCaller(t *testing.T) {
	for _, k := range []string{"enter", "esc", "n"} {
		t.Run(k, func(t *testing.T) {
			m := syncModel(t)
			m.syncReturnStep = stepMenu
			m, cmd := key(t, m, k)

			if m.step != stepMenu {
				t.Fatalf("step = %d, want the menu", m.step)
			}
			if m.syncPullCurrent || m.syncSyncMain {
				t.Fatal("the offers survived the skip")
			}
			if m.syncIncomingCurr != nil || m.syncIncomingMain != nil {
				t.Fatal("the commit lists survived the skip")
			}
			if m.syncCurrTotal != 0 || m.syncMainTotal != 0 || m.syncAhead != 0 || m.syncDiverged {
				t.Fatal("the counters survived the skip")
			}
			if cmd == nil {
				t.Fatal("returning to the menu did not request a refresh")
			}
		})
	}
}

// The dialog also opens in front of a push. Skipping it there must land back
// on the push screen, not on the dashboard.
func TestSyncSkipReturnsToTheStepThatOpenedIt(t *testing.T) {
	m := syncModel(t)
	m.syncReturnStep = stepPush
	m, _ = key(t, m, "esc")

	if m.step != stepPush {
		t.Fatalf("step = %d, want the push screen back", m.step)
	}
	if m.syncPullCurrent || m.syncSyncMain {
		t.Fatal("the offers survived the skip")
	}
}

func TestSyncQuits(t *testing.T) {
	m := syncModel(t)
	m, cmd := key(t, m, "q")
	if !m.quitting || cmd == nil {
		t.Fatalf("quitting=%v cmd=%v", m.quitting, cmd)
	}
}

// Once a pull is in flight the dialog stops answering keys — a second `p`
// would run a second merge against a repository already mid-merge.
func TestSyncBlocksInputWhileAPullIsInFlight(t *testing.T) {
	m := syncModel(t)
	m.pulling = true
	m.pullingKind = pullKindCurrent

	for _, k := range []string{"p", "s", "esc", "enter", "q"} {
		next, _ := key(t, m, k)
		if next.step != stepSync {
			t.Fatalf("%q left the dialog mid-pull (step %d)", k, next.step)
		}
		if next.quitting {
			t.Fatalf("%q quit during an in-flight pull", k)
		}
		if next.pullingKind != pullKindCurrent {
			t.Fatalf("%q changed the operation under way", k)
		}
	}
}

// The spinner has to keep ticking while the pull runs.
func TestSyncForwardsSpinnerTicksWhilePulling(t *testing.T) {
	m := syncModel(t)
	m.pulling = true

	_, cmd := m.Update(m.spinner.Tick())
	if cmd == nil {
		t.Fatal("the spinner stopped ticking during the pull")
	}
}

// The header understates nothing: the true count comes from the counter, not
// from how many subjects the sample happens to hold.
func TestSyncViewDisclosesCommitsBeyondTheSample(t *testing.T) {
	m := syncModel(t)
	m.syncCurrTotal = 25
	m.syncIncomingCurr = make([]string, syncCommitSample)
	for i := range m.syncIncomingCurr {
		m.syncIncomingCurr[i] = "subject"
	}

	out := m.View()
	if !strings.Contains(out, "25 new") {
		t.Fatalf("view does not carry the true count:\n%s", out)
	}
	if !strings.Contains(out, "and 15 more") {
		t.Fatalf("view does not disclose the truncation:\n%s", out)
	}
}

// Every shape of the dialog has to render: diverged, both offers, and each
// offer alone.
func TestSyncViewRendersEveryShape(t *testing.T) {
	shapes := []struct {
		name  string
		apply func(*Model)
	}{
		{"both", func(m *Model) {}},
		{"pull-only", func(m *Model) { m.syncSyncMain = false }},
		{"sync-only", func(m *Model) { m.syncPullCurrent = false }},
		{"diverged", func(m *Model) { m.syncDiverged = true; m.syncAhead = 2 }},
		{"pulling", func(m *Model) { m.pulling = true }},
		{"syncing", func(m *Model) { m.pulling = true; m.pullingKind = pullKindMain }},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			m := syncModel(t)
			s.apply(&m)
			if strings.TrimSpace(m.View()) == "" {
				t.Fatal("rendered nothing")
			}
		})
	}
}

// A diverged branch gets the warning AND the pointer at force-with-lease —
// pulling would merge the pre-rewrite commits back in.
func TestSyncDivergedViewPointsAtTheForcePush(t *testing.T) {
	m := syncModel(t)
	m.syncDiverged = true
	m.syncAhead = 2
	m.syncCurrTotal = 3

	out := m.View()
	if !strings.Contains(out, "diverged") {
		t.Fatalf("view does not name the divergence:\n%s", out)
	}
	if !strings.Contains(out, "force-with-lease") {
		t.Fatalf("view does not point at the safe way out:\n%s", out)
	}
}
