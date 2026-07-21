package folders

import (
	"context"
	"os"
	"testing"

	"github.com/telecollection/telecollection/internal/telegram/dialogs"
)

func TestFolderName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		displayName string
		want        string
	}{
		{"plain name", "Docs", "Docs [TC]"},
		{"already marked is idempotent", "Docs [TC]", "Docs [TC]"},
		{"lowercase marker normalised", "docs [tc]", "docs [TC]"},
		{"prefix marker normalised", "[tc] Fotos", "Fotos [TC]"},
		{"surrounding whitespace trimmed", "  Docs  ", "Docs [TC]"},
		{"empty yields bare marker", "", "[TC]"},
		{"whitespace only yields bare marker", "   ", "[TC]"},
		{"marker only yields bare marker", "[TC]", "[TC]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := FolderName(tc.displayName); got != tc.want {
				t.Errorf("FolderName(%q) = %q, want %q", tc.displayName, got, tc.want)
			}
		})
	}
}

// TestFolderNameSatisfiesIsFolderTitle couples FolderName with the discovery
// predicate: whatever FolderName produces must be recognised as a folder title
// by dialogs.IsFolderTitle, otherwise a freshly created folder would not be
// listed back. It also confirms applying FolderName twice is stable.
func TestFolderNameSatisfiesIsFolderTitle(t *testing.T) {
	t.Parallel()

	inputs := []string{"Docs", "Docs [TC]", "docs [tc]", "[tc] Fotos", "  Docs  ", "", "   ", "[TC]"}
	for _, in := range inputs {
		got := FolderName(in)
		if !dialogs.IsFolderTitle(got) {
			t.Errorf("IsFolderTitle(FolderName(%q)) = false, want true (got %q)", in, got)
		}
		if again := FolderName(got); again != got {
			t.Errorf("FolderName not idempotent: FolderName(%q) = %q, want %q", got, again, got)
		}
	}
}

func TestCreateNilAPI(t *testing.T) {
	t.Parallel()
	if _, err := Create(context.Background(), nil, "Docs"); err == nil {
		t.Errorf("Create(nil api) err = nil, want error")
	}
}

func TestRenameNilAPI(t *testing.T) {
	t.Parallel()
	if err := Rename(context.Background(), nil, dialogs.Folder{}, "Docs"); err == nil {
		t.Errorf("Rename(nil api) err = nil, want error")
	}
}

func TestDeleteNilAPI(t *testing.T) {
	t.Parallel()
	if err := Delete(context.Background(), nil, dialogs.Folder{}); err == nil {
		t.Errorf("Delete(nil api) err = nil, want error")
	}
}

// TestCRUDE2E exercises Create/Rename/Delete against a real Telegram account.
// The channel-management RPCs (channels.createChannel, channels.editTitle,
// channels.deleteChannel, messages.setHistoryTTL) can only be validated over a
// live MTProto session, so this is skipped unless TELECOL_TEST_TG=1. Wiring an
// authenticated *tg.Client requires operator credentials (see client.Run and
// docs/plano/FASE-1.md 1.7) and is intentionally left to integration runs.
func TestCRUDE2E(t *testing.T) {
	if os.Getenv("TELECOL_TEST_TG") != "1" {
		t.Skip("folder CRUD end-to-end requires a live Telegram account; set TELECOL_TEST_TG=1 to opt in")
	}
	// A real run: build an authenticated client (client.New + client.Run), take
	// the *tg.Client from tg.NewClient(...).API() inside Run, then:
	//   f, err := Create(ctx, api, "e2e-"+unique)
	//   err = Rename(ctx, api, f, "e2e-renamed")
	//   err = Delete(ctx, api, f)
	// asserting each step and that dialogs.List reflects the changes. This
	// wiring depends on live credentials and is left to manual/integration
	// validation; see docs/plano/FASE-1.md 1.7.
	t.Skip("live Telegram client wiring not available in unit tests")
}
